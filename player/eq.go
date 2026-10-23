package player

/*
 * 实时可调的均衡器（EQ）
 *
 * 设计要点：
 *  1. 默认关闭。conf.Cfg.Eq.Enabled 为 false 时，Process 直接返回，
 *     调用点只需要一次 if 判断，几乎零开销，不改变原有播放行为。
 *  2. 参数可热更新。系数与状态彻底分离：eqParams 快照里只有系数(不可变)，
 *     延迟线挂在 EQ 上由音频线程独占，所以播放中途改增益/频率不会清零状态、不会爆音。
 *  3. 支持三种滤波器：低频 shelf(低音增强/衰减)、高频 shelf、peaking(任意频段)，
 *     另外内部还有低通/高通，给虚拟低音用。
 *  4. 全部用 float64 计算，S16 进出，内部不做重采样。
 *  5. 虚拟低音用带限谐波激励(missing fundamental)：只对低频做全波整流产生
 *     谐波再混回去，不需要 FFT 也不需要过采样，开销约等于多挂 1/3 个 EQ 段。
 *
 * 采样率必须在打开音频流时告诉 EQ：SetSampleRate。
 * 不同采样率的歌曲切换时会自动重算系数。
 */

import (
	"math"
	"sync"
	"sync/atomic"

	"workdayAlarmClock/conf"
)

// 复用 conf 里的类型，配置文件和运行时处理保持同一份定义。
type EQBand = conf.EQBand
type EQSettings = conf.EQSettings

// DefaultEQ 默认配置：关闭状态，给一套常见的低音增强参数。
func DefaultEQ() EQSettings {
	return conf.DefaultEQ()
}

// biquad 二阶滤波器系数，Direct Form I。
// 这里只放系数(不可变快照)，延迟线在 biquadState 里，由音频线程独占，
// 这样热更新参数时重算系数不会把正在用的状态清掉。
type biquad struct {
	b0, b1, b2 float64
	a1, a2     float64
}

// biquadState 左右声道各自的延迟线，避免串音。
type biquadState struct {
	x1, x2, y1, y2 [2]float64
}

// setCoeffs 用 RBJ Audio EQ Cookbook 公式计算系数。
func (b *biquad) setCoeffs(band EQBand, sampleRate float64) {
	if sampleRate <= 0 {
		b.setBypass()
		return
	}
	// 频率夹在奈奎斯特频率之下，避免系数发散
	freq := band.Freq
	if freq <= 0 || freq >= sampleRate/2 {
		b.setBypass()
		return
	}
	q := band.Q
	if q <= 0 {
		q = 0.707
	}
	w0 := 2 * math.Pi * freq / sampleRate
	cosW0 := math.Cos(w0)
	sinW0 := math.Sin(w0)
	A := math.Pow(10, band.Gain/40)

	var b0, b1, b2, a0, a1, a2 float64
	switch band.Type {
	case "lowshelf":
		alpha := sinW0 / 2 * math.Sqrt((A+1/A)*(1/q-1)+2)
		sq := 2 * math.Sqrt(A) * alpha
		b0 = A * ((A + 1) - (A-1)*cosW0 + sq)
		b1 = 2 * A * ((A - 1) - (A+1)*cosW0)
		b2 = A * ((A + 1) - (A-1)*cosW0 - sq)
		a0 = (A + 1) + (A-1)*cosW0 + sq
		a1 = -2 * ((A - 1) + (A+1)*cosW0)
		a2 = (A + 1) + (A-1)*cosW0 - sq
	case "highshelf":
		alpha := sinW0 / 2 * math.Sqrt((A+1/A)*(1/q-1)+2)
		sq := 2 * math.Sqrt(A) * alpha
		b0 = A * ((A + 1) + (A-1)*cosW0 + sq)
		b1 = -2 * A * ((A - 1) + (A+1)*cosW0)
		b2 = A * ((A + 1) + (A-1)*cosW0 - sq)
		a0 = (A + 1) - (A-1)*cosW0 + sq
		a1 = 2 * ((A - 1) - (A+1)*cosW0)
		a2 = (A + 1) - (A-1)*cosW0 - sq
	case "peaking":
		alpha := sinW0 / (2 * q)
		b0 = 1 + alpha*A
		b1 = -2 * cosW0
		b2 = 1 - alpha*A
		a0 = 1 + alpha/A
		a1 = -2 * cosW0
		a2 = 1 - alpha/A
	case "lowpass":
		alpha := sinW0 / (2 * q)
		b0 = (1 - cosW0) / 2
		b1 = 1 - cosW0
		b2 = b0
		a0 = 1 + alpha
		a1 = -2 * cosW0
		a2 = 1 - alpha
	case "highpass":
		alpha := sinW0 / (2 * q)
		b0 = (1 + cosW0) / 2
		b1 = -(1 + cosW0)
		b2 = b0
		a0 = 1 + alpha
		a1 = -2 * cosW0
		a2 = 1 - alpha
	default:
		b.setBypass()
		return
	}
	if a0 == 0 || math.IsNaN(a0) {
		b.setBypass()
		return
	}
	b.b0 = b0 / a0
	b.b1 = b1 / a0
	b.b2 = b2 / a0
	b.a1 = a1 / a0
	b.a2 = a2 / a0
}

