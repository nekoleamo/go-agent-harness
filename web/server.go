// HTTP/SSE 服务:能力全在宿主,浏览器只订阅事件流(SSE 下行)+ REST 上行。
// 路由:
//
//	GET  /             静态前端(embed web/dist;data.static_dir 覆写=开发态 Vite HMR)
//	GET  /api/events   SSE 流(?after=<seq> 断线续传;全量历史重放 + 实时帧)
//	POST /api/input    {content};running 时 409;"/" 前缀走 ctx.commands,其余注入 agentLoop
//	POST /api/confirm  {id, ok} 审批应答
//	GET  /api/state    状态快照(model/thinking/sandbox/stats/session/running/version)
//	GET  /api/sessions 会话列表;POST {action:switch|new|fork|clone|delete, id, seq} 切换/新建/分支/克隆/删除
//	GET  /api/sessions/{id}/export 会话导出(原始 jsonl 下载;id 空=主会话)
//	GET  /api/workspaces 工作区历史;DELETE /api/workspaces/{key} 删除工作区记录(不动文件夹)
//	POST /api/sessions/rename {name} 会话改名(编辑会话名)
//	GET  /api/commands 命令注册表;POST /api/commands/{name} {args} 直接执行
//	POST /api/compact {prompt?} 手动滚动压缩(摘要+折叠数);POST /api/settings/history {n} 历史注入条数
//	GET/POST /api/tools[/{name}] 工具清单/调用;GET /api/jobs… 后台任务
//	GET /api/plugins … 插件清单/加载/卸载;GET /api/models 聚合模型列表
//	GET/POST /api/providers … 多 provider;POST /api/reload 指令热更
//	POST /api/shutdown 优雅停机(触发宿主 system/shutdown → DisposeAll;桌面壳/跨平台统一通道)
//	POST /api/attachments 附件上传(multipart "file";流式/大小 20MB/类型白名单;落盘
//	$GAH_HOME/attachments/<时间戳>/;GET /attachments/{...} 静态预览(仅本机/鉴权外))
//
// 安全:默认绑定 127.0.0.1:2233;data.auth_token 非空时全部 /api/* 需携带(Authorization: Bearer / ?token=)。
// 可选服务(Ctx 可选注入)未装配时对应端点返回 503/501 显式错误,不静默降级。
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/providerfile"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

//go:embed dist
var distFS embed.FS

// Config Web UI 服务配置(插件 data 透传)。
type Config struct {
	Addr      string // 监听地址(data.addr;默认 127.0.0.1:2233)
	AuthToken string // 可选鉴权 token;空 = 仅本机绑定(默认 addr 即本机)
	StaticDir string // 可选静态目录覆写(开发态 Vite HMR;优先于 embed)
	// UIPluginsDir UI 插件目录(M7.2):扫 <dir>/<id>/manifest.json 聚合槽位覆盖,
	// /ui-plugins/ 静态托管(插件 vite 产物);默认 $GAH_HOME/ui-plugins。
	UIPluginsDir string

	// AttachmentsDir 附件目录(附件一期):POST /api/attachments 上传落盘根,
	// GET /attachments/{...} 静态托管预览;默认 $GAH_HOME/attachments(ui-web-app 注入)。
	AttachmentsDir string
}

// CommandResult 命令执行结果帧载荷(/ 命令经 ctx.commands 执行,输出回前端)。
type CommandResult struct {
	Raw    string `json:"raw"`    // "name args"(前端回显)
	Output string `json:"output"` // 输出文本(可多行)
	Error  string `json:"error"`  // 结构化错误(空 = 成功)
}

// Server Web UI 服务:持有宿主服务依赖 + 事件通道 + 确认服务。
type Server struct {
	cfg      Config
	hub      *EventHub
	confirm  *ConfirmService
	question *QuestionService
	log      *slog.Logger

	loop     sdk.AgentLoop
	sessions sdk.SessionLog
	llm      sdk.LLMService
	sb       sdk.Sandbox
	ap       sdk.ApprovalService // 可选(审批档位 M17:未装配时 state 省略/control 400)
	bk       sdk.BackupService   // 可选(整体备份 M18:未装配时 /api/backup 503)
	us       sdk.UsageStatsService // 可选
	cs       sdk.CwdSessions       // 可选
	cmds     sdk.CommandRegistry   // 可选(未装配 = / 命令不可用)
	tools    sdk.ToolRegistry      // 可选(工具清单/调用/todo 面板)
	jobs     sdk.JobService        // 可选(后台任务)
	pm       sdk.PluginManager     // 可选(插件启停)
	imc      sdk.IMChannelService  // 可选(IM 通道状态 /api/im/channels;未装配 503)
	imLogin  sdk.IMLoginProvider  // 可选(面板扫码登录 /api/im/login;渠道未实现则 503)
	sp       sdk.SystemPromptService // 可选(/reload 指令热更)
	tc       sdk.TurnControl       // 可选(回合取消 /api/control cancel;未装配 = 503)

	running     atomic.Bool
	http        *http.Server
	unsubStatus sdk.Disposer // agent/status 订阅撤销(驱动 running 复位)

	// OnReady 监听成功回调(参数=访问 URL;监听失败不触发,插件层据此自动打开浏览器)。
	OnReady func(url string)

	// OnShutdown 停机触发回调(POST /api/shutdown;由插件层绑定宿主 system/shutdown 事件,
	// 触发 cmd/gah 退出 → DisposeAll 回收插件/外部进程。nil = 未装配 → 端点 503)。
	OnShutdown func()
}

// New 构造服务;依赖经插件装配层注入(Required 之外的可选注入失败即忽略)。
func New(cfg Config, hub *EventHub, confirm *ConfirmService, log *slog.Logger) *Server {
	return &Server{cfg: cfg, hub: hub, confirm: confirm, question: NewQuestionService(hub), log: log}
}

// Question Web 提问服务(P3;ui-web-app 用它注册渠道呈现者或 Provide ctx.question)。
func (s *Server) Question() *QuestionService { return s.question }

