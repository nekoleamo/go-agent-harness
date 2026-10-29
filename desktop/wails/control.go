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
	"context"
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
	mux.HandleFunc("/shell/log", c.guard(c.postOnly(c.handleLog)))
	mux.HandleFunc("/shell/pick-folder", c.guard(c.postOnly(c.handlePickFolder)))
	mux.HandleFunc("/shell/export-save", c.guard(c.postOnly(c.handleExportSave)))
	mux.HandleFunc("/shell/autostart", c.guard(c.handleAutostart))
	mux.HandleFunc("/shell/update-state", c.guard(c.handleUpdateState))
	mux.HandleFunc("/shell/update-check", c.guard(c.postOnly(c.handleUpdateCheck)))
	mux.HandleFunc("/shell/update-install", c.guard(c.postOnly(c.handleUpdateInstall)))
	mux.HandleFunc("/shell/update-restart", c.guard(c.postOnly(c.handleUpdateRestart)))
	mux.HandleFunc("/shell/notify-test", c.guard(c.postOnly(c.handleNotifyTest)))
	mux.HandleFunc("/shell/diag", c.guard(c.handleDiag))
	mux.HandleFunc("/shell/notices", c.guard(c.handleNotices))
	mux.HandleFunc("/shell/session-window", c.guard(c.postOnly(c.handleSessionWindow)))
	mux.HandleFunc("/shell/open-external", c.guard(c.postOnly(c.handleOpenExternal)))
	mux.HandleFunc("/shell/reveal", c.guard(c.postOnly(c.handleReveal)))
	mux.HandleFunc("/shell/backup", c.guard(c.postOnly(c.handleBackup)))
	mux.HandleFunc("/shell/restart-service", c.guard(c.postOnly(c.handleRestartService)))
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

// postOnly 会改状态的端点只接 POST(GET 不该有副作用)。
func (c *controlPlane) postOnly(h func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "只接受 POST"})
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
		Open     bool   `json:"open"`
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
	// 写完顺手定位:用户点「导出」通常是马上要用那个文件,还得自己找一遍就是摩擦。
	if req.Open {
		if err := revealFile(p); err != nil {
			shellLogf("导出后定位失败(不影响导出): %v", err)
		}
	}
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

