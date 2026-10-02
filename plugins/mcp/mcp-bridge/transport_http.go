// transport_http.go:Streamable HTTP 传输(2025-03-26 规范)。
//
// 为什么只做这一半、不做 OAuth:凭据托管(刷新 token、授权码流、多 server 的凭据库)
// 与已登记的 **OAuth 暂缓项**绑在一起(那条的解冻条件是「用户明确要不粘 Key 登录」)。
// 本文件因此只支持**静态 header**(API Key / Bearer,写在 0600 的 mcp.yaml 里)——
// server 回 401/403 时**如实报错**,不假装能自动登录。
//
// 协议要点(照规范实现,逐条标注用在哪):
//  1. 客户端 POST JSON-RPC 单条请求到 server 端点,`Accept: application/json, text/event-stream`。
//  2. 响应两种形态:单条 JSON(`application/json`)或 SSE 流(`text/event-stream`,
//     `data:` 行里是 JSON-RPC 消息)。**两种都要能读** —— 只实现 JSON 会让一半的
//     server 连不上(而且症状是「第一次能通、第二次超时」这种极难查的形态)。
//  3. `Mcp-Session-Id`:server 可在 initialize 响应里给会话 id,后续请求必须带上。
//     带一个 server 不认识的 id 时 server 回 404 → 本实现**重新 initialize 一次**
//     (协议允许;最多一次,不许无限重试)。
//  4. 通知(无 id)server 可回 202 Accepted(无 body)。
//  5. 响应可包含**与请求无关的 notification**(如工具列表变更)—— 读流时要跳过它们,
//     只认 id 匹配的那条。
package mcpbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/internal/mcpconfig"
)

// httpTransport 远程 MCP server(Streamable HTTP)。
type httpTransport struct {
	endpoint string
	headers  map[string]string
	client   *http.Client

	mu        sync.Mutex
	sessionID string
	// reinitDone 保证「session 失效后只重握手一次」—— 否则一个持续回 404 的 server
	// 会让每次调用都重试 initialize,把远端和本地一起拖死。
	reinitDone bool
}

func (h *httpTransport) Kind() string { return "http" }

// Redacted 可安全进日志的描述(与 serverSpec 共用同一份口径)。
func (h *httpTransport) Redacted() string { return newHTTPTransportSummary(h.endpoint, len(h.headers)) }

// newHTTPTransportSummary **只给 host + 是否有凭据**:不给 header 值,也不给 path
// (path 常带 token,例如 /api/<key>/mcp)。
func newHTTPTransportSummary(endpoint string, headerCount int) string {
	u, err := url.Parse(endpoint)
	host := endpoint
	if err == nil && u.Host != "" {
		host = u.Scheme + "://" + u.Host
	}
	auth := "无凭据"
	if headerCount > 0 {
		auth = fmt.Sprintf("带 %d 个请求头(值不回显)", headerCount)
	}
	return fmt.Sprintf("http %s %s", host, auth)
}

// newHTTPTransport 构造(headers 已在配置层规范化:键小写、值 trim)。
func newHTTPTransport(endpoint string, headers map[string]string) (*httpTransport, error) {
	// 端点合规性走**配置层那一份**规则(https 或回环)——两处各判一次早晚会漂,
	// 且漂法很难查:配置层放行、装配时才拒,症状是「面板保存成功、重启后工具不见了」。
	if err := mcpconfig.ValidateEndpoint(endpoint, len(headers) > 0); err != nil {
		return nil, fmt.Errorf("mcp: 远程端点不合规: %w", err)
	}
	return &httpTransport{
		endpoint: endpoint,
		headers:  headers,
		client:   &http.Client{Timeout: httpTimeout},
	}, nil
}

// do 发一次 POST,返回响应体与状态码。
func (h *httpTransport) do(ctx context.Context, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// 规范要求客户端同时接受两种响应形态。
	req.Header.Set("Accept", "application/json, text/event-stream")
	// 规范建议的协议版本头(缺失不影响,给了更兼容)。
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	h.mu.Lock()
	sid := h.sessionID
	h.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("mcp: 连接远程 server 失败:%w", err)
	}
	raw, err := boundedRead(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, nil, err
	}
	// session id 可能出现在 initialize 之外的响应里(规范允许),顺手收下。
	if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
		h.mu.Lock()
		h.sessionID = got
		h.mu.Unlock()
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		return resp, raw, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, nil, fmt.Errorf("mcp: 远程 server 要求认证(HTTP %d);"+
			"本版本只支持**静态请求头**里的 API Key / Bearer,OAuth 登录流程未实现(已登记为暂缓项)",
			resp.StatusCode)
	case http.StatusNotFound:
		return nil, nil, errSessionExpired
	default:
		return nil, nil, fmt.Errorf("mcp: 远程 server 返回 HTTP %d: %s", resp.StatusCode, truncateForErr(raw))
	}
}