// Inject 注入宿主服务;required 缺失返回错误(显式失败),optional 缺失跳过。
func (s *Server) Inject(c sdk.Ctx) error {
	if err := c.Inject("ctx.agentLoop", &s.loop); err != nil {
		return err
	}
	if err := c.Inject("ctx.sessions", &s.sessions); err != nil {
		return err
	}
	if err := c.Inject("ctx.llm", &s.llm); err != nil {
		return err
	}
	if err := c.Inject("ctx.sandbox", &s.sb); err != nil {
		return err
	}
	_ = c.Inject("ctx.approval", &s.ap) // 可选:未装配则 state 省略 ap、control approval 400
	_ = c.Inject("ctx.backup", &s.bk)   // 可选:未装配则 /api/backup 503
	_ = c.Inject("ctx.usageStats", &s.us)
	_ = c.Inject("ctx.cwdSessions", &s.cs)
	_ = c.Inject("ctx.commands", &s.cmds)
	_ = c.Inject("ctx.tools", &s.tools)
	_ = c.Inject("ctx.jobs", &s.jobs)
	_ = c.Inject("ctx.pluginManager", &s.pm)
	_ = c.Inject("ctx.imChannels", &s.imc) // 可选:未装配则 /api/im/channels 503
	if lp, ok := s.imc.(sdk.IMLoginProvider); ok {
		s.imLogin = lp // 渠道实现扫码登录(如 ui-im-wechat)时启用面板入口
	}
	_ = c.Inject("ctx.systemPrompt", &s.sp)
	_ = c.Inject("ctx.turnControl", &s.tc)
	// running 状态:随 agent/status 事件驱动(回合开始 running,结束 idle)
	unsub := c.Subscribe(sdk.EventAgentStatus, func(_ context.Context, ev *sdk.Event) error {
		s.running.Store(ev.Payload == "running")
		return nil
	})
	s.unsubStatus = unsub
	return nil
}

// Start 启动监听(阻塞;外部 goroutine 调用,Shutdown 停止)。
// 先实际监听成功(失败返回错误,不打 listening 日志),再回调 OnReady 并开始服务。
func (s *Server) Start() error {
	if s.cfg.Addr == "" {
		s.cfg.Addr = "127.0.0.1:2233"
	}
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("web ui 监听失败: %w", err)
	}
	s.http = &http.Server{Handler: s.authMiddleware(s.handler())}
	if s.OnReady != nil {
		s.OnReady("http://" + s.cfg.Addr)
	}
	s.log.Info("web ui listening", "addr", s.cfg.Addr)
	return s.http.Serve(ln)
}

// Handler 导出路由(测试/外部挂载经 httptest 直挂)。
func (s *Server) Handler() http.Handler {
	return s.handler()
}

// handler 组装路由(与鉴权解耦)。
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/events/ws", s.handleEventsWS)
	mux.HandleFunc("POST /api/input", s.handleInput)
	mux.HandleFunc("POST /api/confirm", s.handleConfirm)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/control", s.handleControl)
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
	mux.HandleFunc("POST /api/sessions", s.handleSessions)
	mux.HandleFunc("GET /api/sessions/{id}/export", s.handleSessionExport)
	mux.HandleFunc("POST /api/sessions/rename", s.handleSessionRename)
	mux.HandleFunc("GET /api/workspaces", s.handleWorkspaces)
	mux.HandleFunc("DELETE /api/workspaces/{key}", s.handleWorkspaceDelete)
	mux.HandleFunc("GET /api/tools", s.handleTools)
	mux.HandleFunc("POST /api/tools/{name}", s.handleToolCall)
	mux.HandleFunc("GET /api/jobs", s.handleJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleJobGet)
	mux.HandleFunc("POST /api/jobs/{id}/kill", s.handleJobKill)
	mux.HandleFunc("POST /api/commands/{name}", s.handleCommandRun)
	mux.HandleFunc("GET /api/plugins", s.handlePlugins)
	mux.HandleFunc("POST /api/plugins/{id}/load", s.handlePluginAction)
	mux.HandleFunc("POST /api/plugins/{id}/unload", s.handlePluginAction)
	mux.HandleFunc("GET /api/models", s.handleModels)
	mux.HandleFunc("GET /api/providers", s.handleProviders)
	mux.HandleFunc("POST /api/providers", s.handleProviderAdd)
	mux.HandleFunc("POST /api/providers/{name}/use", s.handleProviderUse)
	mux.HandleFunc("DELETE /api/providers/{name}", s.handleProviderDelete)
	mux.HandleFunc("POST /api/reload", s.handleReload)
	mux.HandleFunc("POST /api/shutdown", s.handleShutdown)
	mux.HandleFunc("POST /api/compact", s.handleCompact)
	mux.HandleFunc("POST /api/attachments", s.handleAttachments)
	mux.HandleFunc("POST /api/settings/history", s.handleSettingsHistory)
	mux.HandleFunc("GET /api/commands", s.handleCommands)
	mux.HandleFunc("GET /api/ui-plugins", s.handleUIPlugins)
	mux.HandleFunc("GET /api/todo", s.handleTodo)
	mux.HandleFunc("GET /api/backup", s.handleBackup)
	mux.HandleFunc("POST /api/backup", s.handleBackup)
	mux.HandleFunc("GET /api/im/channels", s.handleIMChannels)
	mux.HandleFunc("POST /api/im/login", s.handleIMLogin)
	mux.HandleFunc("GET /api/im/login/state", s.handleIMLoginState)
	mux.HandleFunc("POST /api/question", s.handleQuestion)
	mux.Handle("/ui-plugins/", s.uiPluginsHandler())
	mux.Handle("/attachments/", s.attachmentsHandler())
	mux.Handle("/", s.staticHandler())
	return mux
}

// Shutdown 停止服务并撤销状态订阅。
func (s *Server) Shutdown() {
	if s.unsubStatus != nil {
		s.unsubStatus()
	}
	if s.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.http.Shutdown(ctx)
	}
}

// —— SSE ——

// afterOf 解析断线续传游标(Last-Event-ID 头或 ?after= 参数;SSE 与 WS 共用)。
func (s *Server) afterOf(r *http.Request) uint64 {
	after := uint64(0)
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		if v, err := strconv.ParseUint(h, 10, 64); err == nil {
			after = v
		}
	}
	if q := r.URL.Query().Get("after"); q != "" {
		if v, err := strconv.ParseUint(q, 10, 64); err == nil {
			after = v
		}
	}
	return after
}

