//go:build linux && cgo && alsa && !app

package xiaozhi

// 厂商四麦前端的客户端库是运行时 dlopen 进来的，这里只做最薄的一层：
// 打开 so、取符号、把 Go 的回调传进去。
//
// C 实现只能写在这个文件的注释前缀里（包里不能再放 .c，否则非 cgo 构建会报错）。
// 回调 wxGoMicData 由 rns_export.go 导出，这里声明一下就能用。

/*
#include <dlfcn.h>
#include <stddef.h>

// 由 rns_export.go 的 //export 生成
extern void wxGoMicData(short *, int, int, int, void *);

// 与库里的原型一致：
//   void four_mic_front_end_alg_client_init_and_set_user_data(void*, void (*)(short*, int, int, int, void*))
typedef void (*wx_mic_cb)(short *, int, int, int, void *);
typedef void (*wx_init_fn)(void *, wx_mic_cb);
typedef void (*wx_int_fn)(int);

static void *g_handle;
static wx_init_fn g_init;
static wx_int_fn g_set_angle;
static wx_int_fn g_set_wakeup;

// wx_rns_open 打开客户端库并取符号，返回 0 表示成功。
static int wx_rns_open(const char *path) {
	if (g_handle != NULL) {
		return 0;
	}
	void *h = dlopen(path, RTLD_NOW | RTLD_GLOBAL);
	if (h == NULL) {
		return 1;
	}
	g_init = (wx_init_fn)dlsym(h, "four_mic_front_end_alg_client_init_and_set_user_data");
	g_set_angle = (wx_int_fn)dlsym(h, "four_mic_front_end_alg_set_lock_angle");
	g_set_wakeup = (wx_int_fn)dlsym(h, "four_mic_front_end_alg_set_wake_up");
	if (g_init == NULL) {
		dlclose(h);
		return 2;
	}
	g_handle = h;
	return 0;
}

// wx_rns_start 注册回调并连上四麦前端服务。
static void wx_rns_start(void *user) {
	if (g_init != NULL) {
		g_init(user, (wx_mic_cb)wxGoMicData);
	}
}

// wx_rns_set_angle 锁定拾音角度（0 表示不锁）。
static void wx_rns_set_angle(int angle) {
	if (g_set_angle != NULL) {
		g_set_angle(angle);
	}
}

// wx_rns_set_wakeup 告诉前端现在要不要做唤醒。
static void wx_rns_set_wakeup(int on) {
	if (g_set_wakeup != NULL) {
		g_set_wakeup(on);
	}
}
*/
import "C"

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"unsafe"

	"workdayAlarmClock/xiaozhi/core"
)

const rnsClientLib = "/usr/lib/libfour_mic_front_end_alg_client.so"

// rnsCapture 厂商四麦前端的采集接入。
//
// 前端已经把波束形成、降噪、AEC 都做完了，回调里拿到的是干净的 16k 单声道，
// 这里只负责攒成 60ms 一帧交出去。
var rnsCapture struct {
	mu       sync.Mutex
	opened   bool
	started  bool
	consumer func([]int16)
	pending  []int16
	log      func(string)
	// logCount 只打前几次回调，方便确认参数语义
	logCount int32
	// lastA/lastB 最近一次回调带的两个整数，唤醒命中时打出来对照
	lastA atomic.Int32
	lastB atomic.Int32
}

// lastCallbackInts 取最近一次音频回调的两个整数。
func lastCallbackInts() (int, int) {
	return int(rnsCapture.lastA.Load()), int(rnsCapture.lastB.Load())
}

// rnsAvailable 这台机器能不能用厂商前端采集。
func rnsAvailable() bool {
	if _, err := os.Stat(rnsClientLib); err != nil {
		return false
	}
	return rnsWakeAvailable()
}

// startRNSCapture 打开前端并把音频交给 onFrame。
func startRNSCapture(onFrame func([]int16), logf func(string)) error {
	rnsCapture.mu.Lock()
	defer rnsCapture.mu.Unlock()

	rnsCapture.log = logf
	if err := ensureFrontEndServer(logf); err != nil {
		return err
	}

	if !rnsCapture.opened {
		path := C.CString(rnsClientLib)
		rc := C.wx_rns_open(path)
		C.free(unsafe.Pointer(path))
		if rc != 0 {
			return fmt.Errorf("打开四麦客户端库失败（%d）", int(rc))
		}
		rnsCapture.opened = true
	}

	rnsCapture.consumer = onFrame
	rnsCapture.pending = rnsCapture.pending[:0]
	if !rnsCapture.started {
		C.wx_rns_start(nil)
		rnsCapture.started = true
		if logf != nil {
			logf("已接入四麦前端：波束/降噪/AEC 由它处理")
		}
	}
	return nil
}

// stopRNSCapture 停止把音频交出去。库本身没有反初始化接口，只能停消费。
func stopRNSCapture() {
	rnsCapture.mu.Lock()
	defer rnsCapture.mu.Unlock()
	rnsCapture.consumer = nil
	rnsCapture.pending = rnsCapture.pending[:0]
}

// handleRNSPCM 处理一次前端回调：拷出数据、攒帧、交上层。
func handleRNSPCM(data *C.short, samples, a, b int) {
	if data == nil || samples <= 0 || samples > 1024 {
		return
	}
	rnsCapture.lastA.Store(int32(a))
	rnsCapture.lastB.Store(int32(b))

	// data 指向库的内部缓冲，回调返回就失效，必须马上拷出来
	src := unsafe.Slice((*int16)(unsafe.Pointer(data)), samples)

	rnsCapture.mu.Lock()
	defer rnsCapture.mu.Unlock()

	if atomic.AddInt32(&rnsCapture.logCount, 1) <= 5 && rnsCapture.log != nil {
		rnsCapture.log(fmt.Sprintf("前端回调 samples=%d a=%d b=%d", samples, a, b))
	}

	consumer := rnsCapture.consumer
	if consumer == nil {
		return
	}
	rnsCapture.pending = append(rnsCapture.pending, src...)
	for len(rnsCapture.pending) >= core.FrameSamples {
		frame := make([]int16, core.FrameSamples)
		copy(frame, rnsCapture.pending[:core.FrameSamples])
		rnsCapture.pending = rnsCapture.pending[core.FrameSamples:]
		consumer(frame)
	}
}
