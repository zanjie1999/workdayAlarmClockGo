package core

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// Frame 一帧收到的数据。Text 与 Binary 分开，是因为协议里 JSON 走文本、Opus 走二进制，
// 两者要交给完全不同的处理路径。
type Frame struct {
	// Binary true 表示二进制帧（Opus 音频），false 表示文本帧（JSON）
	Binary bool
	// Data 帧内容，已是独立副本，可以放心持有
	Data []byte
}

// WSTransport x/net/websocket 的薄封装。
//
// 只做三件事：带上自定义头拨号、区分文本/二进制收帧、给发送加锁。
// 断线重连、超时策略都留给 session，这里保持哑管道。
type WSTransport struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	closed  bool
	closeMu sync.Mutex
}

// frameCodec 自定义编解码：既要拿到 payload 类型，又不能像 websocket.Message 那样丢掉它。
var frameCodec = websocket.Codec{
	Marshal: func(v any) ([]byte, byte, error) {
		switch data := v.(type) {
		case string:
			return []byte(data), websocket.TextFrame, nil
		case []byte:
			return data, websocket.BinaryFrame, nil
		}
		return nil, websocket.UnknownFrame, websocket.ErrNotSupported
	},
	Unmarshal: func(data []byte, payloadType byte, v any) error {
		frame, ok := v.(*Frame)
		if !ok {
			return websocket.ErrNotSupported
		}
		frame.Binary = payloadType == websocket.BinaryFrame
		// ReadAll 已经是新分配的切片，这里直接接管
		frame.Data = data
		return nil
	},
}

// DialWS 建立连接。header 里放 Authorization / Device-Id 等业务头。
func DialWS(ctx context.Context, rawURL string, header http.Header) (*WSTransport, error) {
	location, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, err
	}
	if location.Scheme != "ws" && location.Scheme != "wss" {
		return nil, errors.New("地址不是 ws/wss：" + rawURL)
	}

	// 服务端不校验 Origin，但 x/net 要求必须给一个，按同源填
	originScheme := "http"
	if location.Scheme == "wss" {
		originScheme = "https"
	}
	origin := &url.URL{Scheme: originScheme, Host: location.Host}

	cfg, err := websocket.NewConfig(location.String(), origin.String())
	if err != nil {
		return nil, err
	}
	cfg.Header = header

	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if deadline, ok := ctx.Deadline(); ok {
		dialer.Deadline = deadline
	}
	cfg.Dialer = dialer

	conn, err := websocket.DialConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &WSTransport{conn: conn}, nil
}

// SendText 发一个文本帧（JSON）。
func (t *WSTransport) SendText(data []byte) error {
	return t.send(string(data))
}

// SendBinary 发一个二进制帧（Opus 包）。
func (t *WSTransport) SendBinary(data []byte) error {
	return t.send(data)
}

func (t *WSTransport) send(v any) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if t.isClosed() {
		return errors.New("连接已关闭")
	}
	// 写不能无限期挂着，否则对端不收数据时整个会话线程会被拖死
	_ = t.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err := frameCodec.Send(t.conn, v)
	if err != nil {
		// x/net 没有超时语义，超时表现为错误，这里翻译成人话
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return errors.New("发送超时")
		}
	}
	return err
}

// Receive 收一帧。阻塞直到有数据、连接关闭或出错，调用方应放在独立 goroutine 里。
func (t *WSTransport) Receive() (Frame, error) {
	var frame Frame
	if err := frameCodec.Receive(t.conn, &frame); err != nil {
		return Frame{}, err
	}
	return frame, nil
}

// Close 关闭连接，可重复调用。
func (t *WSTransport) Close() error {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return nil
	}
	t.closed = true
	t.closeMu.Unlock()
	return t.conn.Close()
}

func (t *WSTransport) isClosed() bool {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	return t.closed
}