// setBypass 直通：y = x
func (b *biquad) setBypass() {
	b.b0, b.b1, b.b2 = 1, 0, 0
	b.a1, b.a2 = 0, 0
}

// process 原地处理一个采样点，状态由调用方传入。
func (b *biquad) process(s *biquadState, x float64, ch int) float64 {
	y := b.b0*x + b.b1*s.x1[ch] + b.b2*s.x2[ch] - b.a1*s.y1[ch] - b.a2*s.y2[ch]
	s.x2[ch] = s.x1[ch]
	s.x1[ch] = x
	s.y2[ch] = s.y1[ch]
	s.y1[ch] = y
	return y
}

// virtualBass 谐波激励式虚拟低音(missing fundamental)。
// 低频路径按单声道处理：左右低频在录音里高度相关，合并后只跑一次滤波，
// 生成的谐波等量混回两个声道，滤波开销直接减半。
type virtualBass struct {
	lp, hp biquad
	gain   float64
}

// virtualBassState 虚拟低音的延迟线，只由音频线程访问。
type virtualBassState struct {
	lp, hp biquadState
}

// setCoeffs 按低频上限 cutoff 生成系数。
// 低通留下要"虚拟"的低频；高通只负责清掉整流产生的直流和差频，
// 2f 谐波本身不在带内，所以不能切太高。
func (v *virtualBass) setCoeffs(cutoff, sampleRate float64) {
	v.lp.setCoeffs(EQBand{Type: "lowpass", Freq: cutoff, Q: 0.707}, sampleRate)
	hp := cutoff * 0.5
	if hp < 30 {
		hp = 30
	}
	v.hp.setCoeffs(EQBand{Type: "highpass", Freq: hp, Q: 0.707}, sampleRate)
}

// process 出入都是 S16 量纲的左右声道。
// 全波整流 |x| 产生 2f/4f… 谐波；带限之后高次谐波衰减很快，
// 24kHz 以上已经低到可忽略，所以不需要过采样来压混叠。
func (v *virtualBass) process(s *virtualBassState, l, r float64) (float64, float64) {
	m := v.lp.process(&s.lp, (l+r)*0.5, 0)
	h := v.hp.process(&s.hp, math.Abs(m), 0) * v.gain
	return l + h, r + h
}

// eqFilter 一条挂进链路的频段：系数 + 它对应的频段下标。
// 记下标是为了让延迟线按频段固定存放，中间某个频段从 0dB 变成非 0dB 时
// 不会把后面频段的状态错位。
type eqFilter struct {
	coeffs biquad
	band   int
}

