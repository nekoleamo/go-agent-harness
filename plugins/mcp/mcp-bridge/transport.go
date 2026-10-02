// transport.go:MCP 请求-响应传输的**唯一接缝**(2026-10-02,补「MCP 传输面」的零依赖那半)。
//
// 为什么抽这一层:`mcpClient` 原来把「怎么把一条 JSON-RPC 送出去」写死在
// 「往子进程 stdin 写一行 + 从 stdout 逐行读」。要支持远程 server(Streamable HTTP),
// 就得让这一段变成可替换的 —— 否则会出现两份并行实现(一份 stdio 一份 http),
// 握手/超时/错误语义迟早各走各的。
//
// 现在有两种实现:
//   - stdioTransport(transport_stdio.go):现状逐行 JSON-RPC,代码**原样搬移**,语义零变化。
//   - httpTransport(transport_http.go):Streamable HTTP(2025-03-26)。
//
// **本文件不含凭据逻辑**:headers 只从 $GAH_HOME/config/mcp.yaml 读进来,且该类型
// 在序列化时一律打码(见 mcpconfig.Server.MarshalJSON)。OAuth 流程**不做**(绑在
// 已登记的 OAuth 暂缓项上)—— server 回 401 时如实说「需要认证」,不假装能自动登录。
package mcpbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// maxHTTPResponse 单个 HTTP 响应体上限(安全审计 C5 同款闸):远程 server 的响应是
// **不可信输入**,没有上限就能让宿主一路分配到 OOM。8 MiB 与 stdio 单行上限同档。
const maxHTTPResponse = 8 << 20

// httpTimeout 单次往返上限(零依赖 ⇒ 自己定;stdio 侧靠 ctx/60s 兜底,这里取更紧的
// 90s:远程 server 慢不代表值得一直等)。
const httpTimeout = 90 * time.Second

// transport 一条 JSON-RPC 往返的抽象。
//
// 形状刻意只有三个方法:协议握手(initialize/initialized)、工具面(tools/list)、
// 工具调用(tools/call)在两个实现里**逐字相同** —— 传输面该薄就薄,不然差异会长回去。
type transport interface {
	// RoundTrip 发请求并等同一 id 的响应(内部处理 id 匹配)。
	RoundTrip(ctx context.Context, req rpcReq) (rpcResp, error)
	// Notify 发一条无 id 的通知(不等响应;失败**返回错误**而不是吞掉)。
	Notify(ctx context.Context, method string, params any) error
	// Close 释放底层资源(stdio = 杀子进程;http = 关空闲连接)。
	Close() error
	// Kind 传输种类("stdio"/"http"),供上层决定「有没有本地进程可看护/可施加内核沙箱」。
	Kind() string
	// Redacted 一行**可安全进日志**的描述(绝不含 headers 值)。
	Redacted() string
}

// errSessionExpired 远程 server 说这个 session id 不认识(404 + session 相关)。
// 上层据此重新 initialize 一次 —— 协议允许,但**最多一次**,不许无限重试。
var errSessionExpired = errors.New("mcp: 远程会话已失效(需重新 initialize)")

// respErr 把 JSON-RPC 错误响应转成 Go error(两个实现共用同一句文案)。
func respErr(r rpcResp) error {
	if r.Error != nil {
		return fmt.Errorf("mcp-rpc error %d: %s", r.Error.Code, r.Error.Message)
	}
	return nil
}

// 解出 result 到 result(为空则原样返回)。
func decodeResult(r rpcResp, result any) error {
	if err := respErr(r); err != nil {
		return err
	}
	if result != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, result)
	}
	return nil
}

// boundedRead 读 body 但不超过上限(超限显式报错,不截断 —— 截断的 JSON 只会给出
// 一个语法错误,让人误以为是协议问题)。
func boundedRead(r io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxHTTPResponse+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxHTTPResponse {
		return nil, fmt.Errorf("mcp: 远程 server 响应体超上限 %d 字节", maxHTTPResponse)
	}
	return raw, nil
}

// serverSpec 插件装配期解析出的连接参数。
//
// 为什么要单独一个类型:holder 的崩溃重连要**重建同一份连接**,而重建时不能再回头读
// manifest(它可能已被 Disposer 撤掉)。同时它给出一行可安全进日志的描述。
type serverSpec struct {
	kind    string // stdio | http
	command string
	args    []string
	url     string
	headers map[string]string
}

// Redacted 可安全进日志的描述(绝不含 header 值,也不含 path —— path 常带 token)。
func (s serverSpec) Redacted() string {
	if s.kind == "http" {
		return newHTTPTransportSummary(s.url, len(s.headers))
	}
	return fmt.Sprintf("stdio %s(%d 参数)", s.command, len(s.args))
}

// parseServerSpec 从 manifest data 解析连接参数(缺省 stdio)。
//
// 校验放在这里而不是只靠配置层:mcp-bridge 也可能被**直接装配**(插件清单/env),
// 那条路不经过 mcpconfig.Normalize。
func parseServerSpec(data map[string]any) (serverSpec, error) {
	spec := serverSpec{kind: "stdio"}
	if v, ok := data["transport"].(string); ok && v != "" {
		spec.kind = strings.ToLower(strings.TrimSpace(v))
	}
	switch spec.kind {
	case "http":
		spec.url, _ = data["url"].(string)
		spec.url = strings.TrimSpace(spec.url)
		if spec.url == "" {
			return spec, fmt.Errorf("mcp-bridge: transport=http 需要 url")
		}
		if h, ok := data["headers"].(map[string]any); ok {
			spec.headers = make(map[string]string, len(h))
			for k, v := range h {
				ks := strings.ToLower(strings.TrimSpace(k))
				if ks == "" {
					continue
				}
				if s, ok := v.(string); ok {
					spec.headers[ks] = strings.TrimSpace(s)
				}
			}
		}
		return spec, nil
	case "stdio":
		spec.command, _ = data["command"].(string)
		spec.command = strings.TrimSpace(spec.command)
		if a, ok := data["args"].([]any); ok {
			for _, x := range a {
				if s, ok := x.(string); ok {
					spec.args = append(spec.args, s)
				}
			}
		}
		if spec.command == "" {
			return spec, fmt.Errorf("mcp-bridge: 需要 data.command(远程 server 用 transport=http + url)")
		}
		return spec, nil
	default:
		return spec, fmt.Errorf("mcp-bridge: transport %q 非法(可选 stdio|http)", spec.kind)
	}
}

// newClient 按 spec 建连接(两种传输的握手逻辑在 mcpClient 里,逐字相同)。
func newClient(spec serverSpec) (*mcpClient, error) {
	if spec.kind == "http" {
		tr, err := newHTTPTransport(spec.url, spec.headers)
		if err != nil {
			return nil, err
		}
		return &mcpClient{tr: tr}, nil
	}
	return newStdioClient(spec.command, spec.args)
}