// handleSessionWindow 开一个新会话窗口(前端「新会话在新窗口打开」用)。
func (c *controlPlane) handleSessionWindow(w http.ResponseWriter, _ *http.Request) {
	id, err := c.sh.openSessionWindow()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// handleOpenExternal 用系统浏览器打开外链(页面里点外链都走这里,别把窗口导航走)。
func (c *controlPlane) handleOpenExternal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	_ = readJSON(r, &req)
	if err := c.sh.openExternal(req.URL); err != nil {
		shellLogf("打开外链失败: %v", err)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleReveal 在文件管理器里定位文件(导出完成后「在文件夹中显示」)。
func (c *controlPlane) handleReveal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	_ = readJSON(r, &req)
	if err := revealFile(req.Path); err != nil {
		shellLogf("定位文件失败: %v", err)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleRestartService 重启后台服务(与托盘「重新启动服务」同一条路径)。
func (c *controlPlane) handleRestartService(w http.ResponseWriter, _ *http.Request) {
	if err := c.sh.restartSidecar(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleNotices 壳侧提示(便携/迁移/失联)。页面自己来取,理由见 notices.go 文件头。
func (c *controlPlane) handleNotices(w http.ResponseWriter, _ *http.Request) {
	items := getShellNotices()
	if items == nil {
		items = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleDiag 返回诊断信息(日志路径 + 尾部):真机反馈问题时,让用户能一键拿到关键线索。
func (c *controlPlane) handleDiag(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"log_path":  shellLogPath(),
		"tail":      shellLogTail(200),
		"data_root": c.sh.dataRoot,
		"web_addr":  c.sh.addr,
	})
}

// handleBackup 手动备份数据根到用户主目录(gah-upgrade-backup/<时间戳>)。
// 升级前壳会自己调一次;这个端点给「用户手动做一件危险操作前」留个出口。
func (c *controlPlane) handleBackup(w http.ResponseWriter, _ *http.Request) {
	root := c.sh.dataRoot
	if root == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "数据根未知(服务未就绪)"})
		return
	}
	path, err := backupBeforeUpgrade(root)
	if err != nil {
		shellLogf("手动备份失败: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	shellLogf("手动备份完成: %q", path)
	writeJSON(w, http.StatusOK, map[string]any{"path": path})
}

// handleUpdateState 当前更新状态(状态机由 wails updater 持有,壳不再自己维护一份)。
func (c *controlPlane) handleUpdateState(w http.ResponseWriter, _ *http.Request) {
	st := "unconfigured"
	if c.sh.updater != nil && c.sh.app != nil {
		st = string(c.sh.app.Updater.State())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  st,
		"version": version,
		"busy":    c.sh.updateBusy.Load(),
		"seq":     c.sh.updateSeq.Load(),
	})
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

// handleUpdateCheck 只查不装(2026-10-03 的决定):一次「检查更新」不该顺手把整个应用换掉。
func (c *controlPlane) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if c.sh.updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
			"reason": "更新器未装配(见壳日志)",
		})
		return
	}
	// busy/seq:前端设置面板凭它们显示「检查中…」并判断这一轮有没有结论
	// (旧壳用 CHECK_SEQ/CHECK_DONE 做同一件事)。
	seq := c.sh.updateSeq.Add(1)
	c.sh.updateBusy.Store(true)
	defer c.sh.updateBusy.Store(false)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	rel, err := c.sh.app.Updater.Check(ctx)
	_ = seq
	if err != nil {
		shellLogf("检查更新失败: %v", err)
		writeJSON(w, http.StatusOK, map[string]any{"status": "failed", "message": err.Error()})
		return
	}
	if rel == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "upToDate", "version": version})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "available",
		"version": rel.Version,
		"notes":   rel.Notes,
	})
}

// handleUpdateInstall 下载并安装上一次检查到的更新(下载+验签+落位由框架负责)。
func (c *controlPlane) handleUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if c.sh.updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
		return
	}
	// 便携版不自动更新:Windows 不允许覆盖正在运行的程序,「装到一半失败」比「不装」更糟。
	// 但仍然保留检查(用户需要知道有没有新版),只是把动作换成「去下载」。
	if c.sh.portable {
		shellLogf("便携版: 拒绝自动安装,提示用户下载新包")
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "portable",
			"message": "便携版不自动更新:请下载新包覆盖本目录",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	// 升级前备份:整包替换一旦把数据读坏,用户没有第二份。
	if path, err := backupBeforeUpgrade(c.sh.dataRoot); err != nil {
		shellLogf("升级前备份失败(中止升级): %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"status": "failed", "message": "升级前备份失败,已中止: " + err.Error()})
		return
	} else if path != "" {
		shellLogf("升级前备份: %s", path)
	}
	if err := c.sh.app.Updater.DownloadAndInstall(ctx); err != nil {
		shellLogf("安装更新失败: %v", err)
		writeJSON(w, http.StatusOK, map[string]any{"status": "failed", "message": err.Error()})
		return
	}
	shellLogf("更新已就绪,等待重启应用")
	writeJSON(w, http.StatusOK, map[string]any{"status": "installed"})
}

// handleUpdateRestart 应用已就绪的更新(框架会起 helper 等本进程退出后换文件)。
func (c *controlPlane) handleUpdateRestart(w http.ResponseWriter, r *http.Request) {
	if c.sh.updater == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "restarting"})
	go func() {
		// 先把应答发出去再重启,否则前端拿到的是断链而不是结论。
		time.Sleep(200 * time.Millisecond)
		if err := c.sh.app.Updater.Restart(context.Background()); err != nil {
			shellLogf("重启应用失败: %v", err)
		}
	}()
}
