//go:build linux && cgo && alsa && !app

package xiaozhi

// 这个文件只负责把 Go 函数导出成 C 符号给厂商库回调。
// 带 //export 的文件，注释前缀里只能有声明——所以 C 实现放在 rns_bridge.go。

/*
#include <stdint.h>
*/
import "C"

import "unsafe"

//export wxGoMicData
func wxGoMicData(data *C.short, samples C.int, a C.int, b C.int, user unsafe.Pointer) {
	handleRNSPCM(data, int(samples), int(a), int(b))
}
