// 控制面:壳侧能力的本地 HTTP 接口(替代旧壳的 10 条 Tauri IPC 命令)。
//
// 为什么不用 bindings:v3.0 的 JS runtime 由 asset server 在 /wails/runtime.js 提供,
// call 走**同源** HTTP;而本项目的页面由 gah 自己提供(同源直连才能保住 SSE/WS/cookie),
// 页面源是 http://127.0.0.1:<port>,拿不到 runtime(上游 PR #4473 被以「不进 3.0 周期」关闭)。
// 于是壳侧能力改成一份**只监听回环**的小 HTTP API。
//
// 三道闸(缺一条就等于把「选目录/写文件/开关自启」开放给本机任意网页):
//  1. 监听地址固定 127.0.0.1,且校验 Host 头是回环原名(DNS rebinding 防护);
//  2. 每个请求必须带一次性 token(每次启动重新生成,经 URL fragment 交给页面,
//     fragment 不发往服务端、不进 Referer/访问日志);
//  3. CORS 只放行 `http://<本次 gah 的 addr>`,其余 Origin 一律 403。
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// controlPlane 控制面的监听器与凭据。
type controlPlane struct {
	ln     net.Listener
	token  string
	origin string // 唯一放行的页面源
	sh     *shell
}

// port 控制面端口(fragment 里交给页面)。
func (c *controlPlane) port() string {
	_, p, err := net.SplitHostPort(c.ln.Addr().String())
	if err != nil {
		return ""
	}
	return p
}

// startControlPlane 起控制面监听(127.0.0.1:0)并返回凭据。
func startControlPlane(sh *shell) (*controlPlane, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	tok, err := randomToken()
	if err != nil {
		_ = ln.Close()
		return nil, err
	}
	cp := &controlPlane{ln: ln, token: tok, origin: "http://" + sh.addr, sh: sh}
	srv := &http.Server{Handler: cp.routes(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			shellLogf("控制面服务退出: %v", err)
		}
	}()
	return cp, nil
}

// randomToken 32 字节随机 token(hex)。
//
// 例外:`GAH_SHELL_CTL_TOKEN` 显式指定(仅用于 CI 冒烟与本地调试 —— 自动化用例要能
// 不扒日志就拿到 token)。它只影响壳自己的回环控制面,不涉及 gah 的凭据。
func randomToken() (string, error) {
	if v := os.Getenv("GAH_SHELL_CTL_TOKEN"); v != "" {
		return v, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// routes 路由表。每条业务处理都包在 guard 里(闸门不靠各 handler 自觉)。
func (c *controlPlane) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/shell/ping", c.guard(c.handlePing))
	mux.HandleFunc("/shell/log", c.guard(c.handleLog))
	mux.HandleFunc("/shell/pick-folder", c.guard(c.handlePickFolder))
	mux.HandleFunc("/shell/export-save", c.guard(c.handleExportSave))
	mux.HandleFunc("/shell/autostart", c.guard(c.handleAutostart))
	mux.HandleFunc("/shell/update-state", c.guard(c.handleUpdateState))
	mux.HandleFunc("/shell/update-check", c.guard(c.handleUpdateCheck))
	mux.HandleFunc("/shell/notify-test", c.guard(c.handleNotifyTest))
	return mux
}

// guard 统一执行:Host 检查 → Origin/CORS → token 校验 → 方法检查。
func (c *controlPlane) guard(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "bad host"})
			return
		}
		// Origin 只在浏览器发跨源请求时出现;放行值只有 gah 页面那一个。
		if o := r.Header.Get("Origin"); o != "" {
			if o != c.origin {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "bad origin"})
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "X-Shell-Token, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		got := r.Header.Get("X-Shell-Token")
		if subtle.ConstantTimeCompare([]byte(got), []byte(c.token)) != 1 {
			shellLogf("控制面: 拒绝未带 token 的请求 %s %s", r.Method, r.URL.Path)
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method"})
			return
		}
		h(w, r)
	}
}

