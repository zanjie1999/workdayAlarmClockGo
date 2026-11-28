//go:build !linux || !cgo || !alsa || app

package xiaozhi

import "context"

// 这个文件覆盖所有用不了小智的形态：Windows / macOS / Android App / 没有 cgo 的构建。
// 界面照常能显示设置项，但点开始会直接给出"不可用"，不会静默失败。

// Available 当前编译形态下功能是否可用。
func Available() bool { return false }

// Running 永远不在运行。
func Running() bool { return false }

// Current 返回一个空状态，页面照常能渲染。
func Current() Status { return Status{State: "idle"} }

// Start 直接返回不可用。
func Start(Options) error { return ErrUnavailable }

// Stop 无事可做。
func Stop() {}

// Trigger 不可用。
func Trigger() bool { return false }

// CheckActivation 直接返回不可用。
func CheckActivation(context.Context, Options) (ActivationResult, error) {
	return ActivationResult{}, ErrUnavailable
}
