//go:build linux && cgo && alsa && !app

package xiaozhi

/*
#include <stdint.h>

int wx_rns_open(const char *path);
void wx_rns_start(void *user);
void wx_rns_set_angle(int angle);
void wx_rns_set_wakeup(int on);
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
	// logCount 只打前几次回调，方便对参数语义留个现场
	logCount int32
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

// wxGoMicData 是前端线程回调进来的入口。
// 参数按库里的实现：data / samples（样本数，不是字节）/ 两个整数（角度之类）/ user。
//
//export wxGoMicData
func wxGoMicData(data *C.short, samples C.int, a C.int, b C.int, user unsafe.Pointer) {
	n := int(samples)
	if data == nil || n <= 0 || n > 1024 {
		return
	}
	// data 指向库的内部缓冲，回调返回就失效，必须马上拷出来
	src := unsafe.Slice((*int16)(unsafe.Pointer(data)), n)

	rnsCapture.mu.Lock()
	defer rnsCapture.mu.Unlock()

	if atomic.AddInt32(&rnsCapture.logCount, 1) <= 5 && rnsCapture.log != nil {
		rnsCapture.log(fmt.Sprintf("前端回调 samples=%d a=%d b=%d", n, int(a), int(b)))
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
		// 回调线程里直接交给上层；上层只做编码和发送，不会再进这把锁
		consumer(frame)
	}
}
