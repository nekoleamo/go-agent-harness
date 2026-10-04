// HTTP/SSE 服务:能力全在宿主,浏览器只订阅事件流(SSE 下行)+ REST 上行。
// 路由:
//
//	GET  /             静态前端(embed web/dist;data.static_dir 覆写=开发态 Vite HMR)
//	GET  /api/events   SSE 流(?after=<seq> 断线续传;首连回放尾部窗口 + baseline 首帧)
//	GET  /api/session/events?before=<seq>&limit=<n> 会话事件分页(长会话上滚加载更早历史)
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
//	GET/POST/PATCH/DELETE /api/schedules… 定时计划(NOND-W4;POST /{id}/run 立即触发一次)
//	GET/POST /api/mcp MCP server 配置(NOND-M1 第 3 步:列表+状态 / 保存(=写 mcp.yaml)+重启插件重载)
//	GET /api/plugins … 插件清单/加载/卸载;GET /api/models 聚合模型列表
//	GET/POST /api/providers … 多 provider;POST /api/reload 指令热更
//	GET/POST /api/roles + GET/PATCH/DELETE /api/roles/{id} + PUT /api/roles/{id}/agents +
//	POST /api/roles/{id}/rename|use 角色面板(第七十九批;未装配 ctx.roles → 503,面板整段隐藏);
//	POST /api/skills + GET/DELETE /api/skills/{name}?role= + POST /api/skills/{name}/relocate 技能库
//	(共享库 / 角色私有;relocate = 改名/跨库移动,第八十四批);
//	GET /api/trash + POST /api/trash/restore 回收站(第八十三批;删除的角色/技能可恢复);
//	GET /api/rolepack/{id} 下载角色包(zip) + POST /api/rolepack?as=&overwrite=1 导入角色包
//	GET /api/skillpack/{name} 下载**共享**技能包(zip) + POST /api/skillpack?as=&overwrite=1 导入
//	  (角色私有技能随角色包走,见 /api/rolepack)
//	(multipart "file";第九十三批;独立前缀 —— 角色 id 可以是 export/import,挂 /api/roles 下会被通配吃);
//	GET/PUT /api/instructions 全局指令 $GAH_HOME/AGENTS.md(第八十一批;PUT 后连带
//	ReloadInstructions,重载失败 → 200 + warning)
//	POST /api/shutdown 优雅停机(触发宿主 system/shutdown → DisposeAll;桌面壳/跨平台统一通道)
//	POST /api/attachments 附件上传(multipart "file";流式/大小 20MB/类型白名单;落盘
//	$GAH_HOME/attachments/<时间戳>/;GET /attachments/{...} 静态预览(同受鉴权门保护))
//	POST /api/auth     token 引导通道:{token} 或 Bearer → 下发 gah_token cookie(204;见 bootstrap.go)
//
// 安全:默认绑定 127.0.0.1:2233;全部请求经 guardMiddleware 做 Host 白名单 + 同源(Origin/Referer)
// + 请求体类型校验(见 guard.go);data.auth_token 非空时**全表面**需凭据(Authorization: Bearer /
// gah_token cookie):/api/* 缺凭据 401,其它路径(导航/静态/附件/UI 插件产物)返回引导页(见 bootstrap.go)。
// 可选服务(Ctx 可选注入)未装配时对应端点返回 503/501 显式错误,不静默降级。
package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/internal/providerfile"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/internal/sessionevents"
	"github.com/nekoleamo/go-agent-harness/internal/sessionhtml"
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
	ap       sdk.ApprovalService       // 可选(审批档位 M17:未装配时 state 省略/control 400)
	bk       sdk.BackupService         // 可选(整体备份 M18:未装配时 /api/backup 503)
	us       sdk.UsageStatsService     // 可选
	cs       sdk.CwdSessions           // 可选
	sdir     sdk.SessionDir            // 可选(按会话 id 取日志;未装配 = 只支持主会话)
	ss       sdk.SessionSummaryService // 可选(F3 会话概述;未装配则 summary 端点 503)
	cmds     sdk.CommandRegistry       // 可选(未装配 = / 命令不可用)
	tools    sdk.ToolRegistry          // 可选(工具清单/调用/todo 面板)
	jobs     sdk.JobService            // 可选(后台任务)
	sched    sdk.ScheduleService       // 可选(定时计划 NOND-W4;未装配 → /api/schedules 503)
	notices  sdk.NoticeService         // 可选(提示通道 NOND-N1;未装配 → /api/notices 503)
	extp     sdk.ExternalPlugins       // 可选(外部插件控制面 NOND-M1;未装配 = 保存 MCP 配置后需重启)
	pm       sdk.PluginManager         // 可选(插件启停)
	// rejectedUI 被完整性闸拦下的 UI 插件(id → 原因);供 /api/ui-plugins 一并下发,
	// 让界面能区分「没装这个插件」与「装了但被完整性闸拦下」。写路径是 HTTP handler,
	// 故用 mutex 而非裸 map。
	rejMu      sync.Mutex
	rejectedUI map[string]string
	sp         sdk.SystemPromptService // 可选(/reload 指令热更)
	tc         sdk.TurnControl         // 可选(回合取消 /api/control cancel;未装配 = 503)
	doc        sdk.DocService          // 可选(文档预览 D1:未装配 → /api/doc/* 503;懒解析见 docSvc)
	roles      sdk.RoleService         // 可选(角色面板 1b:未装配 → /api/roles 503;懒解析见 roleSvc)
	roleSkills sdk.SkillsService       // 可选(技能库/新建技能;懒解析同上)
	memory     sdk.MemoryService       // 可选(记忆治理面板:未装配 → /api/memory 503;懒解析见 memorySvc;与角色服务共用 roleMu)
	ctx        sdk.Ctx                 // 宿主上下文(懒解析可选服务,避免装配顺序依赖)
	docMu      sync.Mutex              // doc 懒解析互斥(并发首请求防数据竞争)
	roleMu     sync.Mutex              // 角色/技能懒解析互斥(同上)

	running      atomic.Bool             // 主会话(键空)占用;其余在 runBySession
	runMu        sync.Mutex              // 守护 runBySession
	runBySession map[string]*atomic.Bool // 会话键 → 回合占用(懒建)
	lifeMu       sync.Mutex              // 守护 ln/http/closed:Listen/Start(插件)与 Shutdown(卸载)可并发
	ln           net.Listener
	http         *http.Server
	closed       bool         // 已 Shutdown:Start 若尚未发布 http 则放弃监听(不留孤儿)
	unsubStatus  sdk.Disposer // agent/status 订阅撤销(驱动 running 复位)

	// OnReady 监听成功回调(参数=访问 URL;监听失败不触发,插件层据此自动打开浏览器)。
	OnReady func(url string)

	// OnShutdown 停机触发回调(POST /api/shutdown;由插件层绑定宿主 system/shutdown 事件,
	// 触发 cmd/gah 退出 → DisposeAll 回收插件/外部进程。nil = 未装配 → 端点 503)。
	OnShutdown func()
}

// New 构造服务;依赖经插件装配层注入(Required 之外的可选注入失败即忽略)。
func New(cfg Config, hub *EventHub, confirm *ConfirmService, log *slog.Logger) *Server {
	return &Server{cfg: cfg, hub: hub, confirm: confirm, question: NewQuestionService(hub), log: log, runBySession: map[string]*atomic.Bool{}}
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
	_ = c.Inject("ctx.sessionDir", &s.sdir)   // 可选:未装配则 ?session= 非主会话 → 501/503(显式)
	_ = c.Inject("ctx.sessionSummary", &s.ss) // 可选:未装配则 /api/sessions/summary 503
	_ = c.Inject("ctx.commands", &s.cmds)
	_ = c.Inject("ctx.tools", &s.tools)
	_ = c.Inject("ctx.jobs", &s.jobs)
	_ = c.Inject("ctx.schedule", &s.sched)
	_ = c.Inject("ctx.notices", &s.notices)
	_ = c.Inject("ctx.extplugins", &s.extp)
	_ = c.Inject("ctx.pluginManager", &s.pm)
	_ = c.Inject("ctx.systemPrompt", &s.sp)
	_ = c.Inject("ctx.turnControl", &s.tc)
	s.ctx = c
	// ctx.doc 采用**懒解析**(见 docSvc):ui-web-app 与 host-docview 无拓扑依赖,
	// 启动顺序不定 —— 启动期一次性 Inject 会恒为 nil(policy-guard ctx.confirm 同款时序坑)。
	_ = c.Inject("ctx.doc", &s.doc)
	// 角色/技能同款懒解析(见 roles.go 的 roleService):这里先试一次,host-roles/host-skills
	// 已在位时省掉首个请求的探测;不在位(未启用/后启用)就留空,由懒解析在首次请求时补上。
	_ = c.Inject("ctx.roles", &s.roles)
	_ = c.Inject("ctx.skills", &s.roleSkills)
	// running 状态:随 agent/status 事件驱动(回合开始 running,结束 idle)。
	// 多会话并行后事件不再归属某个窗口,所以**只用来驱动主会话**那把闸;
	// 各会话的占用由 handleInput 的 CAS 直接置位/复位(权威),不靠事件反推。
	unsub := c.Subscribe(sdk.EventAgentStatus, func(_ context.Context, ev *sdk.Event) error {
		s.running.Store(ev.Payload == "running")
		return nil
	})
	s.unsubStatus = unsub
	return nil
}

