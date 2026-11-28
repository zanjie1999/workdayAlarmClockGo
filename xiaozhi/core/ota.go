package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OtaClient 官方 OTA 接口客户端。
//
// 两条路：已经绑定的设备直接拿到 websocket 地址和 token；
// 没绑定的设备拿到一个 6 位配对码，用户去 xiaozhi.me 输入，期间要轮询 /activate。
type OtaClient struct {
	// OtaURL OTA 地址，末尾斜杠可有可无
	OtaURL string
	// DeviceMac 设备身份（Device-Id 头 + 请求体 mac_address）
	DeviceMac string
	// ClientID 客户端身份（Client-Id 头 + 请求体 uuid）
	ClientID string
	// AppVersion 上报给服务端的版本号
	AppVersion string
	// BoardName 板子名，上报在 board.name
	BoardName string
	// HTTP 可注入的客户端，nil 用默认（10s 连接 / 15s 读超时）
	HTTP *http.Client
	// Log 日志回调，可为 nil
	Log func(string)
}

func (c *OtaClient) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(fmt.Sprintf(format, args...))
	}
}

func (c *OtaClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			MaxIdleConns:          4,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
		},
	}
}

// Body 构造这次 OTA 的请求体，轮询激活时要原样重发同一份。
func (c *OtaClient) Body() []byte {
	version := c.AppVersion
	if version == "" {
		version = "1.0"
	}
	board := c.BoardName
	if board == "" {
		board = "generic"
	}
	return OtaRequestBody(c.DeviceMac, c.ClientID, version, board)
}

// post 发一次 POST，返回状态码和响应体。
func (c *OtaClient) post(ctx context.Context, target string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// 服务端靠这个头决定系统播报（配对提示等）用哪种语言
	req.Header.Set("Accept-Language", DeviceLanguage)
	req.Header.Set("Device-Id", c.DeviceMac)
	req.Header.Set("Client-Id", c.ClientID)
	req.Header.Set("Activation-Version", "1")
	version := c.AppVersion
	if version == "" {
		version = "1.0"
	}
	board := c.BoardName
	if board == "" {
		board = "generic"
	}
	// 官方固件一律报 generic/<版本>，服务端按这个前缀挑固件规则，别改成板子名
	req.Header.Set("User-Agent", "generic/"+version)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, raw, nil
}

// serverMessage 从错误响应里挖一句人话，比 "HTTP 404" 有用。
func serverMessage(raw []byte) string {
	var m struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &m) == nil {
		if m.Error != "" {
			return m.Error
		}
		if m.Message != "" {
			return m.Message
		}
	}
	return ""
}

// Request 请求一次 OTA。
// 返回的 OtaResult 里 WsURL 非空表示可以直接连；Activation 非空表示要用户去配对。
func (c *OtaClient) Request(ctx context.Context, body []byte) (*OtaResult, error) {
	code, raw, err := c.post(ctx, c.OtaURL, body)
	if err != nil {
		return nil, fmt.Errorf("OTA 请求失败：%w", err)
	}
	res, perr := ParseOta(raw)
	if perr != nil {
		if msg := serverMessage(raw); msg != "" {
			return nil, fmt.Errorf("OTA 返回 HTTP %d：%s", code, msg)
		}
		return nil, fmt.Errorf("OTA 返回 HTTP %d，响应无法解析", code)
	}
	if res.Activation != nil {
		return res, nil
	}
	if res.WsURL != "" {
		return res, nil
	}
	if msg := serverMessage(raw); msg != "" {
		return nil, fmt.Errorf("OTA 返回 HTTP %d：%s", code, msg)
	}
	if code < 200 || code >= 300 {
		return nil, fmt.Errorf("OTA 返回 HTTP %d", code)
	}
	return nil, errors.New("OTA 响应里没有 websocket 字段")
}

// activateURL 配对状态接口 = OTA 地址 + /activate
func (c *OtaClient) activateURL() string {
	return strings.TrimRight(c.OtaURL, "/") + "/activate"
}

// PollActivation 等用户在网页上输入配对码。
//
// 对齐官方固件（Ota::Activate）的两条规则：
//   - 激活请求体是 "{}"，绝不能带 code——带了服务端会无条件回 200（假成功）；
//   - HTTP 202 表示还在等，200 表示服务端已认这台设备，但能不能连仍以 OTA 下发的
//     token 为准，所以每轮都先问 OTA 再问 activate。
//
// onUpdate 在配对码被服务端换新时回调，方便页面跟着更新。
func (c *OtaClient) PollActivation(ctx context.Context, body []byte, interval time.Duration, onUpdate func(code, message string)) (*OtaResult, error) {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	current := ""
	for {
		res, err := c.Request(ctx, body)
		if err == nil && res.WsURL != "" {
			c.logf("设备已绑定，拿到连接地址")
			return res, nil
		}
		if err == nil && res.Activation != nil {
			if res.Activation.Code != "" && res.Activation.Code != current {
				current = res.Activation.Code
				if onUpdate != nil {
					onUpdate(current, res.Activation.Message)
				}
			}
		}

		code, raw, aerr := c.post(ctx, c.activateURL(), []byte("{}"))
		switch {
		case aerr != nil:
			c.logf("激活轮询失败：%v", aerr)
		case code == 200:
			c.logf("激活已通过，等待服务端下发配置")
		case code == 202:
			// 正常等待中，不刷日志
		default:
			msg := serverMessage(raw)
			if msg == "" {
				msg = fmt.Sprintf("HTTP %d", code)
			}
			c.logf("激活轮询：%s", msg)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}