// consumeStream 通道消费(通道 seam:SSE 与 WS 共用):先订阅实时(弥合
// 重放快照与订阅建立之间的广播 gap)→ 历史重放(seq > after,经 seen 去重
// ——重放期间已入实时流的新帧不重复发)→ 实时转发。sink 返回错误
// (载体写失败=客户端断连)即退;stop 为请求上下文取消。
func (s *Server) consumeStream(after uint64, sink func(Frame) error, stop <-chan struct{}) {
	ch, unsub := s.hub.Stream()
	defer unsub()
	seen := after // 已消费会话游标(会话帧按 Seq 全局递增;非会话帧 ID=0 不参与去重)
	for _, f := range s.hub.ReplayAfter(s.sessions, after) {
		if f.ID > 0 && f.ID <= seen {
			continue // 已被实时流抢先(重放期间新帧入 ch 排队,seq 去重防双发)
		}
		if err := sink(f); err != nil {
			return
		}
		seen = f.ID
	}
	for {
		select {
		case f := <-ch:
			if f.ID > 0 {
				if f.ID <= seen {
					continue // 重放已发(去重)
				}
				seen = f.ID
			}
			if err := sink(f); err != nil {
				return
			}
		case <-stop:
			return
		}
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 断线重连指令(SSE 规范:客户端按此间隔自动重连;配合 Last-Event-ID 续传)
	if _, err := io.WriteString(w, "retry: 3000\n\n"); err != nil {
		return
	}
	fl.Flush()
	sse := func(f Frame) error {
		var b strings.Builder
		if f.ID > 0 {
			b.WriteString("id: ")
			b.WriteString(strconv.FormatUint(f.ID, 10))
			b.WriteByte('\n')
		}
		b.WriteString("event: ")
		b.WriteString(f.Type)
		b.WriteByte('\n')
		raw, err := json.Marshal(f)
		if err != nil {
			return nil
		}
		b.WriteString("data: ")
		b.Write(raw)
		b.WriteString("\n\n")
		if _, err := w.Write([]byte(b.String())); err != nil {
			return err
		}
		fl.Flush()
		return nil
	}
	s.consumeStream(s.afterOf(r), sse, r.Context().Done())
}

// handleEventsWS WebSocket 通道(/api/events/ws):同 payload 不同载体。
// 握手失败(非升级/跨源)回 4xx,前端 transport 据此降级 EventSource。
func (s *Server) handleEventsWS(w http.ResponseWriter, r *http.Request) {
	conn, err := wsTryUpgrade(w, r)
	if err != nil {
		return
	}
	defer conn.Close()
	wsc := func(f Frame) error {
		raw, err := json.Marshal(f)
		if err != nil {
			return nil
		}
		return conn.WriteText(raw)
	}
	// hijack 后 r.Context() 已取消(服务端接管连接):停止信号用永不关闭信道,
	// 客户端断连由 WriteText 错误驱动退出(consumeStream 的 sink err 路径)。
	never := make(chan struct{})
	s.consumeStream(s.afterOf(r), wsc, never)
}

// —— REST ——

type inputReq struct {
	Content     string   `json:"content"`
	Attachments []string `json:"attachments"` // 附件本地路径(/api/attachments 返回的 Path;须位于附件目录内)
}

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	var req inputReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		http.Error(w, "空输入", http.StatusBadRequest)
		return
	}
	// 附件校验(须位于附件目录且存在;防注入任意路径)
	for _, a := range req.Attachments {
		if !s.validAttachment(a) {
			http.Error(w, "附件路径非法或不可访问: "+a, http.StatusBadRequest)
			return
		}
	}
	// running 互斥(TUI 同语义:回合进行中拒绝再次提交)
	if s.running.Load() {
		http.Error(w, "回合进行中,等待完成或取消后再提交", http.StatusConflict)
		return
	}
	// 附件引用文本注入:模型可见附件路径(文本类可经 file 工具读取;图片另走 A2 结构化视觉)
	atts := attachmentList(s.cfg.AttachmentsDir, req.Attachments)
	if len(atts) > 0 {
		var b strings.Builder
		b.WriteString(content)
		b.WriteString("\n\n[附件]\n")
		for i, a := range atts {
			b.WriteString(fmt.Sprintf("%d. %s(路径 %s)\n", i+1, a.Name, a.Rel))
		}
		content = b.String()
	}
	if strings.HasPrefix(content, "/") {
		s.runCommand(content, w)
		return
	}
	s.running.Store(true)
	go func() {
		defer s.running.Store(false)
		var err error
		if l, ok := s.loop.(sdk.AttachmentInput); ok && len(atts) > 0 {
			err = l.RunWithAttachments(context.Background(), content, atts)
		} else {
			err = s.loop.Run(context.Background(), content)
		}
		if err != nil {
			s.hub.Push(Frame{Type: FrameError, Payload: err.Error()})
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// runCommand 斜杠命令经 ctx.commands 同步执行(输出回 SSE 帧)。
func (s *Server) runCommand(content string, w http.ResponseWriter) {
	if s.cmds == nil {
		http.Error(w, "命令不可用: ctx.commands 未装配", http.StatusServiceUnavailable)
		return
	}
	fields := strings.Fields(content)
	name := strings.TrimPrefix(fields[0], "/")
	spec, ok := s.cmds.Get(name)
	if !ok {
		http.Error(w, "未知命令 "+name, http.StatusBadRequest)
		return
	}
	out, err := spec.Run(fields[1:])
	res := &CommandResult{Raw: content, Output: out}
	if err != nil {
		res.Error = err.Error()
	}
	s.hub.Push(Frame{Type: FrameCommand, Payload: res})
	code := http.StatusOK
	if res.Error != "" {
		code = http.StatusOK // 命令业务失败:结果回前端展示,不按 4xx(与 TUI meta 行语义一致)
	}
	writeJSON(w, code, map[string]any{"ok": res.Error == "", "error": res.Error})
}

type confirmReq struct {
	ID string `json:"id"`
	OK bool   `json:"ok"`
}

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	var req confirmReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	s.confirm.Answer(req.ID, req.OK)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// StateView /api/state 快照(前端状态栏/首帧渲染)。
type StateView struct {
	Model    string         `json:"model"`
	Thinking string         `json:"thinking"`
	Sandbox  string         `json:"sandbox"`
	Approval string         `json:"approval,omitempty"` // M17:审批档位(open|smart|strict;未装配省略)
	Stats    sdk.UsageStats `json:"stats"`
	Session  *SessionV      `json:"session,omitempty"`
	Running  bool           `json:"running"`
	Version  string         `json:"version"`
}

// SessionV 会话视图(host-cwd-sessions 未装配时省略)。
type SessionV struct {
	ID   string `json:"id"`   // 空 = 主会话
	Name string `json:"name"` // 显示名(空 = 未命名)
	Path string `json:"path"`
	Key  string `json:"key"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	names := s.llm.Thinking().Names()
	lvl := int(s.llm.Thinking())
	if lvl < 0 || lvl >= len(names) {
		lvl = 0
	}
	approval := ""
	if s.ap != nil {
		approval = string(s.ap.Mode())
	}
	v := StateView{
		Model:    s.llm.Model(),
		Thinking: names[lvl],
		Sandbox:  string(s.sb.Mode()),
		Approval: approval,
		Running:  s.running.Load(),
		Version:  os.Getenv("GAH_VERSION"),
	}
	if s.us != nil {
		v.Stats = s.us.Stats()
	}
	if s.cs != nil {
		v.Session = &SessionV{ID: s.cs.CurrentSession(), Name: s.cs.SessionName(), Path: s.cs.Path(), Key: s.cs.Current()}
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.cs == nil {
		http.Error(w, "会话服务未装配(ctx.cwdSessions)", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		// 空列表归一为空数组(前端渲染不遇 null)
		list := s.cs.Sessions()
		if list == nil {
			list = []sdk.SessionInfo{}
		}
		writeJSON(w, http.StatusOK, list)
	case http.MethodPost:
		var req struct {
			Action string `json:"action"` // switch | new | fork | clone
			ID     string `json:"id"`     // switch 目标(空 = 主会话)
			Seq    uint64 `json:"seq"`    // fork 分支点(会话事件 seq)
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "坏请求体", http.StatusBadRequest)
			return
		}
		switch req.Action {
		case "switch":
			if err := s.cs.Open(req.ID); err != nil {
				http.Error(w, "切换失败: "+err.Error(), http.StatusBadRequest)
				return
			}
			if s.us != nil {
				s.us.Reset()
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": s.currentSessionV()})
		case "new":
			id, err := s.cs.New()
			if err != nil {
				http.Error(w, "新建失败: "+err.Error(), http.StatusBadRequest)
				return
			}
			if s.us != nil {
				s.us.Reset()
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "session": s.currentSessionV()})
		case "delete":
			if err := s.cs.Delete(req.ID); err != nil {
				http.Error(w, "删除失败: "+err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": s.currentSessionV()})
		case "fork", "clone":
			fk, ok := s.cs.(sdk.ForkableSessions)
			if !ok {
				http.Error(w, "会话服务不支持分支/克隆(ForkableSessions)", http.StatusNotImplemented)
				return
			}
			var id string
			var err error
			if req.Action == "fork" {
				id, err = fk.ForkAt(req.Seq)
			} else {
				id, err = fk.CloneCurrent()
			}
			if err != nil {
				http.Error(w, "分支失败: "+err.Error(), http.StatusBadRequest)
				return
			}
			if s.us != nil {
				s.us.Reset()
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "session": s.currentSessionV()})
		default:
			http.Error(w, "未知 action", http.StatusBadRequest)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) currentSessionV() *SessionV {
	return &SessionV{ID: s.cs.CurrentSession(), Name: s.cs.SessionName(), Path: s.cs.Path(), Key: s.cs.Current()}
}

// handleWorkspaces 工作区(项目)历史列表(左侧 Sidebar;按最近使用倒序)。
// cs 未装配 = 503(前端隐藏该节);空列表归一空数组。
func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	if s.cs == nil {
		http.Error(w, "会话服务未装配(ctx.cwdSessions)", http.StatusServiceUnavailable)
		return
	}
	list := s.cs.RecentProjects()
	if list == nil {
		list = []sdk.ProjectInfo{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleWorkspaceDelete 删除工作区(项目)使用记录(仅移除记录,不删文件夹)。
func (s *Server) handleWorkspaceDelete(w http.ResponseWriter, r *http.Request) {
	if s.cs == nil {
		http.Error(w, "会话服务未装配(ctx.cwdSessions)", http.StatusServiceUnavailable)
		return
	}
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "缺工作区 key", http.StatusBadRequest)
		return
	}
	if err := s.cs.UnrecordProject(key); err != nil {
		http.Error(w, "删除失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// CommandView 命令列表 DTO(原始 CommandSpec 含 func 字段,不可 JSON 序列化;
// 前端仅需 name/usage/desc 做 "/" 提示与用法展示)。
type CommandView struct {
	Name  string `json:"name"`
	Usage string `json:"usage"`
	Desc  string `json:"desc"`
}

func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	if s.cmds == nil {
		writeJSON(w, http.StatusOK, []CommandView{})
		return
	}
	out := make([]CommandView, 0, len(s.cmds.List()))
	for _, spec := range s.cmds.List() {
		out = append(out, CommandView{Name: spec.Name, Usage: spec.Usage, Desc: spec.Desc})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleControl 状态栏级控制(model/thinking/sandbox 切换;对齐 TUI /model、/thinking、/sandbox)。
// 核心交互纯 REST(槽位契约:不绕模板渲染);命令式路径仍经 ctx.commands(host 插件命令)。
func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model     string `json:"model"`
		Thinking  string `json:"thinking"`
		Sandbox   string `json:"sandbox"`
		Approval  string `json:"approval"`
		Workspace string `json:"workspace"`
		Cancel    bool   `json:"cancel"` // 取消运行中回合(经 ctx.turnControl;未装配 503)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if req.Cancel {
		if s.tc == nil {
			http.Error(w, "回合控制未装配(ctx.turnControl)", http.StatusServiceUnavailable)
			return
		}
		s.tc.Cancel()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if req.Model != "" {
		s.llm.SetModel(req.Model)
		// 持久化:模型随 providerfile 落盘(重启经 providerfile 链恢复;失败不阻断即时生效)
		_ = providerfile.UpdateModel(req.Model)
	}
	if req.Thinking != "" {
		lvl := sdk.ParseThinking(req.Thinking)
		if req.Thinking != lvl.String() {
			http.Error(w, "未知思考等级 off|low|medium|high", http.StatusBadRequest)
			return
		}
		s.llm.SetThinking(lvl)
		// 持久化偏好(重启恢复)
		p := loadPrefs()
		p.Thinking = lvl.String()
		savePrefs(p)
	}
	if req.Sandbox != "" {
		s.sb.SetMode(sdk.SandboxMode(req.Sandbox))
		// 持久化偏好(重启恢复)
		p := loadPrefs()
		p.Sandbox = req.Sandbox
		savePrefs(p)
	}
	if req.Approval != "" {
		if s.ap == nil {
			http.Error(w, "审批服务未装配(ctx.approval)", http.StatusBadRequest)
			return
		}
		s.ap.SetMode(sdk.ApprovalMode(req.Approval))
		// 持久化偏好(重启恢复)
		p := loadPrefs()
		p.Approval = req.Approval
		savePrefs(p)
	}
	if req.Workspace != "" {
		if s.cs == nil {
			http.Error(w, "会话服务未装配(ctx.cwdSessions)", http.StatusBadRequest)
			return
		}
		// workspace = 真实目录(dir 语义,对齐 TUI /workspace):SwitchDir 内部
		// os.Chdir + key 派生 + 新建空会话,工作区记录以真实 dir 落盘
		id, err := s.cs.SwitchDir(req.Workspace)
		if err != nil {
			http.Error(w, "切换失败: "+err.Error(), http.StatusBadRequest)
			return
		}
		if s.us != nil {
			s.us.Reset()
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "session": s.currentSessionV()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleTodo 任务面板数据端点(M8-T2 展示联动:宿主经工具查询读状态,零额外 seam)。
// 代理 todo 工具 list action(JSON 结果透传);todo 工具未装配 = 503(面板降级隐藏)。
func (s *Server) handleTodo(w http.ResponseWriter, r *http.Request) {
	if s.tools == nil {
		http.Error(w, "todo 工具未装配(ctx.tools)", http.StatusServiceUnavailable)
		return
	}
	res, err := s.tools.Execute(r.Context(), "todo", `{"action":"list"}`)
	if err != nil {
		http.Error(w, "todo 查询失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	if res.Error != "" {
		writeJSON(w, http.StatusOK, map[string]any{"error": res.Error})
		return
	}
	// list 结果为任务 View 数组(JSON 文本);透传供面板渲染
	var out any
	if json.Unmarshal([]byte(res.Content), &out) == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"raw": res.Content})
}

// handleBackup 整体备份端点(M18):GET = 列出备份;POST {action:backup|restore}
// backup: {dest?} 外部路径可选;restore: {name} 指定归档(先自动备份当前态,调用方前端已二次确认)。
// 未装配 ctx.backup(host-backup 插件) → 503 显式错误,不静默降级。
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if s.bk == nil {
		http.Error(w, "备份服务未装配(ctx.backup/host-backup)", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodGet {
		list := s.bk.List()
		if list == nil {
			list = []sdk.BackupInfo{} // 空目录 List 返回 nil → 序列化 null;契约给空数组(前端 length 安全)
		}
		writeJSON(w, http.StatusOK, list)
		return
	}
	var req struct {
		Action string `json:"action"`
		Dest   string `json:"dest"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	switch req.Action {
	case "backup":
		name, err := s.bk.Backup(req.Dest)
		if err != nil {
			http.Error(w, "备份失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name})
	case "restore":
		if req.Name == "" {
			http.Error(w, "缺 restore 归档名", http.StatusBadRequest)
			return
		}
		if err := s.bk.Restore(req.Name); err != nil {
			http.Error(w, "恢复失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restored": req.Name})
	default:
		http.Error(w, "未知 action backup|restore", http.StatusBadRequest)
	}
}

// —— UI 插件(M7.2) ——

// UIPlugin 一个已安装 UI 插件的聚合视图(/api/ui-plugins;前端加载器用)。
type UIPlugin struct {
	ID      string    `json:"id"`
	Version string    `json:"version"`
	Slots   []SlotDef `json:"slots"`
}

// SlotDef 槽位覆盖声明(前端动态导入 module 后 registerSlot)。
type SlotDef struct {
	Name     string `json:"name"` // v1:stream|input|statusbar|confirm;v2 扩展:settings-section/sidebar-action/extra-panel
	Priority int    `json:"priority"`
	Module   string `json:"module"` // 相对插件目录的产物入口(如 ./dist/plugin.js)
}

// handleUIPlugins 聚合全部已安装 UI 插件(每次读盘;安装/卸载即时生效,重载页面即换)。
func (s *Server) handleUIPlugins(w http.ResponseWriter, r *http.Request) {
	out := s.scanUIPlugins()
	if out == nil {
		out = []UIPlugin{}
	}
	writeJSON(w, http.StatusOK, out)
}

// scanUIPlugins 遍历 ui-plugins 目录读 manifest.json(坏 manifest 记日志跳过,不拖垮)。
func (s *Server) scanUIPlugins() []UIPlugin {
	dir := s.cfg.UIPluginsDir
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // 目录不存在 = 无插件
	}
	var out []UIPlugin
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name(), "manifest.json")
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var m struct {
			ID      string    `json:"id"`
			Version string    `json:"version"`
			Slots   []SlotDef `json:"slots"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			s.log.Warn("ui-plugin: manifest 解析失败跳过", "dir", e.Name(), "err", err)
			continue
		}
		if m.ID == "" {
			s.log.Warn("ui-plugin: manifest 缺 id,跳过", "dir", e.Name())
			continue
		}
		if m.Slots == nil {
			m.Slots = []SlotDef{}
		}
		out = append(out, UIPlugin{ID: m.ID, Version: m.Version, Slots: m.Slots})
	}
	return out
}

// uiPluginsHandler 静态托管 ui-plugins 目录(插件 vite 产物;仅本机/鉴权外静态资源)。
// 经 StripPrefix 将 /ui-plugins/<id>/... 映射到目录内 <id>/...(DirFS 根即 ui-plugins)。
func (s *Server) uiPluginsHandler() http.Handler {
	if s.cfg.UIPluginsDir == "" {
		return http.NotFoundHandler()
	}
	return http.StripPrefix("/ui-plugins/", http.FileServerFS(os.DirFS(s.cfg.UIPluginsDir)))
}

// —— 静态与鉴权 ——

// —— 附件上传与托管(附件一期):multipart 流式、大小/类型白名单、路径防穿越。 ——

const (
	maxAttachBytes = 20 << 20  // 单文件上限 20MB
	maxAttachTotal = 32 << 20  // 单请求总上限 32MB
	maxAttachParts = 8         // 单请求最多 8 个文件
)

// AttachmentView 上传成功返回视图(前端 chip/预览用;Path=本地路径供 input 引用)。
type AttachmentView struct {
	Name string `json:"name"`
	Path string `json:"path"`   // 本地绝对路径(input 提交时回传)
	URL  string `json:"url"`    // /attachments/<时间戳>/<名> 预览
	Size int64  `json:"size"`
}

// allowedAttachType 附件类型白名单:图片(视觉/预览)+ 文本/JSON/PDF(模型可经 file 读)。
func allowedAttachType(ct string) bool {
	base, _, _ := strings.Cut(ct, ";")
	base = strings.TrimSpace(base)
	switch {
	case strings.HasPrefix(base, "image/"):
		return true
	case strings.HasPrefix(base, "text/"):
		return true
	case base == "application/pdf", base == "application/json":
		return true
	}
	return false
}

// handleAttachments POST /api/attachments:multipart("file" 字段,可多个)上传,
// 落盘 $GAH_HOME/attachments/<时间戳目录>/<原始名>(重名追加数字后缀)。
func (s *Server) handleAttachments(w http.ResponseWriter, r *http.Request) {
	if s.cfg.AttachmentsDir == "" {
		http.Error(w, "附件目录未配置(ui-web-app 未装配)", http.StatusServiceUnavailable)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "非 multipart 请求", http.StatusBadRequest)
		return
	}
	dir := filepath.Join(s.cfg.AttachmentsDir, time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.log.Error("附件目录创建失败", "dir", dir, "err", err)
		http.Error(w, "附件目录不可写", http.StatusInternalServerError)
		return
	}
	var (
		out   []AttachmentView
		total int64
	)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "multipart 解析失败", http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		if len(out) >= maxAttachParts {
			http.Error(w, "附件数量超限(最多 8 个)", http.StatusRequestEntityTooLarge)
			return
		}
		// 原始名清洗(filepath.Base 防目录穿越)
		name := filepath.Base(part.FileName())
		if name == "." || name == ".." || name == "" {
			http.Error(w, "非法文件名", http.StatusBadRequest)
			return
		}
		ct := part.Header.Get("Content-Type")
		if !allowedAttachType(ct) {
			http.Error(w, "附件类型不允许: "+ct, http.StatusUnsupportedMediaType)
			return
		}
		// 重名追加数字后缀
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		dst := name
		for i := 2; ; i++ {
			if _, serr := os.Stat(filepath.Join(dir, dst)); os.IsNotExist(serr) {
				break
			}
			dst = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		f, err := os.OpenFile(filepath.Join(dir, dst), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			s.log.Error("附件写入失败", "err", err)
			http.Error(w, "附件写入失败", http.StatusInternalServerError)
			return
		}
		n, cerr := io.Copy(f, io.LimitReader(part, maxAttachBytes+1))
		f.Close()
		if cerr != nil {
			http.Error(w, "附件写入失败", http.StatusInternalServerError)
			return
		}
		if n > maxAttachBytes {
			_ = os.Remove(filepath.Join(dir, dst))
			http.Error(w, "附件超限(单文件 ≤20MB)", http.StatusRequestEntityTooLarge)
			return
		}
		total += n
		if total > maxAttachTotal {
			http.Error(w, "附件总量超限(≤32MB)", http.StatusRequestEntityTooLarge)
			return
		}
		rel := filepath.Base(dir) + "/" + dst
		out = append(out, AttachmentView{
			Name: name,
			Path: filepath.Join(dir, dst),
			URL:  "/attachments/" + rel,
			Size: n,
		})
	}
	if len(out) == 0 {
		http.Error(w, "无文件字段(field: file)", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "attachments": out})
}

// attachmentsHandler GET /attachments/{...} 附件静态托管(图片内联预览)。
// DirFS 根即附件目录;路径穿越由 http.FileServerFS 处理(标准库拒绝 .. 越根)。
// 未配置时显式 503(不静默)。
func (s *Server) attachmentsHandler() http.Handler {
	if s.cfg.AttachmentsDir == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "附件目录未配置", http.StatusServiceUnavailable)
		})
	}
	return http.StripPrefix("/attachments/", http.FileServerFS(os.DirFS(s.cfg.AttachmentsDir)))
}

// attachmentMimeByExt 图片扩展名 → MIME(视觉注入 data URI 用;未知回源类型)。
var attachmentMimeByExt = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp", ".svg": "image/svg+xml",
}

// attachmentList 将已校验的附件路径转为 sdk.Attachment(图片按扩展名标记视觉)。
// Rel=相对附件根(会话 jsonl 便携);Path=绝对路径(适配器读取视觉内容)。
func attachmentList(root string, paths []string) []sdk.Attachment {
	out := make([]sdk.Attachment, 0, len(paths))
	for _, p := range paths {
		ext := strings.ToLower(filepath.Ext(p))
		kind, mime := sdk.AttachmentFile, ""
		if m, ok := attachmentMimeByExt[ext]; ok {
			kind = sdk.AttachmentImage
			mime = m
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = p
		}
		out = append(out, sdk.Attachment{Kind: kind, Name: filepath.Base(p), MimeType: mime, Rel: rel, Path: p})
	}
	return out
}

// validAttachment 校验提交路径位于附件目录内且为文件(防注入任意路径/目录穿越)。
func (s *Server) validAttachment(p string) bool {
	root := filepath.Clean(s.cfg.AttachmentsDir)
	if root == "" {
		return false
	}
	cle := filepath.Clean(p)
	rel, err := filepath.Rel(root, cle)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	fi, err := os.Stat(cle)
	return err == nil && !fi.IsDir()
}

// staticHandler 静态托管:data.static_dir(开发态)优先,否则 embed dist。
func (s *Server) staticHandler() http.Handler {
	var sub fs.FS = distFS
	if dir := s.cfg.StaticDir; dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			sub = os.DirFS(dir)
		} else {
			s.log.Warn("web: static_dir 不可用,回退 embed dist", "dir", dir)
		}
	}
	// web/dist 目录直接作为根(embed 的 dist 子树)
	var root fs.FS
	if _, err := fs.Stat(sub, "index.html"); err == nil {
		root = sub
	} else {
		root, _ = fs.Sub(sub, "dist")
	}
	return http.FileServerFS(root)
}

// authMiddleware 可选鉴权:data.auth_token 非空时 /api/* 需匹配
// (Authorization: Bearer <token> 或 ?token=<token>);静态资源不鉴权(仅本机绑定)。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	tok := s.cfg.AuthToken
	if tok == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		given := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			given = strings.TrimPrefix(h, "Bearer ")
		} else if q := r.URL.Query().Get("token"); q != "" {
			given = q
		} else if c, err := r.Cookie("gah_token"); err == nil {
			given = c.Value // 多浏览器会话:前端登录后写 cookie,后续请求/WS 自动携带
		}
		if given != tok {
			http.Error(w, "未授权", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// —— 通用能力 REST 面(为前端增删改铺路;可选服务缺失显式 503/501) ——

// handleTools 工具清单(GET /api/tools):模型可见定义(name/desc/schema)。
func (s *Server) handleTools(w http.ResponseWriter, _ *http.Request) {
	if s.tools == nil {
		writeJSON(w, http.StatusOK, []sdk.ToolDefinition{})
		return
	}
	list := s.tools.List()
	if list == nil {
		list = []sdk.ToolDefinition{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleToolCall 执行工具(POST /api/tools/{name}):body 即工具参数(JSON 原样透传)。
func (s *Server) handleToolCall(w http.ResponseWriter, r *http.Request) {
	if s.tools == nil {
		http.Error(w, "工具服务未装配(ctx.tools)", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	if _, ok := s.tools.Get(name); !ok {
		http.Error(w, "工具未注册: "+name, http.StatusNotFound)
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "读取请求体失败", http.StatusBadRequest)
		return
	}
	res, err := s.tools.Execute(r.Context(), name, string(b))
	if err != nil {
		http.Error(w, "工具失败: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": res.Content, "error": res.Error})
}

// handleJobs 后台任务列表(GET /api/jobs)。
func (s *Server) handleJobs(w http.ResponseWriter, _ *http.Request) {
	if s.jobs == nil {
		http.Error(w, "后台任务服务未装配(ctx.jobs)", http.StatusServiceUnavailable)
		return
	}
	list := s.jobs.List()
	if list == nil {
		list = []sdk.Job{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleJobGet 单任务状态与输出(GET /api/jobs/{id})。
func (s *Server) handleJobGet(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		http.Error(w, "后台任务服务未装配(ctx.jobs)", http.StatusServiceUnavailable)
		return
	}
	job, ok := s.jobs.Output(r.PathValue("id"))
	if !ok {
		http.Error(w, "任务不存在", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// handleJobKill 终止任务(POST /api/jobs/{id}/kill)。
func (s *Server) handleJobKill(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		http.Error(w, "后台任务服务未装配(ctx.jobs)", http.StatusServiceUnavailable)
		return
	}
	if err := s.jobs.Kill(r.PathValue("id")); err != nil {
		http.Error(w, "终止失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleCommandRun 直接执行命令(POST /api/commands/{name}):{args: ["..."]}。
// 绕过 "/" 前缀 input 分发,前端增删改命令交互友好。
func (s *Server) handleCommandRun(w http.ResponseWriter, r *http.Request) {
	if s.cmds == nil {
		http.Error(w, "命令注册表未装配(ctx.commands)", http.StatusServiceUnavailable)
		return
	}
	name := r.PathValue("name")
	spec, ok := s.cmds.Get(name)
	if !ok {
		http.Error(w, "命令未注册: /"+name, http.StatusNotFound)
		return
	}
	var req struct {
		Args []string `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	out, err := spec.Run(req.Args)
	writeJSON(w, http.StatusOK, map[string]any{"raw": "/" + name + " " + strings.Join(req.Args, " "), "output": out, "error": errString(err)})
}

// handlePlugins 插件清单(GET /api/plugins)。
// manage 管理域单一事实源 = catalogue 声明(PluginInfo.Manage),web 不再硬编码名单:
// host(宿主运行,可卸载)| external(已外部化,勿启停)| scenario(场景专用,勿启)| web(常规可启用)。
// 优先级:loaded→host(运行态)> 声明 external/scenario(装载态)> web(默认)。
type pluginView struct {
	sdk.PluginInfo
	Manage string `json:"manage"`
}

func (s *Server) handlePlugins(w http.ResponseWriter, _ *http.Request) {
	if s.pm == nil {
		http.Error(w, "插件管理器未装配(ctx.pluginManager)", http.StatusServiceUnavailable)
		return
	}
	list := s.pm.List()
	if list == nil {
		list = []sdk.PluginInfo{}
	}
	out := make([]pluginView, 0, len(list))
	for _, p := range list {
		m := "web" // 常规(可启用)
		if p.State == "loaded" {
			m = "host"
		} else if p.Manage != "" {
			m = p.Manage // external | scenario(声明)
		}
		out = append(out, pluginView{PluginInfo: p, Manage: m})
	}
	writeJSON(w, http.StatusOK, out)
}

// handlePluginAction 加载/卸载插件(POST /api/plugins/{id}/load|unload)。
func (s *Server) handlePluginAction(w http.ResponseWriter, r *http.Request) {
	if s.pm == nil {
		http.Error(w, "插件管理器未装配(ctx.pluginManager)", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	var err error
	if strings.HasSuffix(r.URL.Path, "/unload") {
		err = s.pm.Unload(id)
	} else {
		err = s.pm.Load(id)
	}
	if err != nil {
		http.Error(w, "插件操作失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleModels 聚合模型列表(GET /api/models)。
// 多 provider 存在 → 每端点聚合(单条失败记 error 不整体失败);否则当前适配器列表。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("all") == "1" {
		if mp, ok := s.llm.(sdk.MultiProviderService); ok {
			list := mp.ListAllModels()
			if list == nil {
				list = []sdk.ProviderModelList{} // 无 provider 时 ListAllModels 为 nil → 契约给空数组(前端 length 安全)
			}
			writeJSON(w, http.StatusOK, map[string]any{"providers": list})
			return
		}
	}
	models, err := s.llm.ListModels()
	if err != nil {
		http.Error(w, "当前适配器不支持列举模型(可手动设置): "+err.Error(), http.StatusNotImplemented)
		return
	}
	if models == nil {
		models = []sdk.ModelInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// handleProviders provider 视图(GET /api/providers)。
func (s *Server) handleProviders(w http.ResponseWriter, _ *http.Request) {
	mp, ok := s.llm.(sdk.MultiProviderService)
	if !ok {
		http.Error(w, "多 provider 能力未实现(MultiProviderService)", http.StatusNotImplemented)
		return
	}
	list := mp.Providers()
	if list == nil {
		list = []sdk.ProviderProfile{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleProviderAdd 新增/更新 provider(POST /api/providers;同名 upsert,首个自动激活)。
func (s *Server) handleProviderAdd(w http.ResponseWriter, r *http.Request) {
	mp, ok := s.llm.(sdk.MultiProviderService)
	if !ok {
		http.Error(w, "多 provider 能力未实现(MultiProviderService)", http.StatusNotImplemented)
		return
	}
	var req struct {
		Name    string `json:"name"`
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		http.Error(w, "name 必填", http.StatusBadRequest)
		return
	}
	if err := mp.AddProvider(req.Name, req.BaseURL, req.APIKey, req.Model); err != nil {
		http.Error(w, "新增失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleProviderUse 切换活跃 provider(POST /api/providers/{name}/use)。
func (s *Server) handleProviderUse(w http.ResponseWriter, r *http.Request) {
	mp, ok := s.llm.(sdk.MultiProviderService)
	if !ok {
		http.Error(w, "多 provider 能力未实现(MultiProviderService)", http.StatusNotImplemented)
		return
	}
	if err := mp.SetActiveProvider(r.PathValue("name")); err != nil {
		http.Error(w, "切换失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleProviderDelete 删除 provider(DELETE /api/providers/{name})。
// 当前接口无 Remove(M12 决策:单条删除不做,upsert 覆盖/全清走 unset/clear)→ 显式 501。
func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.llm.(sdk.MultiProviderService); !ok {
		http.Error(w, "多 provider 能力未实现(MultiProviderService)", http.StatusNotImplemented)
		return
	}
	http.Error(w, "删除暂不支持: upsert 覆盖即可(参考 M12 范围决策)", http.StatusNotImplemented)
}

// handleSessionRename 会话改名(POST /api/sessions/rename)。
func (s *Server) handleSessionRename(w http.ResponseWriter, r *http.Request) {
	if s.cs == nil {
		http.Error(w, "会话服务未装配(ctx.cwdSessions)", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if req.Name == "-" {
		req.Name = "" // 清除名(对齐 TUI /name -)
	}
	if err := s.cs.Rename(req.Name); err != nil {
		http.Error(w, "改名失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": s.currentSessionV()})
}

// handleSessionExport 会话导出(GET /api/sessions/{id}/export):原始 jsonl 下载落盘。
// id 空 = 主会话;匹配失败显式 404(不静默)。
func (s *Server) handleSessionExport(w http.ResponseWriter, r *http.Request) {
	if s.cs == nil {
		http.Error(w, "会话服务未装配(ctx.cwdSessions)", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	list := s.cs.Sessions()
	var path string
	for _, si := range list {
		if si.ID == id {
			path = si.Path
			break
		}
	}
	if path == "" {
		http.Error(w, "会话不存在: "+id, http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		s.log.Error("导出读文件失败", "path", path, "err", err)
		http.Error(w, "会话文件不可读", http.StatusInternalServerError)
		return
	}
	fname := "session-main.jsonl"
	if id != "" {
		fname = "session-" + id + ".jsonl"
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleReload 指令文件热更(POST /api/reload):重读全局/多级/附加指令,失败保留旧值。
func (s *Server) handleReload(w http.ResponseWriter, _ *http.Request) {
	r, ok := s.sp.(sdk.ReloadableInstructions)
	if !ok {
		http.Error(w, "指令热更能力未实现(ReloadableInstructions)", http.StatusNotImplemented)
		return
	}
	if err := r.ReloadInstructions(); err != nil {
		http.Error(w, "重载失败(保留旧值): "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleShutdown 优雅停机(POST /api/shutdown):触发宿主 system/shutdown →
// cmd/gah 订阅退出 → DisposeAll 回收全部插件资源与外部进程。
// 桌面壳/运维的跨平台统一停机通道(Windows 无 SIGTERM;硬杀会跳过 dispose)。
// 顺序:先回 200 并 Flush 再触发回调——确保响应离开后才开始关停(http.Shutdown 会等待本 handler 返回)。
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if s.OnShutdown == nil {
		http.Error(w, "停机回调未装配(ui-web-app 未绑定宿主)", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true,"shutting_down":true}` + "\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	s.OnShutdown()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// handleQuestion POST /api/question:结构化提问作答回传(前端弹层;未知 id 幂等忽略)。
func (s *Server) handleQuestion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID     string   `json:"id"`
		Values []string `json:"values"`
		Text   string   `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.ID == "" {
		http.Error(w, "缺少弹层 id", http.StatusBadRequest)
		return
	}
	s.question.Answer(body.ID, sdk.QuestionAnswer{Values: body.Values, Text: body.Text})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleIMChannels GET /api/im/channels:IM 通道状态(wechat/qq 登录态/授权/诊断;
// P3 三端融合 Web 面板)。未装配(无 IM 插件)显式 503,不静默空。
func (s *Server) handleIMChannels(w http.ResponseWriter, r *http.Request) {
	if s.imc == nil {
		http.Error(w, "IM 通道服务未装配(无 ui-im-* 插件)", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, s.imc.Status())
}