// Listen 先实际占用监听端口(幂等;失败显式返回错误)。
// 存在的意义:**启动期同步失败**(fail-fast)—— 插件在返回成功前就能把「端口被占用」
// 变成启动失败,而不是把错误丢进后台 goroutine 只记日志:那样进程会挂着不动、没有可服务
// 端口、也接不到 /api/shutdown(只能被 kill,外部插件子进程一并残留)。
func (s *Server) Listen() error {
	s.lifeMu.Lock()
	if s.ln != nil || s.closed { // 已监听 / 已停机:幂等
		s.lifeMu.Unlock()
		return nil
	}
	addr := s.cfg.Addr
	if addr == "" {
		addr = "127.0.0.1:2233"
		s.cfg.Addr = addr
	}
	s.lifeMu.Unlock()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// 真实世界里监听失败几乎都是端口被占,且多半不是「本机没空闲端口」而是**上一个 gah 实例
		// 还活着**(后台常驻的 gah web,或旧版二进制被强杀后留下的残留)。底层文案跨平台不可读
		// (linux: address already in use / windows: Only one usage ...),这里补出可操作的两条路。
		return fmt.Errorf("web ui 监听失败: %w(该端口可能被残留的 gah 实例占用:换端口用 GAH_WEB_ADDR,或先清理残留实例)", err)
	}
	s.lifeMu.Lock()
	if s.closed || s.ln != nil { // 并发 Listen/Shutdown:放弃本次句柄
		s.lifeMu.Unlock()
		_ = ln.Close()
		return nil
	}
	s.ln = ln
	s.lifeMu.Unlock()
	return nil
}

// Start 启动服务(阻塞;外部 goroutine 调用,Shutdown 停止)。
// 未 Listen 时自行监听(兼容原有调用方式);失败时**不打 listening 日志**(调用方据错误
// 决定退出还是降级),成功后回调 OnReady(插件层据此自动打开浏览器)。
// 与 Shutdown 并发安全:先建 http.Server 再发布(锁内),Shutdown 已发生则放弃监听
// —— 否则会在卸载后留下无句柄的孤儿监听(端口泄漏)。
func (s *Server) Start() error {
	if err := s.Listen(); err != nil {
		return err
	}
	s.lifeMu.Lock()
	ln := s.ln
	s.lifeMu.Unlock()
	if ln == nil { // 已 Shutdown(无监听)
		return nil
	}
	hs := &http.Server{
		Handler:           s.protected(),
		ReadHeaderTimeout: 10 * time.Second, // 慢头攻击兜底
		IdleTimeout:       120 * time.Second,
		// 不设 WriteTimeout:SSE 长连接/慢客户端会被写超时截断
	}
	s.lifeMu.Lock()
	if s.closed {
		s.lifeMu.Unlock()
		_ = ln.Close()
		return nil
	}
	s.http = hs
	s.lifeMu.Unlock()
	if s.OnReady != nil {
		s.OnReady("http://" + s.cfg.Addr)
	}
	s.log.Info("web ui listening", "addr", s.cfg.Addr)
	return hs.Serve(ln)
}

// Handler 导出完整服务栈(护栏 + 鉴权 + 路由);外部挂载/httptest 直挂即受保护。
func (s *Server) Handler() http.Handler {
	return s.protected()
}

// protected 生产与导出路径的中间件栈:安全头/来源护栏 → token 鉴权 → 路由。
// 包内测试可直挂 handler()(不套护栏),护栏行为见 guard_test.go。
func (s *Server) protected() http.Handler {
	return s.guardMiddleware(s.authMiddleware(s.handler()))
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
	// 会话事件分页(S-P1-2 长会话上滚加载更早历史;窗口回合对齐,见 web/paging.go)
	mux.HandleFunc("GET /api/session/events", s.handleSessionEvents)
	mux.HandleFunc("POST /api/sessions", s.handleSessions)
	mux.HandleFunc("GET /api/sessions/{id}/export", s.handleSessionExport)
	// 主会话导出(id 空):/api/sessions//export 这种空段路径不匹配任何模式(实测 404),
	// 所以主会话另走一条无 id 路由 —— 侧栏「主会话」行的导出走这里。
	mux.HandleFunc("GET /api/sessions/export", s.handleSessionExport)
	mux.HandleFunc("POST /api/sessions/rename", s.handleSessionRename)
	mux.HandleFunc("POST /api/sessions/summary", s.handleSessionSummary)
	mux.HandleFunc("GET /api/workspaces", s.handleWorkspaces)
	mux.HandleFunc("DELETE /api/workspaces/{key}", s.handleWorkspaceDelete)
	mux.HandleFunc("GET /api/tools", s.handleTools)
	mux.HandleFunc("GET /api/tools/conflicts", s.handleToolConflicts)
	mux.HandleFunc("POST /api/tools/{name}", s.handleToolCall)
	mux.HandleFunc("GET /api/jobs", s.handleJobs)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleJobGet)
	mux.HandleFunc("POST /api/jobs/{id}/kill", s.handleJobKill)
	mux.HandleFunc("GET /api/mcp", s.handleMCPList)
	mux.HandleFunc("POST /api/mcp", s.handleMCPSave)
	mux.HandleFunc("GET /api/schedules", s.handleSchedules)
	mux.HandleFunc("GET /api/notices", s.handleNotices)
	mux.HandleFunc("POST /api/schedules", s.handleScheduleAdd)
	mux.HandleFunc("PATCH /api/schedules/{id}", s.handleScheduleUpdate)
	mux.HandleFunc("DELETE /api/schedules/{id}", s.handleScheduleDelete)
	mux.HandleFunc("POST /api/schedules/{id}/run", s.handleScheduleRun)
	mux.HandleFunc("POST /api/commands/{name}", s.handleCommandRun)
	mux.HandleFunc("POST /api/commands/{name}/options", s.handleCommandOptions)
	mux.HandleFunc("GET /api/plugins", s.handlePlugins)
	mux.HandleFunc("POST /api/plugins/{id}/load", s.handlePluginAction)
	mux.HandleFunc("POST /api/plugins/{id}/unload", s.handlePluginAction)
	// 插件安装/卸载/信任(2026-10-03):把 CLI 的安装内核接到面板上。
	// 路由不冲突:既有的是 3 段(/api/plugins/{id}/load),新增是 2 段字面量。
	mux.HandleFunc("GET /api/plugins/install", s.handlePluginInstallList)
	mux.HandleFunc("GET /api/plugins/update-check", s.handlePluginUpdateCheck)
	mux.HandleFunc("POST /api/plugins/install", s.handlePluginInstall)
	mux.HandleFunc("POST /api/plugins/install/enable", s.handlePluginEnable)
	mux.HandleFunc("POST /api/plugins/install/disable", s.handlePluginDisable)
	mux.HandleFunc("POST /api/plugins/uninstall", s.handlePluginUninstall)
	mux.HandleFunc("POST /api/plugins/trust", s.handlePluginTrust)
	mux.HandleFunc("POST /api/plugins/untrust", s.handlePluginUntrust)
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
	mux.HandleFunc("GET /api/ui-plugins/state", s.handleUIPluginState)
	mux.HandleFunc("POST /api/ui-plugins/disable", s.handleUIDisable)
	mux.HandleFunc("POST /api/ui-plugins/enable", s.handleUIEnable)
	mux.HandleFunc("GET /api/todo", s.handleTodo)
	mux.HandleFunc("GET /api/backup", s.handleBackup)
	mux.HandleFunc("POST /api/backup", s.handleBackup)
	// 角色面板(第七十九批 1b):未装配 ctx.roles → 503(前端隐藏「角色」段)
	mux.HandleFunc("GET /api/roles", s.handleRoles)
	mux.HandleFunc("POST /api/roles", s.handleRoles)
	mux.HandleFunc("GET /api/roles/{id}", s.handleRoleOne)
	mux.HandleFunc("PATCH /api/roles/{id}", s.handleRoleOne)
	mux.HandleFunc("DELETE /api/roles/{id}", s.handleRoleOne)
	mux.HandleFunc("PUT /api/roles/{id}/agents", s.handleRoleAgents)
	mux.HandleFunc("POST /api/roles/{id}/rename", s.handleRoleRename)
	mux.HandleFunc("POST /api/roles/{id}/use", s.handleRoleUse)
	mux.HandleFunc("GET /api/instructions", s.handleInstructions)
	mux.HandleFunc("PUT /api/instructions", s.handleInstructionsSave)
	// 记忆治理面板(与 /memory 命令同一份实现;未装配 ctx.memory → 503,面板整段隐藏)
	mux.HandleFunc("GET /api/memory", s.handleMemory)
	mux.HandleFunc("POST /api/memory", s.handleMemory)
	mux.HandleFunc("POST /api/skills", s.handleSkills)
	mux.HandleFunc("GET /api/skills/{name}", s.handleSkillOne)
	mux.HandleFunc("DELETE /api/skills/{name}", s.handleSkillOne)
	// 技能改名/跨库移动(第八十四批;单段通配路由不吃多段路径,与上面两条无冲突)
	mux.HandleFunc("POST /api/skills/{name}/relocate", s.handleSkillRelocate)
	// 回收站(第八十三批):独立前缀,避免与 /api/roles/{id}、/api/skills/{name} 互吃
	mux.HandleFunc("GET /api/trash", s.handleTrash)
	mux.HandleFunc("POST /api/trash/restore", s.handleTrashRestore)
	// 角色包(第九十三批):独立前缀(理由见 web/rolepack.go 顶部)
	mux.HandleFunc("GET /api/rolepack/{id}", s.handleRolePackGet)
	mux.HandleFunc("POST /api/rolepack", s.handleRolePackPost)
	// 技能包(第一百零六批):**独立前缀**(技能名里就有 import/export,挂在
	// /api/skills/{name}/... 会被通配吃掉 —— 与 /api/rolepack 同一课)。
	mux.HandleFunc("GET /api/skillpack/{name}", s.handleSkillPackGet)
	mux.HandleFunc("POST /api/skillpack", s.handleSkillPackPost)
	// 文档预览(D1):无条件注册,服务缺失时 503(前端据 503 隐藏入口)
	mux.HandleFunc("GET /api/doc/preview", s.handleDocPreview)
	mux.HandleFunc("GET /api/doc/raw", s.handleDocRaw)
	mux.HandleFunc("GET /api/doc/asset", s.handleDocAsset)
	mux.HandleFunc("GET /api/doc/raster", s.handleDocRaster)
	mux.HandleFunc("GET /api/doc/tree", s.handleDocTree)
	mux.HandleFunc("GET /api/doc/html", s.handleDocHTML)
	mux.HandleFunc("POST /api/doc/render", s.handleDocRender)
	mux.HandleFunc("POST /api/question", s.handleQuestion)
	mux.HandleFunc(authPath, s.handleAuth) // token 引导通道(唯一豁免鉴权门;护栏照常;非 POST 由处理器 405)
	mux.Handle("/ui-plugins/", s.uiPluginsHandler())
	mux.Handle("/attachments/", s.attachmentsHandler())
	mux.Handle("/", s.staticHandler())
	return mux
}