// eqParams 是一份不可变的处理参数快照，只含系数。
type eqParams struct {
	enabled    bool
	sampleRate float64
	preamp     float64
	filters    []eqFilter
	bass       *virtualBass
	bandCount  int
	// gen 每次重建自增，Process 用它判断状态数组要不要跟着换长度
	gen uint64
}

// EQ 全局均衡器。零值不可用，必须用 GetEQ。
type EQ struct {
	mu     sync.Mutex
	params atomic.Pointer[eqParams]
	// 当前已应用的设置，用于比较是否需要重算
	cur EQSettings
	// 当前采样率
	rate float64
	// 统计：累计处理帧数，方便调试
	processed atomic.Uint64

	// 下面几个字段是音频线程独占的滤波状态，不放进 eqParams 快照：
	// 参数热更新时只换系数，延迟线原样保留，才不会咔哒。
	state     []biquadState
	bassState virtualBassState
	curGen    uint64
	nextGen   uint64 // 仅 e.mu 保护
}

var (
	eqOnce sync.Once
	eqInst *EQ
)

// GetEQ 返回全局 EQ 实例。
func GetEQ() *EQ {
	eqOnce.Do(func() {
		eqInst = &EQ{rate: 48000}
		eqInst.params.Store(&eqParams{enabled: false, sampleRate: 48000})
	})
	return eqInst
}

// Apply 应用一份新的设置。可在任意时刻调用（包括播放中）。
// 返回是否处于启用状态。
func (e *EQ) Apply(s EQSettings) bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cur = s
	e.rebuildLocked()
	return s.Enabled
}

// SetSampleRate 告诉 EQ 当前流的采样率。
// 采样率变化时会重算系数，但保留滤波器状态（避免切歌爆音）。
func (e *EQ) SetSampleRate(rate int) {
	if e == nil || rate <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	r := float64(rate)
	if e.rate == r {
		return
	}
	e.rate = r
	e.rebuildLocked()
}

