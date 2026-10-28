/*
 * 实时 PCM 流的延迟统计与跳读同步
 * zyyme 20251017
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
//
// 卡顿的处理分两态：
//
//   - 稳态：每 30ms 结算一次，把输入侧(内核接收缓冲 + 管道)压到 25ms 左右。
//     这是低通滤波，专门对付日常抖动，慢慢挤掉零碎积压。
//   - 追赶：一次读阻塞超过 80ms，说明刚经历网络事故(WiFi 卡顿、重传、空口拥塞)，
//     链路上挂着"已发出但还没到达"的旧音频。这时只读不写、连续把积压烧掉——
//     读取本身就是网络消费，TCP 窗口随读随开，在途的旧音频会以链路速度灌下来。
//
// 为什么只靠稳态不行：稳态每轮只丢一块(15ms)，还要等声卡节拍才有下一轮，
// 排空速度低于实时，残余会长期挂在链路上，下一次卡顿再叠一份，就是"越断越慢"。
//
// 追赶要烧多少：卡了多久就是链路上积了多久的旧音频，这是唯一不依赖平台测量的
// 硬信号(Read 阻塞了多久)。能读到内核接收缓冲时取两者较大者，真的挂得更多就
// 按链路的量烧。宁可多跳一点新鲜音频(上位机卡顿时可能已经丢过一部分旧包)，
// 也不能让积压以实时速率排出来变成永久延迟——那种延迟永远不会自愈。
const (
	pcmCheckGap    = 30 * time.Millisecond // 稳态结算间隔
	pcmReadChunkMs = 15.0                  // 每次最多读多久的音频
	pcmMarginMs    = 25.0                  // 目标在固有排队之上的余量
	pcmDeadbandMs  = 12.0                  // 死区，避免一直微调
	pcmMaxSkipMs   = 250.0                 // 稳态单轮最多丢多久
	pcmLogGap      = 10 * time.Second      // 跳读日志限流

	// 一次读卡这么久以上，才算真的遇到网络事故。
	// 平时上位机每 10-60ms 送一批，缓冲薄的时候正常等包也会卡 40-60ms，
	// 这期间声卡一直在放，不该丢；超过正常间隔一个身位才动手。
	pcmStarveGap = 80 * time.Millisecond

	// 追赶态的退出条件。
	pcmCatchWindow   = 150 * time.Millisecond // 输入速率观察窗口
	pcmCatchRate     = 1.3                    // 输入速率降到实时的这个倍数以内即视为排空
	pcmCatchCooldown = 300 * time.Millisecond // 退出后的冷静期，避免抖动反复进出

	// 单次追赶的硬上限。欠账本身不设上限(卡多久就记多久)，这个限的是"一次追多久"：
	// 追赶期间声卡是空转的，追多久就静音多久，2 秒是听感和追平之间的折中。
	// 卡 5 秒时最多追 2 秒，剩下的留到下一次卡顿一起追。
	pcmCatchLimit = 2 * time.Second

	// 播放器/声卡侧申请的缓冲时长：aplay 的 -B 40000 和 ALSA 的 buffer_time 都是 40ms。
	// 进了声卡的排队只能按实时速率放出来，属于这条链路不可能再低下去的底。
	pcmSinkBufferMs = 40.0

	// 内核接收缓冲的上限。APLAY_RCVBUF 只是诊断旋钮：调大之后卡顿时内核会
	// 替我们缓存几百毫秒旧音频，内存和延迟一起涨，小机器能直接吃光内存。
	pcmRcvbufCap = 256 << 10
)

// msBytes 把毫秒换算成字节数(按帧对齐)。
func msBytes(rate, frameBytes int, ms float64) int64 {
	return int64(float64(rate)*ms/1000) * int64(frameBytes)
}

// pcmSource 提供平台相关的两个实时量：
//   - socket：内核接收缓冲里还没被读走的字节，查不到返回 -1
//   - sink：已经写进管道/声卡、还没播出去的字节
//
// 做成回调是因为追赶态只想在读完之后量一次 socket，不必每轮都去开文件。
type pcmSource struct {
	socket func() int64
	sink   func() int64
}

// pcmQueue 只被播放那条协程读写，所以计数不用加锁。
type pcmQueue struct {
	rate       int
	frameBytes int
	backend    string
	target     int64 // 典型目标排队(声卡缓冲满时)，只用于启动日志
	deadband   int64
	maxSkip    int64
	// 声卡缓冲上限。sinkBytes 是推算出来的，欠载/恢复瞬间会算出比缓冲还大的
	// 假值(ALSA 刚 prepare、Android 播放头停住)，必须夹住，否则欠账会被算小。
	sinkLimit int64

	readBytes    int64 // 从流里读到的(含被丢弃的)
	writtenBytes int64 // 写进声卡的
	skippedBytes int64 // 主动丢弃的

	// 追赶态。进入后主循环只读不写，连续把过期音频烧掉。
	catching         bool
	catchStart       time.Time
	catchUntil       time.Time // 冷静期结束时刻
	catchTriggerGap  time.Duration
	catchDebt        int64 // 本次至少要烧掉多少字节的旧音频
	catchSkips       int64 // 本次已经烧掉多少
	catchWindowStart time.Time
	catchWindowBytes int64

	// 观测值，只给 PCMStreamStats 用。
	lastGap     time.Duration
	socketBytes int64
	sinkBytes   int64
	stalls      int64 // 识别出的真实卡顿次数
	catchUps    int64 // 进入追赶的次数
	xruns       int64 // 声卡欠载次数(有来源时才有)

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

// queuedBytes 是当前本机可见的排队：已读未写 + 内核接收缓冲 + 声卡侧未播。
func (q *pcmQueue) queuedBytes() int64 {
	queued := q.readBytes - q.writtenBytes - q.skippedBytes + q.clampSink(q.sinkBytes)
	if q.socketBytes > 0 {
		queued += q.socketBytes
	}
	if queued < 0 {
		return 0
	}
	return queued
}

// step 推进一轮，返回这一块该不该写声卡。
//
// 稳态下先结算一次排队(丢旧数据)再读一块；追赶态只读不丢声卡。
// skip=true 表示读到的 n 字节属于过期音频、已经记进 skippedBytes，调用方不要写。
func (q *pcmQueue) step(r io.Reader, buf []byte, src pcmSource) (int, bool, error) {
	defer q.publish(true)
	if q.catching {
		return q.catchUpStep(r, buf, src)
	}

	// 先量后排、再读：只有还没进管道/声卡的旧音频才丢得掉
	q.socketBytes = src.socket()
	q.sinkBytes = src.sink()
	if err := q.fix(r, q.socketBytes, q.sinkBytes); err != nil {
		return 0, false, err
	}

	start := time.Now()
	n, err := r.Read(buf)
	gap := time.Since(start)
	q.lastGap = gap
	if n <= 0 {
		return n, false, err
	}
	q.readBytes += int64(n)
	// 读完之后再看一眼内核缓冲：卡顿后涌进来的积压在读数之后才看得清
	q.socketBytes = src.socket()

	if q.maybeCatchUp(gap) {
		// 刚读到的这块本身就是迟到的旧音频，跟着一起烧掉
		q.skippedBytes += int64(n)
		q.catchSkips += int64(n)
		return n, true, err
	}
	return n, false, err
}

// maybeCatchUp 判断这次读阻塞是不是链路上积了旧音频。
//
// 触发信号是 gap：Read 阻塞了这么久，说明链路上出过事故。
// 但要不要真的追、追多少，得看内核接收缓冲里到底挂了多少——这是唯一能区分
// "链路上挂着一坨旧音频"和"上游根本没发、声卡靠自己欠载追上了"的依据：
//
//   - 卡了 500ms 但内核缓冲 0ms：上位机在阻塞写，数据没发出来，声卡欠载已经把
//     这段时间轴自动跳过去了。这时按 gap 补丢等于白丢好音频，还让声卡空等多秒。
//   - 卡了 500ms 且内核缓冲 300ms：这才是"水管里的旧水"，必须烧掉。
func (q *pcmQueue) maybeCatchUp(gap time.Duration) bool {
	if gap < pcmStarveGap {
		return false
	}
	if !q.catchUntil.IsZero() && time.Now().Before(q.catchUntil) {
		return false
	}
	gapBytes := msBytes(q.rate, q.frameBytes, float64(gap.Milliseconds()))
	chunk := msBytes(q.rate, q.frameBytes, pcmReadChunkMs)
	debt := gapBytes
	switch {
	case q.socketBytes < 0:
		// 量不到内核缓冲的机型只能按卡顿时长结算，靠 catchUpStep 里
		// "读取再次阻塞就退出"避免声卡白等
	case q.socketBytes < chunk:
		// 连一块都不到，没有旧音频可烧，不要追
		return false
	case q.socketBytes > debt:
		debt = q.socketBytes
	}
	if debt <= 0 {
		return false
	}
	q.enterCatchUp(gap, debt)
	return true
}

func (q *pcmQueue) enterCatchUp(gap time.Duration, debt int64) {
	now := time.Now()
	q.catching = true
	q.stalls++
	q.catchUps++
	q.catchTriggerGap = gap
	q.catchDebt = debt
	q.catchStart = now
	q.catchSkips = 0
	q.catchWindowStart = now
	q.catchWindowBytes = 0
	log.Printf("追赶开始: 卡了 %dms，要烧 %dms(内核缓冲 %s)，本机排队 %dms",
		gap.Milliseconds(), q.ms(debt), q.socketText(), q.ms(q.queuedBytes()))
}

// socketText 只用来打日志：量不到内核接收缓冲时写"未知"。
func (q *pcmQueue) socketText() string {
	if q.socketBytes < 0 {
		return "未知"
	}
	return strconv.FormatInt(q.ms(q.socketBytes), 10) + "ms"
}

// catchUpStep 追赶态的读法：连续读、连续丢，不等声卡节拍、不受单轮上限约束。
// 唯一的边界是 catchUpDone 的三条件加 1.5s 硬上限，所以不会把链路烧穿。
func (q *pcmQueue) catchUpStep(r io.Reader, buf []byte, src pcmSource) (int, bool, error) {
	start := time.Now()
	n, err := r.Read(buf)
	gap := time.Since(start)
	q.lastGap = gap
	if n <= 0 {
		if err != nil {
			q.exitCatchUp("读取出错")
			return 0, false, err
		}
		if time.Since(q.catchStart) >= pcmCatchLimit {
			q.exitCatchUp("超过单次上限")
		}
		return 0, true, nil
	}
	q.readBytes += int64(n)

	q.socketBytes = src.socket()
	q.sinkBytes = src.sink()

	// 这一块等了这么久才到，说明链路里没有连续的积压可烧了。
	// 立刻退出、把这块正常写下去，别让声卡继续空转——
	// 追赶期间是不写声卡的，白等多久就是静音多久。
	if gap >= pcmStarveGap {
		q.exitCatchUp("读取再次阻塞")
		return n, false, err
	}
	q.catchWindowBytes += int64(n)

	if reason := q.catchUpDone(gap); reason != "" {
		// 最后这块已经是实时数据，交给调用方正常写
		q.exitCatchUp(reason)
		return n, false, err
	}
	q.skippedBytes += int64(n)
	q.catchSkips += int64(n)
	return n, true, err
}

// catchUpDone 判断能不能退出追赶，返回非空字符串即退出原因。
//
// 退出只认"欠账还清"这一个硬条件：卡了多久就烧够多久。不能拿"输入速率降到
// 实时"当退出条件——卡顿结束后上位机正在补发，速率会长时间高于实时，那个条件
// 迟迟不成立，追赶就会被一路拖长，把好音频也烧掉(实测卡 86ms 烧到 508ms)。
func (q *pcmQueue) catchUpDone(gap time.Duration) string {
	now := time.Now()
	if now.Sub(q.catchStart) >= pcmCatchLimit {
		return "超过单次上限"
	}
	// 内核缓冲里能看到比欠账更多的积压，说明链路上真的还挂着一坨，抬上去继续烧
	if q.socketBytes > q.catchDebt {
		q.catchDebt = q.catchSkips + q.socketBytes
	}
	if q.catchSkips < q.catchDebt {
		return ""
	}
	// 欠账还清了，而且读又阻塞到"事故级"那么久，才说明链路里确实没有旧音频可烧。
	// 门槛必须用 pcmStarveGap(80ms) 而不是更小的值：WiFi 恢复后数据是一阵阵
	// 涌进来的，两次 burst 之间出现 40ms 空档很常见，按 40ms 判断会提前退出，
	// 把没烧完的积压留在本机——那部分会以实时速率排出来，就是永久延迟。
	if gap >= pcmStarveGap {
		return "读阻塞已恢复"
	}
	// 内核缓冲里还挂着明显积压(至少 4 个读块)，继续追
	if q.socketBytes >= msBytes(q.rate, q.frameBytes, 4*pcmReadChunkMs) {
		q.catchDebt = q.catchSkips + q.socketBytes
		return ""
	}
	// 欠账还清但数据还在以超过实时的速度灌进来，说明链路上还有积压没排完。
	// 这条是 socket 量不到时唯一的"还没排空"信号，不能省：卡 86ms 却烧了
	// 508ms 不是丢多了，是上位机内核发送缓冲里真的压着 500 多毫秒旧音频。
	elapsed := now.Sub(q.catchWindowStart)
	if elapsed >= pcmCatchWindow {
		live := float64(q.rate) * float64(q.frameBytes)
		if live <= 0 || float64(q.catchWindowBytes)/elapsed.Seconds() <= live*pcmCatchRate {
			return "输入速率降到实时"
		}
		q.catchWindowStart = now
		q.catchWindowBytes = 0
	}
	return ""
}

func (q *pcmQueue) exitCatchUp(reason string) {
	if !q.catching {
		return
	}
	q.catching = false
	now := time.Now()
	q.catchUntil = now.Add(pcmCatchCooldown)
	log.Printf("追赶结束: 卡顿 %dms，烧掉 %dms，耗时 %dms(%s)，本机排队 %dms(累计已丢 %dms)",
		q.catchTriggerGap.Milliseconds(), q.ms(q.catchSkips),
		now.Sub(q.catchStart).Milliseconds(), reason,
		q.ms(q.queuedBytes()), q.ms(q.skippedBytes))
	q.catchSkips = 0
}

// fix 结算一次：排队超过目标就从流里跳读丢掉多余的旧音频。
// socketBytes 是内核接收缓冲里还没读走的字节，sinkBytes 是声卡侧当前排队的字节，
// socketBytes < 0 表示这个平台查不到接收缓冲，当 0 算。
func (q *pcmQueue) fix(r io.Reader, socketBytes, sinkBytes int64) error {
	// 追赶态由主循环的连续丢弃完成，这里不再动手，免得两处各丢一半
	if q.catching {
		return nil
	}
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
		log.Printf("跳读: 排队 %dms 目标 %dms(声卡 %dms)，丢 %dms(累计 %dms)",
			q.ms(queued), q.ms(target), q.ms(q.clampSink(sinkBytes)),
			q.ms(dropped), q.ms(q.skippedBytes))
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



// ms 把字节数换算成毫秒，只用来打日志和上报。
func (q *pcmQueue) ms(bytes int64) int64 {
	if q.rate <= 0 || q.frameBytes <= 0 {
		return 0
	}
	return bytes / int64(q.frameBytes) * 1000 / int64(q.rate)
}

// PCMStreamSnapshot 是 /aplay 这条链路的实时指标快照。
type PCMStreamSnapshot struct {
	Active     bool   `json:"active"`
	Backend    string `json:"backend"`
	Rate       int    `json:"rate"`
	Channels   int    `json:"channels"`
	LocalLagMs int64  `json:"localLagMs"`
	TargetMs   int64  `json:"targetMs"`
	Catching   bool   `json:"catching"`
	Stalls     int64  `json:"stalls"`
	CatchUps   int64  `json:"catchUps"`
	SkippedMs  int64  `json:"skippedMs"`
	LastGapMs  int64  `json:"lastGapMs"`
	XRuns      int64  `json:"xruns"`
	// SocketKnown 为 false 表示读不到内核接收缓冲，此时追赶只按卡顿时长结算。
	SocketKnown bool `json:"socketKnown"`
}

var (
	pcmStatsMu sync.RWMutex
	pcmStats   PCMStreamSnapshot
)

// PCMStreamStats 返回当前实时流的指标快照；没有流在播时 active=false。
// 只有播放协程会写，HTTP 那边读的是拷贝，不会挡播放。
func PCMStreamStats() PCMStreamSnapshot {
	pcmStatsMu.RLock()
	defer pcmStatsMu.RUnlock()
	return pcmStats
}

func (q *pcmQueue) publish(active bool) {
	lag := q.queuedBytes()
	q.publishValue(PCMStreamSnapshot{
		Active:     active,
		Backend:    q.backend,
		Rate:       q.rate,
		Channels:   q.frameBytes / 2,
		LocalLagMs: q.ms(lag),
		TargetMs:   q.ms(q.target),
		Catching:   q.catching,
		Stalls:     q.stalls,
		CatchUps:   q.catchUps,
		SkippedMs:   q.ms(q.skippedBytes),
		LastGapMs:   q.lastGap.Milliseconds(),
		XRuns:       q.xruns,
		SocketKnown: q.socketBytes >= 0,
	})
}

func (q *pcmQueue) publishValue(value PCMStreamSnapshot) {
	pcmStatsMu.Lock()
	pcmStats = value
	pcmStatsMu.Unlock()
}

// clearPCMStats 在流结束时把 active 标掉，指标保留最后一次的值便于事后查看。
func (q *pcmQueue) clearPCMStats() {
	q.catching = false
	snapshot := PCMStreamStats()
	snapshot.Active = false
	snapshot.Catching = false
	q.publishValue(snapshot)
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
		size, err := strconv.Atoi(value)
		if err != nil || size < 0 {
			return
		}
		// 这个旋钮只是诊断用的。调大之后卡顿时内核会替我们缓存几百毫秒旧音频，
		// 内存和延迟一起涨，小机器会直接被吃光，所以夹一个硬上限。
		if size > pcmRcvbufCap {
			log.Printf("aplay: APLAY_RCVBUF=%d 太大，已夹到 %d(调大只会让卡顿时内存和延迟一起涨)", size, pcmRcvbufCap)
			size = pcmRcvbufCap
		}
		rcvbufSize = size
	})
	return rcvbufSize
}