// Notify 发通知(无 id):允许 202 无 body。
func (h *httpTransport) Notify(ctx context.Context, method string, params any) error {
	b, err := json.Marshal(rpcReq{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	_, raw, err := h.do(ctx, b)
	if err != nil {
		return err
	}
	// 202 且无 body = 正常;若给了 body,里面若含 error 也该报出来(否则「通知被拒」没人知道)。
	if len(bytes.TrimSpace(raw)) > 0 {
		var resp rpcResp
		if json.Unmarshal(raw, &resp) == nil && resp.Error != nil {
			return respErr(resp)
		}
	}
	return nil
}

// RoundTrip 发请求并等同一 id 的响应(JSON 或 SSE 两种响应都读)。
func (h *httpTransport) RoundTrip(ctx context.Context, req rpcReq) (rpcResp, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return rpcResp{}, err
	}
	resp, raw, err := h.do(ctx, b)
	if err != nil {
		if errorsIs(err, errSessionExpired) {
			return h.retryAfterReinit(ctx, b, req)
		}
		return rpcResp{}, err
	}
	ct := resp.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		return parseSSE(raw, req.ID)
	default: // application/json 或未标(按 JSON 试,失败再回退 SSE 解析)
		var r rpcResp
		if err := json.Unmarshal(raw, &r); err == nil {
			return r, nil
		}
		// 有些 server 不标 Content-Type 却回 SSE(实现不一);两种都试,都失败才报错。
		if r2, err2 := parseSSE(raw, req.ID); err2 == nil {
			return r2, nil
		}
		return rpcResp{}, fmt.Errorf("mcp: 无法解析远程响应(Content-Type=%q):%s", ct, truncateForErr(raw))
	}
}

// retryAfterReinit session 失效:重新 initialize 一次,再重发原请求(最多一次)。
func (h *httpTransport) retryAfterReinit(ctx context.Context, body []byte, req rpcReq) (rpcResp, error) {
	h.mu.Lock()
	already := h.reinitDone
	h.reinitDone = true
	h.sessionID = ""
	h.mu.Unlock()
	if already {
		return rpcResp{}, errSessionExpired
	}
	initReq := rpcReq{JSONRPC: "2.0", ID: -1, Method: "initialize", Params: initializeParams{
		ProtocolVersion: "2025-06-18",
		Capabilities:    map[string]any{},
		ClientInfo:      map[string]string{"name": "gah", "version": "dev"},
	}}
	if b, err := json.Marshal(initReq); err == nil {
		// 握手本身的失败不致命地忽略:真正的错误会在重发原请求时暴露出来。
		if _, _, err := h.do(ctx, b); err != nil {
			return rpcResp{}, fmt.Errorf("mcp: 重新握手失败:%w", err)
		}
	}
	_, raw, err := h.do(ctx, body)
	if err != nil {
		return rpcResp{}, err
	}
	var r rpcResp
	if err := json.Unmarshal(raw, &r); err == nil {
		return r, nil
	}
	return parseSSE(raw, req.ID)
}

// Close 释放连接(http 传输没有本地进程可杀)。
func (h *httpTransport) Close() error {
	h.client.CloseIdleConnections()
	return nil
}

// parseSSE 从 SSE 文本里取出**id 匹配**的那条 JSON-RPC 消息。
// 非匹配的消息(通知、别的 id 的响应)按规范跳过。
func parseSSE(raw []byte, wantID int64) (rpcResp, error) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), maxHTTPResponse)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue // 空行 / SSE 注释(心跳)
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // event:/id:/retry: 等字段:本实现不需要
		}
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			continue
		}
		var r rpcResp
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			continue // 一条坏消息不该让整个流失败(规范允许流里混多类消息)
		}
		if r.ID == wantID {
			return r, nil
		}
	}
	if err := sc.Err(); err != nil {
		return rpcResp{}, fmt.Errorf("mcp: 解析 SSE 流失败:%w", err)
	}
	return rpcResp{}, fmt.Errorf("mcp: SSE 流里没有 id=%d 的响应(流提前结束?)", wantID)
}

// truncateForErr 错误信息里带一段响应体片段(限长:错误信息会进日志与 UI)。
func truncateForErr(raw []byte) string {
	const max = 200
	s := strings.TrimSpace(string(raw))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	if s == "" {
		return "(空响应)"
	}
	return s
}

// errorsIs 局部化 errors.Is(避免为一个调用引 errors 包到本文件的 import 组)。
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