// Settings 返回当前设置（副本）。
func (e *EQ) Settings() EQSettings {
	if e == nil {
		return DefaultEQ()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.cur
	out.Bands = append([]EQBand(nil), e.cur.Bands...)
	return out
}

// Enabled 当前是否真正在做处理。
func (e *EQ) Enabled() bool {
	if e == nil {
		return false
	}
	p := e.params.Load()
	return p != nil && p.enabled
}

// rebuildLocked 重算系数并原子替换参数快照。调用方必须持有 e.mu。
func (e *EQ) rebuildLocked() {
	// 关闭时放一个"空参数"，Process 里直接短路
	if !e.cur.Enabled {
		e.storeDisabled()
		return
	}
	// 0dB 的频段数学上就是直通，系数算了也是白算，直接不挂进链路。
	// 上游只送 5ms 一片，每片都要过一遍滤波器，省掉空转的段很划算。
	filters := make([]eqFilter, 0, len(e.cur.Bands))
	for bi, band := range e.cur.Bands {
		if !bandActive(band) {
			continue
		}
		var b biquad
		b.setCoeffs(band, e.rate)
		filters = append(filters, eqFilter{coeffs: b, band: bi})
	}
	// 虚拟低音：强度 0 时完全不建这条链路，Process 里连 if 都省了
	var bass *virtualBass
	if amount := e.cur.VirtualBass / 100; amount > 0 {
		if amount > 1 {
			amount = 1
		}
		freq := e.cur.VirtualBassFreq
		if freq <= 0 {
			freq = conf.VBassDefaultFreq
		}
		bass = &virtualBass{gain: amount}
		bass.setCoeffs(freq, e.rate)
	}
	// 全部频段都是 0dB、预增益 0dB、又没开虚拟低音：整条链路等价于直通
	preamp := math.Pow(10, e.cur.Preamp/20)
	if len(filters) == 0 && preamp == 1 && bass == nil {
		e.storeDisabled()
		return
	}
	e.nextGen++
	e.params.Store(&eqParams{
		enabled:    true,
		sampleRate: e.rate,
		preamp:     preamp,
		filters:    filters,
		bass:       bass,
		bandCount:  len(e.cur.Bands),
		gen:        e.nextGen,
	})
}

func (e *EQ) storeDisabled() {
	e.nextGen++
	e.params.Store(&eqParams{sampleRate: e.rate, gen: e.nextGen})
}

// syncState 在参数快照换代时把状态数组调整到新长度。
// 状态按下标(即频段位置)保存，所以拖动滑块改增益/频率不会把声音打断。
func (e *EQ) syncState(p *eqParams) {
	if n := p.bandCount; n != len(e.state) {
		st := make([]biquadState, n)
		copy(st, e.state)
		e.state = st
	}
	e.curGen = p.gen
}

// bandActive 判断这个频段是否真的会改变声音。
// 增益为 0dB 时无论频率和 Q 是多少，滤波器都是直通，可以直接跳过。
func bandActive(band EQBand) bool {
	if band.Gain == 0 {
		return false
	}
	// 频率非法时 setCoeffs 会退化成直通，同样没必要挂上去
	if band.Freq <= 0 {
		return false
	}
	return true
}

// Process 原地处理交错 S16_LE 立体声数据。
// n 必须是 4 的倍数（一个立体声帧 4 字节）。不足一帧的尾巴原样保留，
// 由调用方累积到下一块再处理。
//
// 未启用时立即返回，开销就是一次原子读 + 一次比较。
func (e *EQ) Process(buf []byte) {
	if e == nil || len(buf) < 4 {
		return
	}
	p := e.params.Load()
	if p == nil || !p.enabled {
		return
	}
	// 参数换代了才动状态数组，平时这里就是一次整数比较
	if p.gen != e.curGen {
		e.syncState(p)
	}

	n := len(buf) &^ 3
	if n == 0 {
		return
	}

	preamp := p.preamp
	bass := p.bass
	filters := p.filters
	state := e.state
	bassState := &e.bassState
	for i := 0; i < n; i += 4 {
		l := float64(int16(uint16(buf[i]) | uint16(buf[i+1])<<8))
		r := float64(int16(uint16(buf[i+2]) | uint16(buf[i+3])<<8))
		if preamp != 1 {
			l *= preamp
			r *= preamp
		}
		if bass != nil {
			l, r = bass.process(bassState, l, r)
		}
		for fi := range filters {
			f := &filters[fi]
			l = f.coeffs.process(&state[f.band], l, 0)
			r = f.coeffs.process(&state[f.band], r, 1)
		}
		dst := buf[i : i+4]
		putS16LE(dst, l, r)
	}

	e.processed.Add(uint64(n / 4))
}

// putS16LE 写入一个立体声帧，带饱和截断防止削波溢出。
func putS16LE(dst []byte, l, r float64) {
	li := clampS16(l)
	ri := clampS16(r)
	dst[0] = byte(li)
	dst[1] = byte(uint16(li) >> 8)
	dst[2] = byte(ri)
	dst[3] = byte(uint16(ri) >> 8)
}

// clampS16 四舍五入并夹到 int16 范围。
func clampS16(v float64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(math.Round(v))
}

// ProcessEQ 是给调用点用的便捷函数：处理一块 S16_LE 立体声数据。
// 未启用时是空操作。
func ProcessEQ(buf []byte) {
	GetEQ().Process(buf)
}

// ResetEQState 清空滤波器历史状态，切歌/断流时调用避免拖尾。
func ResetEQState() {
	e := GetEQ()
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.state)
	e.bassState = virtualBassState{}
}

// EQActive 是否有任何处理需要做。
func EQActive() bool {
	p := GetEQ().params.Load()
	return p != nil && p.enabled
}

// ApplyEQConfig 把配置里的 EQ 设置同步到运行时。
// 启动时调用一次，之后每次通过接口改参数也调用它。
func ApplyEQConfig(s EQSettings) {
	GetEQ().Apply(s)
}

// SyncEQFromConf 从全局配置读取 EQ 设置并应用。
func SyncEQFromConf() {
	ApplyEQConfig(conf.Cfg.Eq)
}
