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
	pcmMaxSkipMs   = 250.0                 // 单次最多丢多久(只限单轮，不限欠账总额)
	pcmLogGap      = 10 * time.Second      // 跳读日志限流

	// 一次读卡这么久以上，才算真的遇到网络事故。
	// 平时上位机每 10-60ms 送一批，缓冲薄的时候正常等包也会卡 40-60ms，
	// 这期间声卡一直在放，不该丢；超过正常间隔一个身位才动手。
	pcmStarveGap = 80 * time.Millisecond
	// 追赶后给声卡留的垫底：丢到一点不剩会让下一次抖动直接欠载。
	pcmKeepMs = 20.0

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
	target     int64 // 典型目标排队(声卡缓冲满时)，只用于启动日志
	deadband   int64
	maxSkip    int64
	// 声卡缓冲上限。sinkBytes 是推算出来的，欠载/恢复瞬间会算出比缓冲还大的
	// 假值(ALSA 刚 prepare、Android 播放头停住)，必须夹住，否则欠账会被算小。
	sinkLimit int64

	readBytes    int64 // 从流里读到的(含被丢弃的)
	writtenBytes int64 // 写进声卡的
	skippedBytes int64 // 主动丢弃的

	// 饿读追赶没丢完的欠账。
	// 一次 Read 只拿得到 pcmReadChunkMs 那么多字节，卡了 300ms 也只能丢 15ms，
	// 剩下的必须记下来在后续轮次继续丢——否则下一轮 gap 已经很小、不再满足
	// 触发条件，欠账就永远挂在那里，表现为"丢了但没丢够"。
	starveOwed int64

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

// read 取代调用点的 r.Read：只负责发现"明显卡住"并记账，丢弃统一交给 fix()。
//
// 一次读卡了特别久(超过 pcmStarveGap)，说明上游攒了一大坨旧音频正在涌过来。
// 这里只把"卡住多久"换算成欠账记进 starveOwed，不在这里丢：
//  - 这一轮最多只拿得到 pcmReadChunkMs(15ms)，用 15ms 的铲子挖 300ms 的坑，
//    永远追不上，而卡顿期间上游还在以实时速率继续送；
//  - fix() 里的 drop() 是 io.ReadFull 循环，能一次性连读任意量，追得快。
// 所以这里记账、fix() 执行，职责分开，也不会重复计数。
func (q *pcmQueue) read(r io.Reader, buf []byte) (int, error) {
	start := time.Now()
	n, err := r.Read(buf)
	gap := time.Since(start)
	if n > 0 {
		q.readBytes += int64(n)

		// 欠账按实际卡住的时间如实记账，不按 pcmMaxSkip 截断。
		// 卡了 500ms 只记 250ms 的话，剩下 250ms 永远补不回来，
		// 下次再卡一次就累积成听感上明显的错位。
		if gap > pcmStarveGap {
			owed := msBytes(q.rate, q.frameBytes, float64(gap.Milliseconds())-pcmKeepMs)
			if owed > 0 {
				q.starveOwed += owed
				if time.Since(q.lastLog) >= pcmLogGap {
					q.lastLog = time.Now()
					log.Printf("饿读: 卡了 %dms，记欠账 %dms(待丢 %dms，累计已丢 %dms)",
						gap.Milliseconds(), q.ms(owed), q.ms(q.starveOwed),
						q.ms(q.skippedBytes))
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
	// 声卡里那部分丢不掉，不参与判断：目标 = 当前声卡积压 + 余量。
	// 原来用的是缓冲上限(85ms)，可稳态积压只有 0~15ms，等于白留 70ms 在 socket 里，
	// 每次同步完都重新留一截，越同步越慢。
	target := q.clampSink(sinkBytes) + msBytes(q.rate, q.frameBytes, pcmMarginMs)
	excess := queued - target
	if excess < q.deadband {
		excess = 0
	}

	// 饿读欠账是独立的一笔账：read() 卡住时欠下的旧音频，必须无条件补丢。
	// 它不受 available(socket 里现存量)的限制——那笔数据在卡顿期间
	// 可能已经被内核收下又被读走，用 socket 现值去限它只会丢不动。
	// 这里取较大者而不是相加，避免同一笔欠账被算两次。
	owedOnly := false
	if q.starveOwed > excess {
		excess = q.starveOwed
		owedOnly = true
	}

	if excess <= 0 {
		return nil
	}
	if excess > q.maxSkip {
		excess = q.maxSkip
	}
	// 常规积压只能用 socket 里现存的量去丢(等新数据来了再丢就等于丢最新音频)；
	// 但欠账必须丢得动，否则永远补不上。
	if !owedOnly && excess > available {
		excess = available
	}
	excess -= excess % int64(q.frameBytes)
	if excess <= 0 {
		return nil
	}
	dropped, err := q.drop(r, excess)

	// drop() 丢掉的是真实流数据，starveOwed 记的是同一笔欠账，
	// 已经在这里补上了就必须同步扣减，否则下一轮会重复计算、多丢一截。
	if dropped > 0 && q.starveOwed > 0 {
		q.starveOwed -= dropped
		if q.starveOwed < 0 {
			q.starveOwed = 0
		}
	}

	if dropped > 0 && now.Sub(q.lastLog) >= pcmLogGap {
		q.lastLog = now
		log.Printf("跳读: 排队 %dms 目标 %dms(声卡 %dms)，丢 %dms(累计 %dms，欠 %dms)",
			q.ms(queued), q.ms(target), q.ms(q.clampSink(sinkBytes)),
			q.ms(dropped), q.ms(q.skippedBytes), q.ms(q.starveOwed))
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