// Shutdown 停止服务并撤销状态订阅。可在 Listen/Start 之前/并发调用:此时仅标记 closed,
// 已 Listen 未 Serve 的监听句柄就地关闭(不留孤儿端口)。
func (s *Server) Shutdown() {
	if s.unsubStatus != nil {
		s.unsubStatus()
	}
	s.lifeMu.Lock()
	s.closed = true
	hs, ln := s.http, s.ln
	s.lifeMu.Unlock()
	if hs == nil {
		if ln != nil { // Listen 过但还没 Serve:关掉,否则端口一直占着
			_ = ln.Close()
		}
		return // 尚未开始服务:Start 侧自检 closed 后放弃(不会留孤儿)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = hs.Shutdown(ctx)
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
// 重放快照与订阅建立之间的广播 gap)→ 历史重放 → 实时转发。sink 返回错误
// (载体写失败=客户端断连)即退;stop 为请求上下文取消。
//
// 历史重放分两种口径(S-P1-2):
//   - after == 0(全新连接,含页面刷新/切会话):只回放**尾部窗口**(回合对齐),
//     并以 FrameBaseline 作首帧告知窗口边界与「更早历史是否还有」。全量重放会让
//     首帧延迟随会话长度线性增长(万帧级会话每次打开重放万帧);
//   - after > 0(断线续传):回放差集。这是真有缺口的场景,必须补齐不能截断。
//
// seen 游标去重保证「重放期间已入实时流的帧」不双发。
// consumeStream 消费事件流。sid 非空时只收该会话的会话帧(非会话帧仍全量),
// 且重放读的是**那个会话自己的**日志(否则历史与实时流会来自两个会话)。
func (s *Server) consumeStream(after uint64, sink func(Frame) error, stop <-chan struct{}, sid string) {
	sc, err := s.scopeOf(sid)
	if err != nil {
		// 拿不到该会话的日志:宁可立刻断流(前端按 after 重连),不接一条混着主会话的流。
		return
	}
	defer sc.Release()
	if sc.ID != "" {
		s.hub.EnsureSession(sc.ID)
		defer s.hub.UnbindSession(sc.ID)
	}
	ch, unsub := s.hub.Stream(sc.ID)
	defer unsub()
	seen := after // 已消费会话游标(会话帧按 Seq 全局递增;非会话帧 ID=0 不参与去重)
	// 未决审批弹层补推(只推给本连接):confirm 帧是实时广播、不落账本,而审批现在默认
	// **不限时地等** —— 用户刷新/重开页面期间推出去的弹层就丢了,不补推他会永远等下去。
	// 其它已连着的前端早就收到过同一帧,所以只推本连接,不用广播。
	if s.confirm != nil {
		for _, p := range s.confirm.Pending() {
			if err := sink(Frame{Type: FrameConfirm, Payload: p}); err != nil {
				return
			}
		}
	}
	var replay []Frame
	if after == 0 {
		frames, base := s.hub.ReplayTail(sc.Log)
		if err := sink(Frame{Type: FrameBaseline, Payload: base}); err != nil {
			return
		}
		replay = frames
	} else {
		replay = s.hub.ReplayAfter(sc.Log, after)
	}
	for _, f := range replay {
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
		case f, ok := <-ch:
			if !ok {
				return // 流被 hub 摘除(丢帧不可静默):断开让客户端按 after 重连重放
			}
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
	s.consumeStream(s.afterOf(r), sse, r.Context().Done(), r.URL.Query().Get("session"))
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
	// 读泵 + ping 保活:客户端静默消失时(即使无事件可写)也能及时回收 goroutine/fd/订阅。
	stop := make(chan struct{})
	go func() {
		conn.drain()
		close(stop)
	}()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if err := conn.WritePing(); err != nil {
					return
				}
			case <-stop:
				return
			}
		}
	}()
	// hijack 后 r.Context() 已取消(服务端接管连接):停止信号由读泵提供,
	// 推送写失败同样驱动 consumeStream 退出。
	s.consumeStream(s.afterOf(r), wsc, stop, r.URL.Query().Get("session"))
}

// —— REST ——

type inputReq struct {
	Content string `json:"content"`
	// Session 目标会话 id(缺省 = 当前主会话)。非当前会话需 agent-loop 支持
	// sdk.SessionRunner(方案 B-1),否则 409 —— 不静默写进当前会话(见 web/session_scope.go)。
	Session string `json:"session,omitempty"`
	// Attachments 附件标识(两种写法均接受):
	//   ① `/attachments/<rel>`(`/api/attachments` 返回的 url,前端默认用这个);
	//   ② 附件目录内的绝对路径(该接口返回的 path,供旧客户端/脚本兼容)。
	Attachments []string `json:"attachments"`
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
	// 写侧闸门:向非当前会话提交需要 per-session 回合能力(未落地则显式拒收)。
	if !s.guardWrite(w, req.Session) {
		return
	}
	// 会话键(空 = 主会话);回合占用、串行落账、取消都按它分桶。
	skey := s.sessionKey(req.Session)
	// 附件校验与解析(必须落在附件根内且为已存在文件;防注入任意路径)
	resolved := make([]string, 0, len(req.Attachments))
	for _, a := range req.Attachments {
		p, aerr := s.resolveAttachment(a)
		if aerr != nil {
			s.log.Warn("web: 附件不可用", "input", a, "err", aerr)
			http.Error(w, "附件不可用: "+a+"("+aerr.Error()+")", http.StatusBadRequest)
			return
		}
		resolved = append(resolved, p)
	}
	// 回合进行中:普通消息改**注入当前回合**(转向:模型下一次请求就会看到,对齐 TUI);
	// 命令路径(/开头)保持拒收(命令即时执行,不经回合),未装配转向能力时同样回落 409。
	// **带附件也不走转向**:转向通道只带文本(见 host-agent-loop 的 Steer),放进去等于
	// 把附件静默丢掉 —— 宁可 409 让用户等回合结束,也不假装收下了。
	// 权威占用在下方 CAS。
	if s.runningFor(skey).Load() {
		if !strings.HasPrefix(content, "/") && len(resolved) == 0 && s.steer(content) {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "accepted": "steer"})
			return
		}
		http.Error(w, "回合进行中,等待完成或取消后再提交", http.StatusConflict)
		return
	}
	// 附件引用文本注入:模型可见附件路径(文本类可经 file 工具读取;图片另走 A2 结构化视觉)
	atts := attachmentList(s.cfg.AttachmentsDir, resolved)
	if len(atts) > 0 {
		var b strings.Builder
		b.WriteString(content)
		b.WriteString("\n\n[附件]\n")
		for i, a := range atts {
			// 同时给「绝对路径」与「附件标识」两种写法:只给相对附件根的形式时,模型看到的是
			// 一个没有根的路径 —— 真机上出现过它把目录名当文件名去 doc_open
			// (「docview: 文件不存在: 20260916-213605」)。
			b.WriteString(fmt.Sprintf("%d. %s(文件路径 %s;附件标识 /attachments/%s)\n", i+1, a.Name, a.Path, filepath.ToSlash(a.Rel)))
		}
		content = b.String()
	}
	if strings.HasPrefix(content, "/") {
		s.runCommand(content, w)
		return
	}
	// CAS 原子占用:Load+Store 分离时并发双击可同时通过快速检查,跑出两个回合
	// (两个 goroutine 共享同一 Loop 的回合状态)。
	busy := s.runningFor(skey)
	if !busy.CompareAndSwap(false, true) {
		// 竞态:另一请求刚起回合 → 同样按转向处理(不静默丢用户输入);
		// 带附件同样不走转向(理由见上面的闸门)。
		if len(resolved) == 0 && s.steer(content) {
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "accepted": "steer"})
			return
		}
		http.Error(w, "回合进行中,等待完成或取消后再提交", http.StatusConflict)
		return
	}
	go func() {
		defer busy.Store(false)
		if skey == "" {
			s.running.Store(true) // 主会话:agent/status 订阅者也据此复位
			defer s.running.Store(false)
		}
		var err error
		switch {
		case skey != "" && len(atts) > 0:
			// per-session + 附件:RunInSession 没有附件变体(附件要拼进会话历史)。
			// 显式报错而不是把附件丢掉跑一个"看不见图"的回合。
			err = fmt.Errorf("向指定会话提交暂不支持附件(会话 %s):请去掉附件,或切到当前会话再发", skey)
		case skey != "":
			sr, ok := s.loop.(sdk.SessionRunner)
			if !ok {
				err = fmt.Errorf("该构建不支持向指定会话提交(缺 SessionRunner)")
			} else {
				err = sr.RunInSession(context.Background(), skey, content)
			}
		default:
			if l, ok := s.loop.(sdk.AttachmentInput); ok && len(atts) > 0 {
				err = l.RunWithAttachments(context.Background(), content, atts)
			} else {
				err = s.loop.Run(context.Background(), content)
			}
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			// 用户按了停止(err = context.Canceled)不是错误:回合流里已经有
			// 「回合已取消」这一行,再推一条红字只会让“自己停的”看着像“崩了”
			// (2026-10-03 实机反馈)。
			s.hub.Push(Frame{Type: FrameError, Payload: err.Error()})
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "accepted": "turn"})
}

// steer 把输入注入运行中的回合(宿主 ctx.turnControl 实现 sdk.TurnSteerer)。
// 返回 false = 未装配该能力 / 无运行回合 / 落账失败 → 调用方按旧行为回落(409),
// 不静默把用户的输入吃掉。已注入的消息由回合落账,并经事件流回到前端渲染。
func (s *Server) steer(content string) bool {
	if s.tc == nil {
		return false
	}
	st, ok := s.tc.(sdk.TurnSteerer)
	if !ok {
		return false
	}
	injected, err := st.Steer(content)
	if err != nil {
		s.log.Warn("web: 转向注入失败(回落 409)", "err", err)
		return false
	}
	return injected
}

