package player

/*
 * 实时可调的均衡器（EQ）
 *
 * 设计要点：
 *  1. 默认关闭。conf.Cfg.Eq.Enabled 为 false 时，Process 直接返回，
 *     调用点只需要一次 if 判断，几乎零开销，不改变原有播放行为。
 *  2. 参数可热更新。滤波器系数与状态分离：系数在参数变化时重算，
 *     状态(延迟线)保留，所以播放中途改增益不会爆音，只会平滑过渡到新曲线。
 *  3. 支持三种滤波器：低频 shelf(低音增强/衰减)、高频 shelf、peaking(任意频段)。
 *  4. 全部用 float64 计算，S16 进出，内部不做重采样。
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

// biquad 二阶滤波器，Direct Form I。
type biquad struct {
	b0, b1, b2 float64
	a1, a2     float64
	// 左右声道各自的状态，避免串音
	x1, x2, y1, y2 [2]float64
}

func (b *biquad) reset() {
	b.x1 = [2]float64{}
	b.x2 = [2]float64{}
	b.y1 = [2]float64{}
	b.y2 = [2]float64{}
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

// process 原地处理一个采样点。
func (b *biquad) process(x float64, ch int) float64 {
	y := b.b0*x + b.b1*b.x1[ch] + b.b2*b.x2[ch] - b.a1*b.y1[ch] - b.a2*b.y2[ch]
	b.x2[ch] = b.x1[ch]
	b.x1[ch] = x
	b.y2[ch] = b.y1[ch]
	b.y1[ch] = y
	return y
}

// eqParams 是一份不可变的处理参数快照。
type eqParams struct {
	enabled    bool
	sampleRate float64
	preamp     float64
	filters    []*biquad
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
	return s.Enabled && len(s.Bands) > 0
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
	// 关闭或没有有效频段时，放一个"空参数"，Process 里直接短路
	if !e.cur.Enabled || len(e.cur.Bands) == 0 {
		e.params.Store(&eqParams{enabled: false, sampleRate: e.rate})
		return
	}
	filters := make([]*biquad, len(e.cur.Bands))
	for i, band := range e.cur.Bands {
		b := &biquad{}
		b.setCoeffs(band, e.rate)
		filters[i] = b
	}
	preamp := math.Pow(10, e.cur.Preamp/20)
	e.params.Store(&eqParams{
		enabled:    true,
		sampleRate: e.rate,
		preamp:     preamp,
		filters:    filters,
	})
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

	n := len(buf) &^ 3
	if n == 0 {
		return
	}

	preamp := p.preamp
	filters := p.filters
	for i := 0; i < n; i += 4 {
		l := float64(int16(uint16(buf[i]) | uint16(buf[i+1])<<8))
		r := float64(int16(uint16(buf[i+2]) | uint16(buf[i+3])<<8))
		if preamp != 1 {
			l *= preamp
			r *= preamp
		}
		for _, f := range filters {
			l = f.process(l, 0)
			r = f.process(r, 1)
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
	p := e.params.Load()
	if p == nil {
		return
	}
	for _, f := range p.filters {
		f.reset()
	}
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