// loopbackHost 校验 Host 头指回环(DNS rebinding 防护:恶意域名解析到 127.0.0.1 时
// Host 是那个域名,不是 IP/localhost)。
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// writeJSON 统一 JSON 应答。
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// readJSON 解析请求体(上限 8MiB:导出内容走这里,别让超大 body 吃内存)。
func readJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v)
}

func (c *controlPlane) handlePing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"version":  version,
		"web_addr": c.sh.addr,
		"pid":      os.Getpid(),
	})
}

func (c *controlPlane) handleLog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Line string `json:"line"`
	}
	_ = readJSON(r, &req)
	if req.Line != "" {
		shellLogf("web: %s", req.Line)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handlePickFolder 弹系统目录选择框(阻塞到用户选定/取消)。
//
// 旧壳在这件事上用「同步命令 + 独立线程 + 前端轮询」绕了两轮真机坑(async 命令从未
// 进入函数体)。Go 侧没有那套 IPC 语义,一次阻塞调用即可 —— 但 handler 本身是并发安全的,
// 阻塞只占一个 goroutine。
func (c *controlPlane) handlePickFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title   string `json:"title"`
		Default string `json:"default"`
	}
	_ = readJSON(r, &req)
	title := req.Title
	if title == "" {
		title = "选择工作区目录"
	}
	opts := &application.OpenFileDialogOptions{
		CanChooseDirectories: true,
		CanChooseFiles:       false,
		CanCreateDirectories: true,
		Title:                title,
		Directory:            req.Default,
		Window:               c.sh.window,
	}
	p, err := c.sh.app.Dialog.OpenFileWithOptions(opts).PromptForSingleSelection()
	shellLogf("选目录: path=%q err=%v", p, err)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if p == "" {
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": p})
}

// handleExportSave 导出内容到用户选定的文件。取消 = cancelled,写失败 = 500(带原因)。
func (c *controlPlane) handleExportSave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title    string `json:"title"`
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad json: " + err.Error()})
		return
	}
	title := req.Title
	if title == "" {
		title = "保存导出文件"
	}
	opts := &application.SaveFileDialogOptions{
		Title:    title,
		Filename: req.Filename,
		Window:   c.sh.window,
	}
	p, err := c.sh.app.Dialog.SaveFileWithOptions(opts).PromptForSingleSelection()
	if err != nil {
		shellLogf("导出: 选择路径失败 %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if p == "" {
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": true})
		return
	}
	if err := os.WriteFile(p, []byte(req.Content), 0o644); err != nil {
		shellLogf("导出: 写文件失败 %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	shellLogf("导出: 已写入 %s(%d 字节)", p, len(req.Content))
	writeJSON(w, http.StatusOK, map[string]any{"path": p})
}

// handleAutostart GET 读状态,POST 开关。
func (c *controlPlane) handleAutostart(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": c.sh.autostartEnabled()})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	_ = readJSON(r, &req)
	var err error
	if req.Enabled {
		err = c.sh.app.Autostart.Enable()
	} else {
		err = c.sh.app.Autostart.Disable()
	}
	got := c.sh.autostartEnabled()
	shellLogf("控制面: 开机自启 want=%v err=%v 实际=%v", req.Enabled, err, got)
	out := map[string]any{"enabled": got}
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, http.StatusInternalServerError, out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateState 更新状态:本期恒为 idle。
// shortcut: 更新器迁移未在本批范围内(见 docs/DESKTOP_FEASIBILITY.md §11.6);
// 上线路径是 wails updater(zip/单文件 + ed25519),替换旧的 latest.json 双源方案。
func (c *controlPlane) handleUpdateState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "idle", "version": version})
}

// handleNotifyTest 发一条测试通知:真机上验「通知到底有没有送达」的唯一手段
// (通知失败在界面上完全不可见,只能看这条端点返回的真实错误)。
func (c *controlPlane) handleNotifyTest(w http.ResponseWriter, _ *http.Request) {
	err := c.sh.sendNotification("gah 通知测试", "看到这条说明系统通知通道是通的")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *controlPlane) handleUpdateCheck(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"status": "unavailable",
		"reason": "更新器尚未迁移到 Wails v3 壳,请到项目 Release 页下载",
	})
}
