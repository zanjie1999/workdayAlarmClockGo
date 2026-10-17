/*
 * 实时 PCM 流的延迟统计与跳读同步
 * zyyme 20251017
 * v1.0
 */

package player

import (
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 下位机播放实时 PCM 时的延迟问题：
// 端到端排队 = 内核接收缓冲(还没读) + 本进程已读未写 + 声卡侧缓冲(已写未播)
// 这三段在设备本地都能读到，上位机看不见的前两段，而下位机 CPU 一忙，
// 排队就在这两段里攒起来，并且因为生产和消费都是实时速率，攒下的延迟会一直保留、不会自愈。
// 所以这里持续量这段排队，一超过目标就直接从流里跳读丢弃，延迟立刻回落。
// 丢弃只发生在数据进管道/声卡之前——进去了就只能按实时速率放出来。
const (
	pcmCheckGap    = 30 * time.Millisecond // 结算间隔
	pcmReadChunkMs = 15.0                  // 每次最多读多久的音频
	pcmMarginMs    = 25.0                  // 目标在固有排队之上的余量
	pcmDeadbandMs  = 12.0                  // 死区，避免一直微调
	pcmMaxSkipMs   = 250.0                 // 单次最多丢多久
	pcmLogGap      = 10 * time.Second      // 跳读日志限流

	// 一次读卡这么久以上，才可能真的是上游被网络堵住了。
	// 正常情况上位机每 10ms 左右就有包，缓冲薄的时候一次读等 40-60ms 很正常，
	// 所以光看卡了多久会误判——还要减掉声卡当时还剩的积压(见 read)。
	pcmStarveGap = 40 * time.Millisecond
	// 追赶后给声卡留的垫底：丢到一点不剩会让下一次抖动直接欠载。
	pcmKeepMs = 20.0
	// 追赶后要等声卡积压回血到这个量，才重新允许触发。
	// 没有这道迟滞的话，丢完声卡是空的，下一次读必然又"卡很久"，会一直剪。
	pcmRearmMs = 30.0

	// 播放器/声卡侧申请的缓冲时长：aplay 的 -B 40000 和 ALSA 的 buffer_time 都是 40ms。
	// 进了声卡的排队只能按实时速率放出来，属于这条链路不可能再低下去的底。
	pcmSinkBufferMs = 40.0
)

// msBytes 把毫秒换算成字节数(按帧对齐)。
func msBytes(rate, frameBytes int, ms float64) int64 {
	return int64(float64(rate)*ms/1000) * int64(frameBytes)
}

// pcmQueue 只被播放那条协程读写，所以计数不用加锁。
type pcmQueue struct {
	rate       int
	frameBytes int
	target     int64 // 目标排队字节数(含声卡侧固有排队)
	deadband   int64
	maxSkip    int64
	// 声卡缓冲上限。sinkBytes 是推算出来的，欠载/恢复瞬间会算出比缓冲还大的
	// 假值(ALSA 刚 prepare、Android 播放头停住)，必须夹住，否则欠账会被算小。
	sinkLimit int64

	readBytes    int64 // 从流里读到的(含被丢弃的)
	writtenBytes int64 // 写进声卡的
	skippedBytes int64 // 主动丢弃的

	// 饿读追赶：一次读卡住多久，就说明上游攒了多久的旧音频，
	// 接下来涌来的数据开头那一段就是那么旧，直接丢掉。
	catchUp int64 // 还要丢多少字节
	// 一次追赶事件里累计丢了多少，只用来打日志：
	// catchUp 会跨多次读延续，打单轮的量会严重低估。
	eventBytes int64
	// 追赶完要等声卡积压回血才重新允许触发，否则丢完就饿、饿了又丢。
	armed bool

	lastCheck time.Time
	lastLog   time.Time
	buf       []byte
}

// newPCMQueue 的 sinkBytes 是这条链路不可能再低下去的排队(管道 + 声卡缓冲)。
// sinkBytes 应该是运行时实测到的真实值：ALSA 的 buffer_size 会被驱动向上取整
// (请求 40ms 实测 78-85ms)，写死的话目标会低于真实地板，跳读会一直触发。
func newPCMQueue(rate, frameBytes int, sinkBytes int64) *pcmQueue {
	return &pcmQueue{
		rate:       rate,
		frameBytes: frameBytes,
		target:     sinkBytes + msBytes(rate, frameBytes, pcmMarginMs),
		deadband:   msBytes(rate, frameBytes, pcmDeadbandMs),
		maxSkip:    msBytes(rate, frameBytes, pcmMaxSkipMs),
		sinkLimit:  sinkBytes,
		buf:        make([]byte, 8<<10),
		armed:      true,
	}
}

// clampSink 把推算出来的声卡积压夹到真实缓冲上限。
func (q *pcmQueue) clampSink(sinkBytes int64) int64 {
	if sinkBytes < 0 {
		return 0
	}
	if q.sinkLimit > 0 && sinkBytes > q.sinkLimit {
		return q.sinkLimit
	}
	return sinkBytes
}

// read 取代调用点的 r.Read，顺便处理"饿着之后涌来一大坨"的情况。
//
// 上位机被网络堵住时不会有数据发过来，这里就会一直卡在 Read 上，
// 声卡同时把缓冲放空(欠载)。等网络缓过来，上位机一口气把攒下的旧音频推过来，
// 这一坨的开头就是"卡住的那段时间"，丢掉它，剩下的正好是实时音频。
//
// 关键：卡了多久 != 声卡空了多久。缓冲薄的时候，正常等下一批包也要卡 40-60ms，
// 这期间声卡一直在放，一点没空。所以要先减掉声卡当时还剩的积压(sinkBytes)，
// 只把真正"放空了"的那部分算成欠账，否则正常节奏会被误判成饿着，一直剪。
//
// sinkBytes 是调用方在这次读之前量到的声卡侧积压。丢的都是已经读进内存的
// 字节，永远不会卡在网络上等数据。
func (q *pcmQueue) read(r io.Reader, buf []byte, sinkBytes int64) (int, error) {
	start := time.Now()
	n, err := r.Read(buf)
	gap := time.Since(start)
	if n > 0 {
		q.readBytes += int64(n)
		sinkBytes = q.clampSink(sinkBytes)
		if !q.armed {
			// 上一轮刚丢过，等声卡积压回血再重新武装，避免丢完就饿、饿了又丢
			if sinkBytes >= msBytes(q.rate, q.frameBytes, pcmRearmMs) {
				q.armed = true
			}
		} else if gap > pcmStarveGap {
			// 卡住的时间减去声卡当时还能放的时间 = 真正断粮的时长
			starved := float64(gap.Milliseconds()) - float64(q.ms(sinkBytes)) - pcmKeepMs
			owed := msBytes(q.rate, q.frameBytes, starved)
			if owed > q.maxSkip {
				owed = q.maxSkip
			}
			if owed > q.catchUp {
				q.catchUp = owed
				q.eventBytes = 0 // 新一轮事件，重新计总量
			}
		}
		if q.catchUp > 0 {
			drop := q.catchUp
			if drop > int64(n) {
				drop = int64(n)
			}
			drop -= drop % int64(q.frameBytes) // 帧对齐，别把采样劈开
			if drop > 0 {
				copy(buf, buf[drop:n])
				n -= int(drop)
				q.catchUp -= drop
				q.skippedBytes += drop
				q.eventBytes += drop
				if q.catchUp == 0 {
					q.armed = false // 丢完了，等声卡回血
				}
				if time.Since(q.lastLog) >= pcmLogGap {
					q.lastLog = time.Now()
					log.Printf("饿读追赶: 卡了 %dms 声卡剩 %dms，本次共丢 %dms(累计 %dms)",
						gap.Milliseconds(), q.ms(sinkBytes), q.ms(q.eventBytes), q.ms(q.skippedBytes))
				}
			}
		}
	}
	return n, err
}

// fix 结算一次：排队超过目标就从流里跳读丢掉多余的旧音频。
// socketBytes 是内核接收缓冲里还没读走的字节，sinkBytes 是声卡侧当前排队的字节，
// socketBytes < 0 表示这个平台查不到接收缓冲，当 0 算。
func (q *pcmQueue) fix(r io.Reader, socketBytes, sinkBytes int64) error {
	now := time.Now()
	if now.Sub(q.lastCheck) < pcmCheckGap {
		return nil
	}
	q.lastCheck = now
	// 能丢的上限 = 已经躺在接收缓冲里的量。等新数据来了再丢就等于丢最新音频，
	// 而且会把这条协程卡在丢弃里出不来，所以宁可这一轮少丢一点。
	available := socketBytes
	if socketBytes < 0 {
		socketBytes = 0
		available = math.MaxInt64
	}
	queued := q.readBytes - q.writtenBytes - q.skippedBytes + socketBytes + q.clampSink(sinkBytes)
	excess := queued - q.target
	if excess < q.deadband {
		return nil
	}
	if excess > q.maxSkip {
		excess = q.maxSkip
	}
	if excess > available {
		excess = available
	}
	excess -= excess % int64(q.frameBytes)
	if excess <= 0 {
		return nil
	}
	dropped, err := q.drop(r, excess)
	if dropped > 0 && now.Sub(q.lastLog) >= pcmLogGap {
		q.lastLog = now
		log.Printf("跳读: 排队 %dms 目标 %dms，丢 %dms(累计 %dms)",
			q.ms(queued), q.ms(q.target), q.ms(dropped), q.ms(q.skippedBytes))
	}
	return err
}

// drop 把流里 excess 字节读走丢掉。
func (q *pcmQueue) drop(r io.Reader, excess int64) (int64, error) {
	done := int64(0)
	for done < excess {
		block := excess - done
		if block > int64(len(q.buf)) {
			block = int64(len(q.buf))
		}
		n, err := io.ReadFull(r, q.buf[:block])
		q.readBytes += int64(n)
		q.skippedBytes += int64(n)
		done += int64(n)
		if err != nil {
			return done, err
		}
	}
	return done, nil
}

// ms 把字节数换算成毫秒，只用来打日志。
func (q *pcmQueue) ms(bytes int64) int64 {
	if q.rate <= 0 || q.frameBytes <= 0 {
		return 0
	}
	return bytes / int64(q.frameBytes) * 1000 / int64(q.rate)
}

var streamConns sync.Map // RemoteAddr -> net.Conn

// PCMStreamConnState 交给 http.Server.ConnState 登记连接：
// 播放实时流时要拿这条连接去问内核"接收缓冲里还压着多少没读的旧音频"。
func PCMStreamConnState(conn net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		streamConns.Store(conn.RemoteAddr().String(), conn)
	case http.StateClosed, http.StateHijacked:
		streamConns.Delete(conn.RemoteAddr().String())
	}
}

