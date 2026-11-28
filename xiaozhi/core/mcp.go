package core

import "encoding/json"

// 设备侧 MCP 的一些固定参数，全部照官方固件/Android 端取值。
const (
	// McpVersionModern 网关的新版 MCP 协议版本
	McpVersionModern = "2026-07-28"
	// McpVersionLegacy 旧版 MCP 协议版本
	McpVersionLegacy = "2024-11-05"
	// MetaProtocolVersion 新版请求 params._meta 里的版本键
	MetaProtocolVersion = "io.modelcontextprotocol/protocolVersion"
	// MetaServerInfo 新版回包 _meta 里的服务信息键
	MetaServerInfo = "io.modelcontextprotocol/serverInfo"
	// MetaClientCapabilities 新版请求 params._meta 里的客户端能力键
	MetaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	// McpServerName 报给网关的服务名
	McpServerName = "workday-alarm-clock"
	// McpCacheTTLMs 工具目录缓存时长，网关按这个 TTL 失效重取
	McpCacheTTLMs = 60000
)

// mcpFeatures 声明设备支持的两版 MCP。少报新版，网关会先发一次失败的
// server/discover 探测，多花一个来回。
func mcpFeatures() map[string]bool {
	return map[string]bool{"mcp": true, "mcp_20260728": true}
}

// isModernMcp 新版每条请求都在 params._meta 里带版本号，据此判断回包形状。
// 不看连接级状态，所以重连也不会串。
func isModernMcp(params json.RawMessage) bool {
	if len(params) == 0 {
		return false
	}
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	v, _ := p.Meta[MetaProtocolVersion].(string)
	return v == McpVersionModern
}

// mcpReply 按版本包装回包：新版要带 resultType 和 _meta.serverInfo，旧版什么都不加。
func mcpReply(id json.RawMessage, sessionID string, result map[string]any, modern bool, version string) []byte {
	if modern {
		result["resultType"] = "complete"
		result["_meta"] = map[string]any{
			MetaServerInfo: map[string]any{"name": McpServerName, "version": version},
		}
	}
	return McpResultJSON(id, result, sessionID)
}

// mcpDiscoverResult 新版第一步的响应：列出两个版本让网关挑，能力只声明 tools。
func mcpDiscoverResult() map[string]any {
	return map[string]any{
		"supportedVersions": []string{McpVersionModern, McpVersionLegacy},
		"capabilities":      map[string]any{"tools": map[string]any{}},
		"ttlMs":             McpCacheTTLMs,
		"cacheScope":        "private",
	}
}

// mcpInitializeResult 旧版第一步的响应。
func mcpInitializeResult(version string) map[string]any {
	return map[string]any{
		"protocolVersion": McpVersionLegacy,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo": map[string]any{
			"name":    McpServerName,
			"version": version,
		},
	}
}

// mcpTextResult 工具结果：一段文本塞进 content 数组。
func mcpTextResult(text string, isError bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	}
}