// runCommand 斜杠命令经 ctx.commands 同步执行(输出回 SSE 帧)。
func (s *Server) runCommand(content string, w http.ResponseWriter) {
	if s.cmds == nil {
		http.Error(w, "命令不可用: ctx.commands 未装配", http.StatusServiceUnavailable)
		return
	}
	fields := sdk.SplitArgs(content)
	if len(fields) == 0 { // 空输入:显式报错,不索引越界
		http.Error(w, "空命令", http.StatusBadRequest)
		return
	}
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
	// Session 作答所属会话 id。非当前会话的待答项在并行回合落地前不存在,
	// 带上错的 id 会被闸门拒收(而不是让一个窗口替另一个窗口应答)。
	Session string `json:"session,omitempty"`
}

func (s *Server) handleConfirm(w http.ResponseWriter, r *http.Request) {
	var req confirmReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if !s.guardWrite(w, req.Session) {
		return
	}
	s.confirm.Answer(req.ID, req.OK)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// StateView /api/state 快照(前端状态栏/首帧渲染)。
type StateView struct {
	Model    string `json:"model"`    // **生效值**(角色声明优先;与真正发出去的请求同源)
	Thinking string `json:"thinking"` // 同上
	// ModelFrom/ThinkingFrom 生效值来源(role|session):前端据此标“角色指定”。
	// 为何不是布尔:后续还可能有别的来源层(会话级 overlay 在未实施清单里)。
	ModelFrom    string `json:"model_from,omitempty"`
	ThinkingFrom string `json:"thinking_from,omitempty"`
	// ModelSession/ThinkingSession 会话档原值:设置面板要能显示“会话档 X(被角色 Y 覆盖)”,
	// 否则用户改完会话值看不到任何变化,也不知道为什么。
	ModelSession    string `json:"model_session,omitempty"`
	ThinkingSession string `json:"thinking_session,omitempty"`
	Sandbox         string `json:"sandbox"`
	// SandboxEffective 档位联动后的**有效**档(仅当与声明档不同时出现):
	// policy-guard 在 sync=true 时按审批档覆盖(open → full-access;strict → read-only),
	// 前端只看 sandbox 会与实际拦截行为不一致。
	SandboxEffective string `json:"sandbox_effective,omitempty"`
	SandboxDerived   bool   `json:"sandbox_derived,omitempty"` // 有效档 != 声明档(= 存在偏离)
	// SandboxFrom 偏离**来源**(role|approval;第九十二批):只有"审批联动"一种来源时前端
	// 可以猜,但角色收紧也走同一条偏离通道 —— 猜错就是把"角色收紧"显示成"随审批联动",
	// 而这两件事对用户意味着完全不同的处置(换角色 vs 改审批档)。
	SandboxFrom string `json:"sandbox_from,omitempty"`
	// SandboxSync 审批档→沙箱有效档 的联动开关(R10 ②-2;仅沙箱实现 sdk.SandboxSync 时出现)。
	// 与 SandboxDerived 是两件事:sync=false 时"有效档 == 声明档"不再等于"没有联动概念"。
	SandboxSync *bool  `json:"sandbox_sync,omitempty"`
	Approval    string `json:"approval,omitempty"` // M17:审批档位(open|smart|strict;未装配省略;**声明档**)
	// ApprovalEffective/ApprovalFrom 角色收紧后的**有效**审批档与偏离来源(第九十二批)。
	// 与沙箱同款:只报声明档会把"危险命令已被直接拒"显示成"会弹确认框"。
	ApprovalEffective string `json:"approval_effective,omitempty"`
	ApprovalFrom      string `json:"approval_from,omitempty"`
	// DataRoot/DataRootWritable:A-5#125 数据根可写性 —— 只读时前端出**页内提示条**
	// (此前只有启动期 WARN/ERROR 日志,浏览器/壳里的用户看不到)。
	// GAH_HOME 未注入(嵌入/单测)时两者都省略,不谎报可写。
	DataRoot         string `json:"data_root,omitempty"`
	DataRootWritable *bool  `json:"data_root_writable,omitempty"`
	// Role/RoleName:当前角色(第七十九批;未装配 ctx.roles 时省略 → 状态栏不显示徒标)
	Role     string         `json:"role,omitempty"`
	RoleName string         `json:"role_name,omitempty"`
	Stats    sdk.UsageStats `json:"stats"`
	Session  *SessionV      `json:"session,omitempty"`
	// Running 该作用域会话是否正在跑回合(每会话一张闸;多会话并行后可同时为真)。
	Running bool `json:"running"`
	// RunningSessions 当前有回合在跑的会话键(空串 = 主会话;多窗口用它显示「别的也在跑」)。
	RunningSessions []string `json:"running_sessions,omitempty"`
	// SessionID 本次快照对应的会话 id(缺省查询 = 当前主会话)。
	SessionID string `json:"session_id,omitempty"`
	// ActiveSessions 其它窗口正在占用的会话 id(多窗口场景可见;未装配目录时省略)。
	ActiveSessions []string `json:"active_sessions,omitempty"`
	Version        string   `json:"version"`
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
	// 角色先取:模型/思考档的**生效值**要经 sdk.Effective* 合成(与 host-roles 注入请求时同一判据);
	// 会话原值一并传出,面板才能说清“会话档被角色覆盖”。
	var roleSpec *sdk.RoleSpec
	if rs := s.roleService(); rs != nil {
		if id := rs.Current(); id != "" {
			if spec, ok := rs.Get(id); ok {
				roleSpec = &spec
			}
		}
	}
	sessionModel, sessionThinking := s.llm.Model(), names[lvl]
	effModel, modelFrom := sdk.EffectiveModel(sessionModel, roleSpec)
	effThinking, thinkingFrom := sdk.EffectiveThinking(sessionThinking, roleSpec)
	approval := ""
	if s.ap != nil {
		approval = string(s.ap.Mode())
	}
	declared := string(s.sb.Mode())
	// Running:每个会话各自一张闸(多会话并行后不再是全局单值)。
	scopeQ := strings.TrimSpace(r.URL.Query().Get("session"))
	v := StateView{
		Model: effModel, ModelFrom: modelFrom, ModelSession: sessionModel,
		Thinking: effThinking.String(), ThinkingFrom: thinkingFrom, ThinkingSession: sessionThinking,
		Sandbox:   declared,
		Approval:  approval,
		Running:   s.runningFor(s.sessionKey(scopeQ)).Load(),
		SessionID: scopeQ,
		Version:   os.Getenv("GAH_VERSION"),
	}
	if rs := s.runningSessionsOf(); len(rs) > 0 {
		v.RunningSessions = rs
	}
	if s.sdir != nil {
		if act := s.sdir.Active(); len(act) > 0 {
			v.ActiveSessions = act
		}
	}
	// A-5#125 数据根可写性:每次请求现探(用户改完权限**无需重启**就能看到提示条消失)。
	if root := dataRootPath(); root != "" {
		v.DataRoot, v.DataRootWritable = root, probeWritable(root)
	}
	// 联动开关(可选能力):设置面板据此渲染勾选态;未实现 = 省略(面板不显示该项)。
	if sc, ok := s.sb.(sdk.SandboxSync); ok {
		on := sc.SyncEnabled()
		v.SandboxSync = &on
	}
	// 沙箱实现可选能力 sdk.EffectiveSandbox 时对齐"实际生效档"(未实现 = 无联动,字段省略)。
	if es, ok := s.sb.(sdk.EffectiveSandbox); ok {
		if eff := string(es.EffectiveMode()); eff != declared {
			v.SandboxEffective, v.SandboxDerived = eff, true
			if src, ok := s.sb.(sdk.EffectiveSource); ok {
				v.SandboxFrom = src.EffectiveFrom()
			}
		}
	}
	// 审批档:角色可收紧(第九十二批)。有效档取实现自报值,不按角色声明猜 ——
	// 猜出来的值与真正裁决的档位一旦漂开,界面就成了假事实。
	if ea, ok := s.ap.(sdk.EffectiveApproval); ok {
		if eff := string(ea.EffectiveMode()); eff != approval {
			v.ApprovalEffective = eff
			if src, ok := s.ap.(sdk.EffectiveSource); ok {
				v.ApprovalFrom = src.EffectiveFrom()
			}
		}
	}
	if s.us != nil {
		v.Stats = s.us.Stats()
	}
	if s.cs != nil {
		v.Session = &SessionV{ID: s.cs.CurrentSession(), Name: s.cs.SessionName(), Path: s.cs.Path(), Key: s.cs.Current()}
	}
	// 当前角色(可选能力):状态栏徒标只读展示;未装配/未启用 → 字段省略
	if roleSpec != nil {
		v.Role, v.RoleName = roleSpec.ID, roleSpec.ID
		if roleSpec.Name != "" {
			v.RoleName = roleSpec.Name
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// handleSessionEvents 会话事件分页(GET /api/session/events?before=<seq>&limit=<n>)。
// 上滚加载更早历史的唯一入口:before 缺省/0 = 尾部窗口(与首连基线同口径),limit 可调小不可调大。
// 事件带完整载荷(前端用 consume 重建消息),与 SSE 实时帧同源 —— 两条路同一份账本事实。
// handleSessionEvents 首屏/上滚历史分页(?session= 指定会话;缺省 = 当前主会话)。
func (s *Server) handleSessionEvents(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeOf(r.URL.Query().Get("session"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotImplemented)
		return
	}
	defer sc.Release()
	before, _ := strconv.ParseUint(r.URL.Query().Get("before"), 10, 64)
	limit := 0
	if q := r.URL.Query().Get("limit"); q != "" {
		v, err := strconv.Atoi(q)
		if err != nil || v < 0 {
			http.Error(w, "limit 需为非负整数", http.StatusBadRequest)
			return
		}
		limit = v
	}
	if limit > SessionPageLimitMax {
		limit = SessionPageLimitMax
	}
	writeJSON(w, http.StatusOK, pageOf(sc.Log.Replay(), before, limit))
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
			Action string `json:"action"` // switch | new | spawn | fork | clone | pin | unpin | delete
			ID     string `json:"id"`     // switch/pin/unpin 目标(空 = 主会话)
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
		case "spawn":
			// 多窗口用:建一个**独立**会话且**不切换当前**。
			// 不能用 new:它会把当前会话切走(本窗口的历史与输入框当场换掉)。
			if s.sdir == nil {
				http.Error(w, "会话目录未装配(缺 ctx.sessionDir)", http.StatusNotImplemented)
				return
			}
			id, err := s.sdir.Spawn()
			if err != nil {
				http.Error(w, "新建失败: "+err.Error(), http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
		case "pin", "unpin":
			// F 组 F2:置顶/取消置顶(不新增端点,沿用 action 分派;上限 8 显式报错)
			if err := s.cs.SetPinned(req.ID, req.Action == "pin"); err != nil {
				http.Error(w, "置顶操作失败: "+err.Error(), http.StatusBadRequest)
				return
			}
			list := s.cs.Sessions()
			if list == nil {
				list = []sdk.SessionInfo{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": list})
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

// CommandOptionView 参数级候选项(逐级确认)。
type CommandOptionView struct {
	Value string `json:"value"`
	Desc  string `json:"desc"`
}

// handleCommandOptions 命令参数级枚举(POST /api/commands/{name}/options)。
// 与 TUI 选择器共用同一注册表声明(CommandSpec.Args):请求 {"picked":["env"]}
// (picked 不含命令名,服务端拼 [name, ...picked])→ 返回下一级的枚举候选与自由参数提示;
// items 与 freeArgs 皆空 = done(可直接执行)。无参数级声明也返回 done(前端不再提示)。
func (s *Server) handleCommandOptions(w http.ResponseWriter, r *http.Request) {
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
		Picked []string `json:"picked"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	picked := append([]string{name}, req.Picked...)
	resp := struct {
		Level    int                 `json:"level"`    // 返回的是第 level+1 级
		Items    []CommandOptionView `json:"items"`    // 枚举候选(可逐级点击)
		FreeArgs []string            `json:"freeArgs"` // 自由参数提示(需手动输入)
		Done     bool                `json:"done"`     // 无更多级 → 可直接执行
	}{Level: len(req.Picked) + 1}
	if idx := len(req.Picked); idx < len(spec.Args) {
		arg := spec.Args[idx]
		if arg.Options != nil {
			for _, o := range arg.Options(picked) {
				resp.Items = append(resp.Items, CommandOptionView{Value: o.Value, Desc: o.Desc})
			}
		}
		if arg.FreeArgs != nil {
			resp.FreeArgs = arg.FreeArgs(picked)
		}
	}
	resp.Done = len(resp.Items) == 0 && len(resp.FreeArgs) == 0
	// 空枚举/空自由参数若下发 null,前端逐级链会直接断掉(客户端按数组消费:
	// `resp.items.map(...)` 抛错 → 被 catch 吞掉 → 自由参数提示整级不显示)。统一归零为 []。
	if resp.Items == nil {
		resp.Items = []CommandOptionView{}
	}
	if resp.FreeArgs == nil {
		resp.FreeArgs = []string{}
	}
	writeJSON(w, http.StatusOK, resp)
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
		Model    string `json:"model"`
		Thinking string `json:"thinking"`
		Sandbox  string `json:"sandbox"`
		Approval string `json:"approval"`
		// SandboxSync 联动开关(指针:区分"没给"与"显式 false")—— R10 ②-2。
		SandboxSync *bool  `json:"sandbox_sync"`
		Workspace   string `json:"workspace"`
		Cancel      bool   `json:"cancel"`            // 取消运行中回合(经 ctx.turnControl;未装配 503)
		Session     string `json:"session,omitempty"` // 取消作用域:非空 = 只停该会话(需 SessionRunner)
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
		// 带 session = 只停那个会话的回合(多窗口各自停自己的);不带 = 停全部(TUI 语义)。
		if key := s.sessionKey(req.Session); key != "" {
			sr, ok := s.loop.(sdk.SessionRunner)
			if !ok {
				http.Error(w, "该构建不支持按会话取消(缺 SessionRunner)", http.StatusNotImplemented)
				return
			}
			sr.CancelSession(key)
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
		updatePrefs(func(p *prefs.Prefs) { p.Thinking = lvl.String() })
	}
	if req.Sandbox != "" {
		switch sdk.SandboxMode(req.Sandbox) {
		case sdk.SandboxReadOnly, sdk.SandboxWorkspace, sdk.SandboxFullAccess:
		default:
			http.Error(w, "未知沙箱档位(只读 read-only|工作区 workspace-write|全权 full-access)", http.StatusBadRequest)
			return
		}
		s.sb.SetMode(sdk.SandboxMode(req.Sandbox))
		// 持久化偏好(重启恢复)
		updatePrefs(func(p *prefs.Prefs) { p.Sandbox = req.Sandbox })
	}
	if req.SandboxSync != nil {
		sc, ok := s.sb.(sdk.SandboxSync)
		if !ok {
			http.Error(w, "该沙箱不支持联动开关(仅声明档)", http.StatusBadRequest)
			return
		}
		sc.SetSyncEnabled(*req.SandboxSync)
		updatePrefs(func(p *prefs.Prefs) { p.SandboxSync = req.SandboxSync })
	}
	if req.Approval != "" {
		switch sdk.ApprovalMode(req.Approval) {
		case sdk.ApprovalOpen, sdk.ApprovalSmart, sdk.ApprovalStrict:
		default:
			http.Error(w, "未知审批档位(open|smart|strict)", http.StatusBadRequest)
			return
		}
		if s.ap == nil {
			http.Error(w, "审批服务未装配(ctx.approval)", http.StatusBadRequest)
			return
		}
		s.ap.SetMode(sdk.ApprovalMode(req.Approval))
		// 持久化偏好(重启恢复)
		updatePrefs(func(p *prefs.Prefs) { p.Approval = req.Approval })
	}
	if req.Workspace != "" {
		if s.cs == nil {
			http.Error(w, "会话服务未装配(ctx.cwdSessions)", http.StatusBadRequest)
			return
		}
		// workspace = 真实目录(dir 语义,对齐 TUI /workspace):SwitchDir 内部
		// os.Chdir + key 派生 + 新建空会话,工作区记录以真实 dir 落盘。
		//
		// 进出都记日志(时间 + 结果):这段会重启外部工具进程并同步沙箱 root,真机上
		// 出现过「确认后界面一直不变、没有任何报错」(2026-09-17)—— 失败原因必须能在
		// 壳日志(它现在收集 sidecar stderr)里直接读到,而不是只给前端一句 400。
		s.log.Info("web: 切换工作区", "dir", req.Workspace)
		switchStart := time.Now()
		id, err := s.cs.SwitchDir(req.Workspace)
		if err != nil {
			s.log.Warn("web: 切换工作区失败", "dir", req.Workspace,
				"耗时", time.Since(switchStart).String(), "err", err)
			http.Error(w, "切换失败: "+err.Error(), http.StatusBadRequest)
			return
		}
		s.log.Info("web: 切换工作区完成", "dir", req.Workspace,
			"id", id, "耗时", time.Since(switchStart).String())
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
		if out == nil {
			out = []any{} // 空账本 list 序列化为 null → 契约给空数组(前端 Array.isArray 判定,与 /api/backup 同规)
		}
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

// uiPluginTrustNote UI 插件的信任模型提示(单一文案源,经 /api/ui-plugins 下发;前端设置面板照显)。
// 事实:UI 插件产物经动态 import() 进主页面,与主应用同源同 realm → 可调全部 API(含工具执行)。
const uiPluginTrustNote = "UI 插件与宿主同源同权限:可调用全部 API(含工具执行),只安装你信任的插件"

// UIPlugin 一个已安装 UI 插件的聚合视图(/api/ui-plugins;前端加载器用)。
type UIPlugin struct {
	ID      string    `json:"id"`
	Version string    `json:"version"`
	Slots   []SlotDef `json:"slots"`
	// 信任模型明示(不改数组结构:前端插件加载器按 id/slots 消费,新增字段向后兼容)。
	Trusted   bool   `json:"trusted"`
	TrustNote string `json:"trust_note"`
	// 产物摘要(R10 ⑤-3 完整性提示):sha256 覆盖范围见 HashScope —— 用户可拿它与发布方
	// 公布的校验值比对。**这不是安全边界**(能改插件目录的人也能改这里显示的哈希),
	// 但能让"与公布值不符"变成可见事实,而不是靠人肉翻目录。
	SHA256    string `json:"sha256,omitempty"`
	HashScope string `json:"hash_scope,omitempty"` // full(全部文件)/ entry(仅入口,超预算)/ none(不可读)
	HashNote  string `json:"hash_note,omitempty"`  // 降级原因(scope != full 时必填,不静默)
}

// SlotDef 槽位覆盖声明(前端动态导入 module 后 registerSlot)。
type SlotDef struct {
	Name     string `json:"name"` // v1:stream|input|statusbar|confirm;v2 扩展:settings-section/sidebar-action/extra-panel
	Priority int    `json:"priority"`
	Module   string `json:"module"` // 相对插件目录的产物入口(如 ./dist/plugin.js)
	// Title v2 扩展点展示文案(区段名/动作/面板标题);聚合时原样下发,
	// 丢失会让插件声明的标题退化为宿主默认文案(2026-09-19 本机验收遯到)。
	Title string `json:"title,omitempty"`
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
		// 完整性闸(2026-10-03):ui-plugins/SHA256SUMS 存在即强制。UI 插件是**与宿主同源
		// 同权限**的面(产物经动态 import() 进主页面,能调全部 API),此前只有一个
		// 「展示用」的摘要 —— 加载与否与它无关。拒绝的动作是**不下发**:前端因此完全
		// 看不到它,而不是「看到了但摘要不符」让人自己去比。
		// 停用(批四 §A.4):被用户停用的 UI 插件**不下发** ⇒ 前端根本不加载它。
		//
		// 为什么 UI 侧也需要「停用」这个中间态:批四给 UI 侧建了**默认闸**,被闸挡住之后
		// 如果只剩「删目录」这一条路,用户想「先关掉、回头再放行」就必须付出删除的代价。
		// 进程型侧在批二已经有这条中间态,两侧对齐。
		if prefs.IsUIDisabled(m.ID) {
			s.log.Info("ui-plugin: 已被用户停用,不下发", "id", m.ID)
			continue
		}
		if why := uiTrustCheck(dir, e.Name(), m); why != "" {
			s.log.Warn("ui-plugin: 完整性未通过,不下发", "id", m.ID, "reason", why)
			s.noteRejectedUI(m.ID, why)
			continue
		}
		h := digestPluginDir(filepath.Join(dir, e.Name()), m.Slots)
		out = append(out, UIPlugin{ID: m.ID, Version: m.Version, Slots: m.Slots, Trusted: true, TrustNote: uiPluginTrustNote,
			SHA256: h.Sum, HashScope: h.Scope, HashNote: h.Note})
	}
	return out
}

// clearRejectedUI 清空被拦记录(scan 开始时调:清单可能已被修复)。
func (s *Server) clearRejectedUI() {
	s.rejMu.Lock()
	s.rejectedUI = map[string]string{}
	s.rejMu.Unlock()
}

// noteRejectedUI 记一条被拦的 UI 插件(同一 id 覆盖最新原因)。
func (s *Server) noteRejectedUI(id, why string) {
	s.rejMu.Lock()
	defer s.rejMu.Unlock()
	if s.rejectedUI == nil {
		s.rejectedUI = map[string]string{}
	}
	s.rejectedUI[id] = why
}

// rejectedUIList 被拦的 UI 插件(排序;先清空再逐条重填 —— scan 是权威)。
func (s *Server) rejectedUIList() []pluginView {
	s.rejMu.Lock()
	ids := make([]string, 0, len(s.rejectedUI))
	for id := range s.rejectedUI {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]pluginView, 0, len(ids))
	for _, id := range ids {
		out = append(out, pluginView{
			PluginInfo: sdk.PluginInfo{ID: id, State: "rejected"},
			Manage:     "external",
			Rejected:   s.rejectedUI[id],
		})
	}
	s.rejMu.Unlock()
	return out
}

// uiTrustCheck UI 插件完整性校验(清单不存在 = 未启用 = 放行)。
//
// 摘要口径用 **entry scope**(manifest + 槽位声明会被 import 的模块),不是整目录 ——
// 见 internal/install/uitrust.go 的口径说明(全量既费预算又与「实际执行的代码」不精确对应)。
func uiTrustCheck(root, dirName string, m struct {
	ID      string    `json:"id"`
	Version string    `json:"version"`
	Slots   []SlotDef `json:"slots"`
}) string {
	list, err := plugintrust.Load(root)
	if err != nil {
		return err.Error()
	}
	if !list.Enforced() {
		return ""
	}
	slots := make([]install.UISlot, 0, len(m.Slots))
	for _, sl := range m.Slots {
		slots = append(slots, install.UISlot{Module: sl.Module})
	}
	d, derr := install.UIDigestEntry(filepath.Join(root, dirName), slots)
	if derr != nil {
		return derr.Error()
	}
	if err := list.Verify(m.ID, mustHexSum(d.Sum)); err != nil {
		return err.Error()
	}
	return ""
}

// mustHexSum 64 位 hex → [32]byte(清单比对用)。
func mustHexSum(s string) [32]byte {
	var out [32]byte
	b, _ := hex.DecodeString(s)
	copy(out[:], b)
	return out
}

// —— UI 插件产物摘要(R10 ⑤-3) ——

const (
	// pluginHashBudget 全量摘要的产物总字节预算:超预算只摘要入口产物并在 Note 里说明
	// (插件是 vite 产物,正常几十~几百 KB;预算触顶说明该目录不是普通构建产物)。
	pluginHashBudget = 4 << 20
	// pluginHashMaxFile 单文件上限:超过则跳过该文件并计入 Note(不静默漏掉;防一次性读巨物进内存)。
	pluginHashMaxFile = 1 << 20
)

// pluginDigest 一份产物摘要。
type pluginDigest struct {
	Sum   string // 十六进制 sha256(空 = 无可摘要产物)
	Scope string // full / entry / none
	Note  string // 降级原因与跳过项说明(scope=full 时为空)
}

// digestPluginDir 计算插件产物目录摘要(确定性:相对路径排序 + 逐文件 sha256 → 汇总)。
// 摘要输入含文件路径与长度(防"挪内容改名字"撞出同一值)。
// scope 语义严格对齐实际覆盖范围:任何降级(超预算/文件过大/读失败)都写进 Note ——
// 摘要最忌讳"看着是校验值,其实只盖了一半"。
func digestPluginDir(dir string, slots []SlotDef) pluginDigest {
	var files []string
	total := int64(0)
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil || strings.HasPrefix(filepath.Base(rel), ".") {
			return nil // 点文件(含 .DS_Store)不参与:不改变产物语义
		}
		files = append(files, rel)
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	if len(files) == 0 {
		return pluginDigest{Scope: "none", Note: "产物目录无文件"}
	}
	sort.Strings(files)
	overflow := total > pluginHashBudget
	// 超预算时只摘要「入口产物 + manifest」:manifest 定义了槽位指向,漏了它就能靠改指向
	// 绕过校验;两者都很小,加起来不会再把预算顶穿。
	entry := entryModules(slots)
	entry["manifest.json"] = true
	sum := sha256.New()
	covered, skipped, coveredBytes := 0, 0, int64(0)
	for _, rel := range files {
		if overflow && !entry[filepath.ToSlash(rel)] {
			skipped++
			continue // 超预算:只盖入口,其余计入 skipped(见 Note)
		}
		full := filepath.Join(dir, rel)
		info, err := os.Stat(full)
		if err != nil {
			skipped++
			continue
		}
		if info.Size() > pluginHashMaxFile {
			skipped++
			continue
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			skipped++
			continue
		}
		coveredBytes += int64(len(raw))
		fmt.Fprintf(sum, "%s\x00%d\x00", filepath.ToSlash(rel), len(raw))
		sum.Write(raw)
		covered++
	}
	scope := "full"
	var notes []string
	if overflow {
		scope = "entry"
		notes = append(notes, fmt.Sprintf("产物共 %s,超出摘要预算 %s:仅摘要入口产物与 manifest",
			humanBytes(total), humanBytes(pluginHashBudget)))
	}
	if skipped > 0 && !overflow {
		notes = append(notes, fmt.Sprintf("%d 个文件因过大或读失败未摘要", skipped))
	}
	if covered == 0 {
		return pluginDigest{Scope: "none", Note: strings.Join(append(notes, "无文件可摘要"), ";")}
	}
	if skipped > 0 {
		notes = append(notes, fmt.Sprintf("覆盖 %d/%d 个文件 %s", covered, len(files), humanBytes(coveredBytes)))
	}
	return pluginDigest{Sum: hex.EncodeToString(sum.Sum(nil)), Scope: scope, Note: strings.Join(notes, ";")}
}

// entryModules 槽位声明的入口产物集合(相对插件目录,规范化成 "./x" 与 "x" 都能命中)。
func entryModules(slots []SlotDef) map[string]bool {
	out := map[string]bool{}
	for _, sl := range slots {
		m := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(sl.Module)), "./")
		if m != "" {
			out[m] = true
		}
	}
	return out
}

// humanBytes 人读体积(摘要降级说明用;1 位小数足够)。
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// uiPluginsHandler 静态托管 ui-plugins 目录(插件 vite 产物;受鉴权门保护:token 模式缺凭据 → 引导页)。
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
	maxAttachBytes = 20 << 20 // 单文件上限 20MB
	maxAttachTotal = 32 << 20 // 单请求总上限 32MB
	maxAttachParts = 8        // 单请求最多 8 个文件
)

// AttachmentView 上传成功返回视图(前端 chip/预览用;Path=本地路径供 input 引用)。
type AttachmentView struct {
	Name string `json:"name"`
	Path string `json:"path"` // 本地绝对路径(input 提交时回传)
	URL  string `json:"url"`  // /attachments/<时间戳>/<名> 预览
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
		out     []AttachmentView
		total   int64
		written []string // 本次已落盘文件:任一失败分支统一回收(否则留孤儿文件堆积)
	)
	cleanup := func() {
		for _, p := range written {
			_ = os.Remove(p)
		}
		if len(written) > 0 {
			_ = os.Remove(dir) // 目录空则可删(非空/失败忽略)
		}
		written = nil
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			cleanup()
			http.Error(w, "multipart 解析失败", http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}
		if len(out) >= maxAttachParts {
			cleanup()
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
			cleanup()
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
		dstPath := filepath.Join(dir, dst)
		f, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			s.log.Error("附件写入失败", "err", err)
			cleanup()
			http.Error(w, "附件写入失败", http.StatusInternalServerError)
			return
		}
		written = append(written, dstPath)
		n, cerr := io.Copy(f, io.LimitReader(part, maxAttachBytes+1))
		f.Close()
		if cerr != nil {
			cleanup()
			http.Error(w, "附件写入失败", http.StatusInternalServerError)
			return
		}
		if n > maxAttachBytes {
			cleanup()
			http.Error(w, "附件超限(单文件 ≤20MB)", http.StatusRequestEntityTooLarge)
			return
		}
		total += n
		if total > maxAttachTotal {
			cleanup()
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
		cleanup()
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
	fs := http.StripPrefix("/attachments/", http.FileServerFS(os.DirFS(s.cfg.AttachmentsDir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.ServeHTTP(&attachGuardWriter{ResponseWriter: w, name: path.Base(strings.TrimSuffix(r.URL.Path, "/"))}, r)
	})
}

// attachGuardWriter 附件响应的类型裁决(安全审计 C2,2026-09-27)。
//
// 问题:附件端点此前把上传文件**原样内联**托管 —— `x.html`(或内容嗅探为 HTML 的无扩展名文件)
// 以 `text/html` 在同源返回,浏览器直接**执行**其中的脚本 ⇒ 存储型 XSS:脚本拿着 UI 的 cookie
// 调 `/api/*`(读会话、注入提示词、触发工具与审批)。上传又是用户日常动作,故门槛只有"点开预览"。
//
// 修法复用文档侧既有判据 `isDangerousInline`(**单一事实源**,见 web/doc.go):可执行文档类型
// 一律改 `Content-Disposition: attachment`(下载而非呈现)+ 收窄 CSP(`default-src 'none'; sandbox`,
// 连呈现都禁脚本与跳转)。图片(不含 svg)与 PDF 保持内联 —— 前端只用 `<img src="/attachments/…">`
// 预览,PDF 由浏览器原生查看器承载,无同源脚本面。
//
// 为何在 ResponseWriter 上拦截而不是看请求后缀:类型由 `http.ServeContent` **按扩展名或内容嗅探**
// 决定(无扩展名文件会被嗅探成 text/html),只有拿到它写入的 Content-Type 才能不漏判。
type attachGuardWriter struct {
	http.ResponseWriter
	name        string // 下载名(= 请求的文件名;仅用于 attachment 的 filename 参数)
	wroteHeader bool
}

func (a *attachGuardWriter) WriteHeader(code int) {
	if !a.wroteHeader {
		a.wroteHeader = true
		h := a.Header()
		if isDangerousInline(h.Get("Content-Type")) {
			h.Set("Content-Disposition", mime.FormatMediaType("attachment",
				map[string]string{"filename": a.name}))
			h.Set("Content-Security-Policy", attachDangerCSP)
		}
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *attachGuardWriter) Write(b []byte) (int, error) {
	if !a.wroteHeader {
		a.WriteHeader(http.StatusOK)
	}
	return a.ResponseWriter.Write(b)
}

// attachDangerCSP 可执行文档类型的响应 CSP:什么都不许(比全局的 frame-ancestors 'none' 严得多)。
const attachDangerCSP = "default-src 'none'; sandbox"

// attachmentMimeByExt 图片扩展名 → MIME(视觉注入 data URI 用;未知回源类型)。
var attachmentMimeByExt = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".bmp": "image/bmp", ".svg": "image/svg+xml",
}

// attachmentList 将已解析的附件绝对路径转为 sdk.Attachment(图片按扩展名标记视觉)。
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

// resolveAttachment 把提交的附件标识解析为附件根内的绝对路径(供模型读取/视觉注入)。
// 接受两种形式:① `/attachments/<rel>` 相对标识(前端默认:与平台路径语义无关,
// 服务端按自己的附件根解析,不受分隔符/大小写/数据根漂移影响);② 绝对路径(兼容旧客户端)。
// 两种形式都必须落在附件根内且为已存在的普通文件(防注入任意路径/目录穿越)。
func (s *Server) resolveAttachment(a string) (string, error) {
	root := filepath.Clean(s.cfg.AttachmentsDir)
	if s.cfg.AttachmentsDir == "" || root == "." {
		return "", errors.New("附件目录未配置")
	}
	if strings.TrimSpace(a) == "" {
		return "", errors.New("空路径")
	}
	var p string
	if rest, ok := strings.CutPrefix(a, "/attachments/"); ok {
		// 相对标识:必须是纯相对路径(反斜杠/绝对路径前缀一律拒)
		if rest == "" || strings.ContainsAny(rest, `\:`) {
			return "", errors.New("非法相对标识")
		}
		p = filepath.Join(root, filepath.FromSlash(rest))
	} else {
		p = filepath.Clean(a)
	}
	if !attachmentWithinRoot(root, p) {
		return "", errors.New("不在附件目录内")
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", errors.New("文件不存在或不可访问")
	}
	if fi.IsDir() {
		return "", errors.New("是目录而不是文件")
	}
	return p, nil
}

// attachmentWithinRoot 判断 p 是否位于 root 之内(段级归属判定,防 `..` 越界)。
// Windows 路径大小写不敏感(见 attachmentWithinRootFold)。
func attachmentWithinRoot(root, p string) bool {
	return attachmentWithinRootFold(root, p, runtime.GOOS == "windows")
}

// attachmentWithinRootFold 带显式大小写折叠开关的归属判定(便于在非 Windows 上覆盖该分支)。
func attachmentWithinRootFold(root, p string, foldCase bool) bool {
	r, c := filepath.Clean(root), filepath.Clean(p)
	if foldCase {
		r, c = strings.ToLower(r), strings.ToLower(c)
	}
	rel, err := filepath.Rel(r, c)
	if err != nil {
		return false
	}
	if rel == "." {
		return false // 附件根目录自身不是可提交的附件
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	} else if r, err := fs.Sub(sub, "dist"); err == nil {
		root = r
	} else {
		// 不构造 nil fs.FS(http.FileServerFS(nil) 每个请求 panic):显式 500 并记错误
		s.log.Error("web: 静态资源不可用(web/dist 缺失或 static_dir 无 index.html)", "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "静态资源不可用: web/dist 未构建", http.StatusInternalServerError)
		})
	}
	// 静态资源不再自设 cookie:token 模式的凭据只经 POST /api/auth(引导页)下发,
	// 缺凭据时由 authMiddleware 返回引导页(见 bootstrap.go)。
	// CSP 纵深:只加在 SPA 静态响应上(见 guard.go spaCSP)。
	return withSPACSP(http.FileServerFS(root))
}

// authMiddleware 鉴权门(token 模式全表面,data.auth_token 非空时生效):
//   - /api/* 缺凭据 → 401(WS 握手同此);
//   - 其它路径(导航/静态资源/附件/UI 插件产物)缺凭据 → 引导页(不含任何业务内容,见 bootstrap.go);
//   - 非 GET/HEAD 的非 API 路径缺凭据 → 401(引导页只服务浏览器导航)。
//
// 唯一豁免 = POST /api/auth(引导页用 fragment 里的 token 换 cookie;仍受 guardMiddleware 的
// Host 白名单 + 同源校验约束)。凭据通道:Authorization: Bearer <token> 或 gah_token cookie;
// 不再接受 ?token=(会进浏览器历史/访问日志/Referer)。
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	tok := s.cfg.AuthToken
	if tok == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == authPath {
			next.ServeHTTP(w, r) // 引导通道:凭据在 body/Bearer,由 handleAuth 校验
			return
		}
		if subtle.ConstantTimeCompare([]byte(s.credential(r)), []byte(tok)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") ||
			(r.Method != http.MethodGet && r.Method != http.MethodHead) {
			http.Error(w, "未授权", http.StatusUnauthorized)
			return
		}
		writeBootstrap(w)
	})
}

// credential 取请求携带的凭据(Authorization: Bearer 优先,其次 gah_token cookie)。
func (s *Server) credential(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie("gah_token"); err == nil {
		return c.Value // 多浏览器会话:引导页换取后,后续请求/WS 握手自动携带
	}
	return ""
}

// handleAuth token 引导通道(POST /api/auth):凭据取自 body {"token":"..."} 或 Bearer 头,
// constant-time 比对;成功 → 204 + gah_token cookie(HttpOnly/SameSite=Strict;https 加 Secure)。
// 未启用鉴权(data.auth_token 空)→ 404(显式:无需引导)。失败不回显 token。
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "引导通道仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	tok := s.cfg.AuthToken
	if tok == "" {
		http.Error(w, "未启用鉴权(data.auth_token 为空)", http.StatusNotFound)
		return
	}
	given := ""
	if r.Body != nil {
		var b struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, authBodyLimit)).Decode(&b); err == nil {
			given = strings.TrimSpace(b.Token)
		}
	}
	if given == "" {
		given = s.credential(r) // 仅凭 Bearer 头调用(无 body)的客户端
	}
	if subtle.ConstantTimeCompare([]byte(given), []byte(tok)) != 1 {
		http.Error(w, "凭据无效", http.StatusUnauthorized)
		return
	}
	s.setAuthCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// setAuthCookie 下发会话 cookie(HttpOnly + SameSite=Strict;https 时 Secure)。
func (s *Server) setAuthCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // 会话凭据:HttpOnly+SameSite=Strict;同机用户本就能读 config 里的 token
		Name: "gah_token", Value: s.cfg.AuthToken, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr 写 JSON 错误体(第九十六批):前端 req() 优先解析 {error} —— 用 http.Error 的纯文本
// 它只能把整段原文当消息,而 JSON 体又会被原样甩出来({"error":"…"} 是开发者形状)。
// 本批只收敛「模型列举 / 多 provider 能力未实现」这族**用户会看到**的 501;其余 http.Error
// 逐点收敛是另一笔账(前端已能回退纯文本,不会报 JSON 解析错)。
func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

// —— 通用能力 REST 面(为前端增删改铺路;可选服务缺失显式 503/501) ——

// handleTools 工具清单(GET /api/tools):模型可见定义(name/desc/schema)。
// `?all=1` = **管理面**(设置面板的角色工具勾选):返回全部已注册工具 —— 被角色排除的
// 工具必须还列得出来,否则面板分不清“被角色排除”与“没装这个插件”(第九十一批)。
func (s *Server) handleTools(w http.ResponseWriter, r *http.Request) {
	if s.tools == nil {
		writeJSON(w, http.StatusOK, []sdk.ToolDefinition{})
		return
	}
	list := s.tools.List()
	if r.URL.Query().Get("all") != "" {
		if tc, ok := s.tools.(sdk.ToolCatalogue); ok {
			list = tc.ListAll()
		}
	}
	if list == nil {
		list = []sdk.ToolDefinition{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleToolConflicts 注册期同名工具冲突(GET /api/tools/conflicts;B3 可见性面)。
// 为何单独一个端点而不是塞进 /api/tools:后者是**模型可见定义**的数组,消费方多
// (前端/外部 MCP 客户端/回调插件),改形状会连带破坏;冲突是诊断面,单独给。
func (s *Server) handleToolConflicts(w http.ResponseWriter, _ *http.Request) {
	out := []sdk.ToolConflict{}
	if s.tools != nil {
		if rep, ok := s.tools.(sdk.ToolConflictReporter); ok {
			if got := rep.ToolConflicts(); got != nil {
				out = got
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
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

// handleSchedules 定时计划列表(GET /api/schedules;未装配 ctx.schedule → 503)。
// handleNotices GET /api/notices?since=<id> —— 提示回填(NOND-N1)。
// 提示不落盘、不进会话记录,是**瞬时信号**:页面刷新/重连后只能靠这个端点补上
// 「离开期间错过的那几条」。返回 NoticePage{items,max_id,gap} —— gap=true 表示 since 之后
// 确有提示被环形缓冲丢弃,回填不完整(前端须如实标注,不得谎报完整)。
func (s *Server) handleNotices(w http.ResponseWriter, r *http.Request) {
	if s.notices == nil {
		http.Error(w, "提示通道未装配(ctx.notices)", http.StatusServiceUnavailable)
		return
	}
	var since uint64
	if raw := r.URL.Query().Get("since"); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			http.Error(w, "since 需为非负整数", http.StatusBadRequest)
			return
		}
		since = v
	}
	page := s.notices.List(since)
	if page.Items == nil {
		page.Items = []sdk.Notice{}
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleSchedules(w http.ResponseWriter, _ *http.Request) {
	if s.sched == nil {
		http.Error(w, "定时计划服务未装配(ctx.schedule)", http.StatusServiceUnavailable)
		return
	}
	list := s.sched.List()
	if list == nil {
		list = []sdk.Schedule{}
	}
	writeJSON(w, http.StatusOK, list)
}

// scheduleReq 新增/修改计划的请求体(PATCH 为空字段 = 不改;Enabled 用指针区分“未传”与 false)。
type scheduleReq struct {
	Name    string `json:"name"`
	Cron    string `json:"cron"`
	Prompt  string `json:"prompt"`
	Enabled *bool  `json:"enabled"`
}

// handleScheduleAdd 新增计划(POST /api/schedules)。
func (s *Server) handleScheduleAdd(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil {
		http.Error(w, "定时计划服务未装配(ctx.schedule)", http.StatusServiceUnavailable)
		return
	}
	var req scheduleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	enabled := true // 默认启用(新建即生效;停用需显式传 false)
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	p, err := s.sched.Add(sdk.Schedule{Name: req.Name, Cron: req.Cron, Prompt: req.Prompt, Enabled: enabled})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// handleScheduleUpdate 修改计划(PATCH /api/schedules/{id}):仅覆盖传入字段。
func (s *Server) handleScheduleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil {
		http.Error(w, "定时计划服务未装配(ctx.schedule)", http.StatusServiceUnavailable)
		return
	}
	id := r.PathValue("id")
	cur, ok := findSchedule(s.sched.List(), id)
	if !ok {
		http.Error(w, "计划不存在: "+id, http.StatusNotFound)
		return
	}
	var req scheduleReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if req.Name != "" {
		cur.Name = req.Name
	}
	if req.Cron != "" {
		cur.Cron = req.Cron
	}
	if req.Prompt != "" {
		cur.Prompt = req.Prompt
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	upd, err := s.sched.Update(cur)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, upd)
}

// handleScheduleDelete 删除计划(DELETE /api/schedules/{id})。
func (s *Server) handleScheduleDelete(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil {
		http.Error(w, "定时计划服务未装配(ctx.schedule)", http.StatusServiceUnavailable)
		return
	}
	if err := s.sched.Remove(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleScheduleRun 立即触发一次(POST /api/schedules/{id}/run;异步,状态回读列表)。
func (s *Server) handleScheduleRun(w http.ResponseWriter, r *http.Request) {
	if s.sched == nil {
		http.Error(w, "定时计划服务未装配(ctx.schedule)", http.StatusServiceUnavailable)
		return
	}
	if err := s.sched.RunNow(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// findSchedule 按 ID 取计划快照。
func findSchedule(plans []sdk.Schedule, id string) (sdk.Schedule, bool) {
	for _, p := range plans {
		if p.ID == id {
			return p, true
		}
	}
	return sdk.Schedule{}, false
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
		// Session 目标会话 id:命令(如 /compact、/export)在哪个会话上执行。
		// 非当前会话需 per-session 回合能力,否则 409(见 web/session_scope.go)。
		Session string `json:"session,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if !s.guardWrite(w, req.Session) {
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
	// Rejected 非空 = 这条是**被拒绝加载**的外部插件(白名单不符等),不是「已装载」。
	// 为何要单列:被拒的表现是「工具整组消失」,而原因若只在日志里,界面就无从区分
	// 「没装」与「被拦」—— 面板据此显示原因与补救办法。
	Rejected string `json:"rejected,omitempty"`
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
	// 被拒的外部插件:实现 sdk.RejectedPlugins 的宿主才报(否则本宿主没这个面)。
	// 为何要单列:被拒的表现是「工具整组消失」,而原因若只在日志里,界面就无从区分
	// 「没装」与「被拦」—— 面板据此显示原因与补救办法。
	if rp, ok := s.extp.(sdk.RejectedPlugins); ok {
		for _, r := range rp.Rejected() {
			out = append(out, pluginView{
				PluginInfo: sdk.PluginInfo{ID: r.Name, State: "rejected"},
				Manage:     "external",
				Rejected:   r.Reason,
			})
		}
	}
	// 被完整性闸拦下的 UI 插件也列出来(否则它只是「不见了」):scan 是权威,每次现清现填。
	s.clearRejectedUI()
	_ = s.scanUIPlugins()
	out = append(out, s.rejectedUIList()...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
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

// providerModelsView 聚合模型列表的可序列化视图。
// 为何不直接输出 sdk.ProviderModelList:`Err` 是 `error` 接口,JSON 只能序列化成 `{}` 或
// 内嵌字段(前端永远拿不到失败原因),首启引导的「连通性自检」就无从给 401/404/DNS 人话提示。
// 转字符串同时给长度上限(端点返回 HTML 错误页时不把整页塞进响应体)。
type providerModelsView struct {
	Name    string          `json:"Name"`
	BaseURL string          `json:"BaseURL"`
	Models  []sdk.ModelInfo `json:"Models"`
	Err     string          `json:"Err,omitempty"`
}

// probeErrMaxRunes 单条探测错误的最大长度(超出截断并加省略号)。
const probeErrMaxRunes = 300

// truncateRunes 按 rune 截断(不切断多字节字符)。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// providerModelsViews 把聚合结果转为可序列化视图(Err → 字符串;Models 归一为非 nil 数组)。
func providerModelsViews(list []sdk.ProviderModelList) []providerModelsView {
	out := make([]providerModelsView, 0, len(list))
	for _, p := range list {
		v := providerModelsView{Name: p.Name, BaseURL: p.BaseURL, Models: p.Models}
		if v.Models == nil {
			v.Models = []sdk.ModelInfo{} // 前端 length/遍历安全(与 /api/models 单端点分支口径一致)
		}
		if p.Err != nil {
			v.Err = truncateRunes(p.Err.Error(), probeErrMaxRunes)
		}
		out = append(out, v)
	}
	return out
}

// handleModels 聚合模型列表(GET /api/models)。
// 多 provider 存在 → 每端点聚合(单条失败记 error 不整体失败);否则当前适配器列表。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("all") == "1" {
		if mp, ok := s.llm.(sdk.MultiProviderService); ok {
			writeJSON(w, http.StatusOK, map[string]any{"providers": providerModelsViews(mp.ListAllModels())})
			return
		}
	}
	models, err := s.llm.ListModels()
	if err != nil {
		writeErr(w, http.StatusNotImplemented, "当前适配器不支持列举模型(可手动设置): "+err.Error())
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
		writeErr(w, http.StatusNotImplemented, "多 provider 能力未实现(MultiProviderService)")
		return
	}
	// 不回传明文密钥(设置面板只需 name/base_url/model/active;编辑时手填新 key)。
	// 此前直接 writeJSON(mp.Providers()) → 任何能访问 /api/providers 的页面/脚本
	// 都能拿到全部 provider 的 API key 明文。
	list := mp.Providers()
	out := make([]sdk.ProviderProfile, 0, len(list))
	for _, p := range list {
		p.APIKey = maskKey(p.APIKey)
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// maskKey 密钥回显掩码(保留首 3/末 4 便于辨识;短 key 全掩)。
func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "****"
	}
	return k[:3] + "****" + k[len(k)-4:]
}

// handleProviderAdd 新增/更新 provider(POST /api/providers;同名 upsert,首个自动激活)。
func (s *Server) handleProviderAdd(w http.ResponseWriter, r *http.Request) {
	mp, ok := s.llm.(sdk.MultiProviderService)
	if !ok {
		writeErr(w, http.StatusNotImplemented, "多 provider 能力未实现(MultiProviderService)")
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
		writeErr(w, http.StatusNotImplemented, "多 provider 能力未实现(MultiProviderService)")
		return
	}
	if err := mp.SetActiveProvider(r.PathValue("name")); err != nil {
		http.Error(w, "切换失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleProviderDelete 删除 provider(DELETE /api/providers/{name})。
// 单条删除已实现(第二十一批,结清 M12 记的 TODO):删活跃则活跃顺延剩余首个,删空回退 env/样板;
// 不存在显式 400(不静默成功)。
func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request) {
	mp, ok := s.llm.(sdk.MultiProviderService)
	if !ok {
		writeErr(w, http.StatusNotImplemented, "多 provider 能力未实现(MultiProviderService)")
		return
	}
	if err := mp.RemoveProvider(r.PathValue("name")); err != nil {
		http.Error(w, "删除失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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

// handleSessionExport 会话导出(GET /api/sessions/{id}/export、GET /api/sessions/export):
//
//	?format=jsonl(缺省)原始 jsonl 下载;?format=html 自包含 HTML 下载
//	(渲染器与 TUI `/export <路径>.html` 同一实现,见 internal/sessionhtml)。
//
// id 空(含无 id 路由)= 主会话;匹配失败显式 404(不静默)。
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
	fname := "session-main"
	if id != "" {
		fname = "session-" + id
	}
	if r.URL.Query().Get("format") == "html" {
		// 会话文件 → 类型化事件 → HTML:与 /export *.html 同一条链(sessionevents 还原
		// 载荷,渲染器在 internal/sessionhtml)。不切当前会话,纯读。
		evs, err := sessionevents.ReadFile(path)
		if err != nil {
			s.log.Error("导出读事件失败", "path", path, "err", err)
			http.Error(w, "会话文件不可读", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`.html"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sessionhtml.Render(evs)))
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`.jsonl"`)
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
