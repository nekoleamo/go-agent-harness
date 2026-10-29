// session:多会话窗口(旧 Rust 壳的 spawnSession + open_session_window 等价移植)。
//
// 为什么要多个窗口:一个窗口 = 一个 webview + 一条 SSE 连接 + 一份会话日志实例。
// 并行跑两条任务时,用户要能同时盯着两边,而不是来回切会话(切会话会重建对话状态)。
//
// 三条纪律(照抄旧壳,都是真机踩出来的):
//  1. 会话 id 由**宿主**分配(POST /api/sessions {"action":"spawn"}),壳只负责开窗口 ——
//     壳自己造 id 会在数据层对不上;
//  2. 窗口数量设上限:不设的话「连点托盘」就能把本机资源吃满,而症状(界面变卡)与原因
//     隔着好几层;
//  3. 关掉会话窗口只是关它自己,主窗口与 sidecar 照旧。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// maxSessionWindows 会话窗口上限(每个窗口一份 webview + SSE + 会话日志实例)。
const maxSessionWindows = 8

// sessionWindows 本进程开着的会话窗口(id → 是否存在)。
var sessionWindows = struct {
	mu  sync.Mutex
	ids map[string]bool
}{ids: map[string]bool{}}

// spawnSession 向宿主要一个新会话 id(失败返回空串,由调用方给出明确结论)。
func (s *shell) spawnSession() string {
	body := bytes.NewBufferString(`{"action":"spawn"}`)
	req, err := http.NewRequest(http.MethodPost, "http://"+s.addr+"/api/sessions", body)
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	if c := cookieHeader(s.gahToken); c != "" {
		req.Header.Set("Cookie", c)
	}
	resp, err := gahClient().Do(req)
	if err != nil {
		shellLogf("新会话窗口: 请求宿主失败: %v", err)
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		shellLogf("新会话窗口: 宿主返回 HTTP %d: %s", resp.StatusCode, string(raw))
		return ""
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ID == "" {
		shellLogf("新会话窗口: 应答里没有 id: %s", string(raw))
		return ""
	}
	return out.ID
}

// sessionURL 会话窗口地址:主窗口同源 + &session=<id>;凭据仍在 fragment 里
// (gah 的 token 必须最后,引导页的正则会取到 fragment 末尾)。
func (s *shell) sessionURL(sessionID string) string {
	frag := fmt.Sprintf("shellport=%s&shelltoken=%s",
		url.QueryEscape(s.ctl.port()), url.QueryEscape(s.ctl.token))
	if s.gahToken != "" {
		frag += "&token=" + url.QueryEscape(s.gahToken)
	}
	return "http://" + s.addr + "/?shell=desktop&session=" + url.QueryEscape(sessionID) + "#" + frag
}

// openSessionWindow 开一个新会话窗口。返回会话 id 与失败原因(二选一)。
func (s *shell) openSessionWindow() (string, error) {
	sessionWindows.mu.Lock()
	if len(sessionWindows.ids) >= maxSessionWindows {
		sessionWindows.mu.Unlock()
		return "", fmt.Errorf("会话窗口已达上限 %d 个,请先关掉一些", maxSessionWindows)
	}
	sessionWindows.mu.Unlock()

	id := s.spawnSession()
	if id == "" {
		return "", fmt.Errorf("向宿主申请新会话失败(sidecar 未就绪?)")
	}
	w := s.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:           "gah-session-" + id,
		Title:          "gah",
		Width:          1200,
		Height:         800,
		URL:            s.sessionURL(id),
		EnableFileDrop: false,
	})
	w.Show()
	w.Focus()

	sessionWindows.mu.Lock()
	sessionWindows.ids[id] = true
	sessionWindows.mu.Unlock()
	shellLogf("新会话窗口: session=%s(当前 %d 个)", id, len(sessionWindows.ids))

	// 关窗即回收计数:否则「开-关」循环几次就到上限,用户会以为功能坏了。
	w.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
		sessionWindows.mu.Lock()
		delete(sessionWindows.ids, id)
		n := len(sessionWindows.ids)
		sessionWindows.mu.Unlock()
		shellLogf("会话窗口已关闭: session=%s(剩 %d 个)", id, n)
	})
	return id, nil
}