// TuneStreamConn 收紧这条连接的内核接收缓冲，让反压更快传到上位机
// (环境变量 APLAY_RCVBUF，默认 16KB，设 0 表示不动系统默认值)。
func TuneStreamConn(remoteAddr string) {
	size := rcvbufBytes()
	if size <= 0 {
		return
	}
	if conn, ok := streamConn(remoteAddr); ok {
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(size)
		}
	}
}

func streamConn(remoteAddr string) (net.Conn, bool) {
	if remoteAddr == "" {
		return nil, false
	}
	value, ok := streamConns.Load(remoteAddr)
	if !ok {
		return nil, false
	}
	conn, ok := value.(net.Conn)
	return conn, ok
}

// unreadBytes 问内核这条连接还有多少字节没被读走，未知返回 -1。
func unreadBytes(remoteAddr string) int64 {
	conn, ok := streamConn(remoteAddr)
	if !ok || socketUnread == nil {
		return -1
	}
	return int64(socketUnread(conn))
}

// socketUnread 由平台相关文件在 init 里装上(只有 Linux 有)。
var socketUnread func(net.Conn) int

var rcvbufOnce sync.Once
var rcvbufSize int

func rcvbufBytes() int {
	rcvbufOnce.Do(func() {
		rcvbufSize = 16 << 10
		value := strings.TrimSpace(os.Getenv("APLAY_RCVBUF"))
		if value == "" {
			return
		}
		if size, err := strconv.Atoi(value); err == nil && size >= 0 {
			rcvbufSize = size
		}
	})
	return rcvbufSize
}
