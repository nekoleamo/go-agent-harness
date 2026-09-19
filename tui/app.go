// App:装配事件桥,驱动 bubbletea 程序。插件 ui-tui-app 仅做薄壳挂载。
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// App 封装 TUI 程序与宿主桥接。
// 事件流:session/event + agent/status 广播 → program.Send → Update → 渲染。
// 输入:onSubmit → goroutine 跑 agentLoop.Run(回合异步,不阻塞 UI)。
type App struct {
	model     *Model
	program   *tea.Program
	c         sdk.Ctx
	loop      sdk.AgentLoop
	llm       sdk.LLMService
	confirmCh chan bool // Confirm 阻塞等待用户答复(单通道,兼容旧路径)

	pendMu  sync.Mutex  // 融合 Present 的待应答通道(P3:tui 作为 confirm presenter)
	pending []chan bool // 每次 Present 一个;确认结果广播并清理

	askMu   sync.Mutex   // 提问待答通道(P3:tui 作为 question presenter)
	qPend   []pendingQ   // 每次 PresentQuestion 一个(按 id 定向回填,见 answerQuestion)
	qSeq    atomic.Int64 // 无 id 提问(直调 presenter/测试)的本地编号源
	started atomic.Bool  // TUI 程序已启动(未启动时不向 program 发送,防测试/装配期阻塞)
	subs    []sdk.Disposer
	cmds    sdk.CommandRegistry // ctx.commands(可为 nil:未装配时命令不可用)

	// cancelFn 当前回合的取消函数(Esc 中断)。UI goroutine 读、回合 goroutine 写,
	// 必须原子(普通字段是 data race:取消丢失或对已结束回合误调)。
	cancelFn atomic.Pointer[context.CancelFunc]
	widgets  []Widget // P4-12 输入区 widget 行(宿主/插件经 AddWidget 注册)

	mFiles    []sdk.Option // @ 引用文件索引缓存(projectFiles;当前 cwd 下惰性构建)
	mFilesDir string       // 缓存对应的 cwd(失效判据:workspace 切换后重建)

	themeBase map[string]string // M13 启动活动覆盖链(data.palette+theme.yaml),/theme default 重置目标
	notifier  *notifier         // NOND-N2 系统级通知落点(探测 + 逐级降级;/notify 可查/可切)
}

// NewApp 构造 TUI 应用。命令注册表(ctx.commands,host-commands 提供)注入:
// 内部命令(宿主级)注册进表与插件命令共表——提示列表/分发/help 全部动态。
func NewApp(c sdk.Ctx, loop sdk.AgentLoop, llm sdk.LLMService, profile string, palette ...map[string]string) *App {
	// 启动即从服务拉取实际生效配置(模型/沙箱/思考等级),状态栏不显示"未设置"等假默认;
	// 装配顺序保证适配器已 SetModel(host-llm → 适配器先于 ui 启动)。
	state := &State{Profile: profile, Workspace: workspaceName(),
		Model:    llm.Model(),
		Thinking: llm.Thinking().String(),
	}
	// 沙箱档位从服务读实际值(而非展示层写死 workspace-write);联动覆盖时标注有效档
	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err == nil && sb != nil {
		state.Sandbox = sandboxDisplay(sb)
	}
	m := &Model{state: state}
	a := &App{model: m, c: c, loop: loop, llm: llm, confirmCh: make(chan bool, 1)}
	// M13 主题启动加载链:data.palette(装配层样板)→ theme.yaml(用户全局覆盖)→ 默认表。
	// 坏主题文件显式提示(错误行),不中断 TUI(渲染以默认表兜底)。
	base := map[string]string{}
	if len(palette) > 0 {
		for k, v := range palette[0] {
			base[k] = v
		}
	}
	if over, err := loadThemeMain(); err != nil {
		state.Lines = append(state.Lines, Line{Kind: "error", Text: "主题加载失败: " + err.Error()})
	} else {
		for k, v := range over {
			base[k] = v
		}
		if err := ApplyTheme(base); err != nil {
			state.Lines = append(state.Lines, Line{Kind: "error", Text: "主题应用失败: " + err.Error()})
		}
	}
	a.themeBase = base
	a.notifier = newDefaultNotifier() // NOND-N2:探测终端能力(失败 = 仅状态栏,不报错)
	a.syncDisplay()                   // 状态栏模型 + 来源(provider 域名缩写)拉实际生效值
	var reg sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &reg); err != nil {
		// host-commands 未装配:命令分发/提示不可用(不阻塞 TUI)
		reg = nil
	}
	a.cmds = reg
	m.onSubmit = a.submit
	m.onCommand = a.command
	m.onConfirm = a.confirmResult
	m.onQuestion = a.answerQuestion
	m.onCancel = a.cancelCurrent
	m.hints = a.suggestHints
	m.levels = a.levels
	m.onFiles = a.projectFiles                         // @ 引用补全候选(项目文件索引,含 cwd 缓存;workspace 切换失效)
	m.onWidgets = func() []Widget { return a.widgets } // P4-12 widget 行注入(渲染帧拉取)
	m.onThinkingCycle = a.cycleThinking
	m.onOpenDoc = a.loadDocPager
	m.onStats = func() sdk.UsageStats {
		var us sdk.UsageStatsService
		if err := a.c.Inject("ctx.usageStats", &us); err != nil {
			return sdk.UsageStats{} // host-usage-stats 未装配:状态栏显示 上下文 -
		}
		return us.Stats()
	}
	m.onDock = a.dockInfo
	m.onDockOutput = a.dockOutputCmd
	m.onNotice = a.systemNotify // NOND-N2 提示 → 系统级落点(终端的活,warn/error 才发)
	m.onDockKill = a.dockKillCmd
	m.onDockSteer = a.dockSteer
	a.registerInternalCommands()
	a.applyPrefs() // 恢复上次退出偏好(思考/沙箱/历史;与 Web 共享 gah-state.json)
	// 启动即新会话(host-cwd-sessions 启动时 New):模型上下文与展示层均从空开始,
	// 不自动重放主会话历史——过往对话保留在会话文件,经 /session switch 进入时重放。
	// 状态栏显示当前会话标签(名优先,无名称回退 id/主会话)。
	{
		var cs sdk.CwdSessions
		if err := c.Inject("ctx.cwdSessions", &cs); err == nil {
			state.Session = sessionLabel(cs)
		}
	}
	a.program = tea.NewProgram(m)
	return a
}

// dockInfo S-P0-3 后台坞拉取:汇总 ctx.jobs 与 ctx.fanout 的计数、最新一条运行中任务,
// 以及展开列表的行快照(薄投影,见 dockRows)。
// 只读拉取(渲染帧调用,不写状态);两服务均未装配时返回零值(折叠行不占位)。
// 明细动作(看输出/定向/终止)走宿主命令 /jobs 与 ctx.fanout,与 TUI/Web 同源。
func (a *App) dockInfo() DockInfo {
	var info DockInfo
	// Inject 失败/类型不符时目标保持零值(nil),dockRows 容忍 nil(仅取到多少算多少)。
	// 不用 `if ... == nil && s != nil` 的旧写法:计数与列表行必须同一份快照,避免两遍遍历。
	var js sdk.JobService
	_ = a.c.Inject("ctx.jobs", &js)
	var fo sdk.FanoutService
	_ = a.c.Inject("ctx.fanout", &fo)
	info.Rows = dockRows(js, fo)
	for _, r := range info.Rows {
		info.Total++
		if r.running {
			info.Running++
			if info.Latest == "" {
				info.Latest = r.Summary
			}
		}
	}
	return info
}

// dockOutputCmd / dockKillCmd 坞面板动作:转调宿主命令 /jobs(单一事实源),
// 不在 TUI 重复实现输出渲染与终止逻辑。命令未装配(无 host-jobs)→ 显式错误。
func (a *App) dockOutputCmd(id string) (*DocPager, error) {
	spec, ok := a.cmds.Get("jobs")
	if !ok || spec.Run == nil {
		return nil, errString("/jobs 命令未装配(需 host-jobs 插件),无法查看后台输出")
	}
	text, err := spec.Run([]string{"output", id})
	if err != nil {
		return nil, err
	}
	return NewTextPager(TextPagerSpec{
		Title:  "后台输出 " + id,
		Format: "text",
		Status: "来自 /jobs output(与 TUI/Web 命令同源)",
		Lines:  strings.Split(text, "\n"),
	}), nil
}

func (a *App) dockKillCmd(id string) (string, error) {
	spec, ok := a.cmds.Get("jobs")
	if !ok || spec.Run == nil {
		return "", errString("/jobs 命令未装配(需 host-jobs 插件),无法停止后台任务")
	}
	return spec.Run([]string{"kill", id})
}

// dockSteer 定向:仅后台子代理支持(ctx.fanout.SendMessage);任务行/未运行/未装配均显式报错。
// 先在列表里定位行种类:job 不支持注入消息(与 Hermes 坞同语义,只对子代理定向)。
func (a *App) dockSteer(id, msg string) error {
	for _, r := range a.model.state.DockRows {
		if r.ID == id && r.Kind != "agent" {
			return errString("定向仅适用于子代理(选中项是后台任务;任务无会话可注入)")
		}
	}
	var fo sdk.FanoutService
	if err := a.c.Inject("ctx.fanout", &fo); err != nil || fo == nil {
		return errString("ctx.fanout 未装配(需 host-fanout),无法定向")
	}
	if err := fo.SendMessage(id, msg); err != nil {
		return errString("定向失败: " + err.Error())
	}
	return nil
}

// OpenDoc 请求打开文档预览(host `doc/open` 事件 / 工具行 / 命令;异步投递到 UI 循环)。
func (a *App) OpenDoc(path string, page, sheet int) {
	if path == "" {
		return
	}
	if a.program != nil && a.started.Load() {
		a.program.Send(DocOpenMsg{Path: path, Page: page, Sheet: sheet})
		return
	}
	// 未启动/测试:直接构造(不阻塞)
	if p, err := a.loadDocPager(path, page, sheet); err == nil && p != nil {
		a.model.state.Doc = p
	}
}

// OpenPager 打开已构造好的浮层(S-P1-1 diff/open:内容由 host 侧成型,端侧只渲染)。
// nil 不入栈(清单意图不弹窗)。
func (a *App) OpenPager(p *DocPager) {
	if p == nil {
		return
	}
	if a.program != nil && a.started.Load() {
		a.program.Send(PagerMsg{Pager: p})
		return
	}
	// 未启动/测试:直接入栈(不阻塞)
	a.model.state.Doc = p
}

// loadDocPager 经 ctx.doc 加载文档并构造 pager(TUI 侧不到 Web 端点,直连服务)。
func (a *App) loadDocPager(path string, page, sheet int) (*DocPager, error) {
	var doc sdk.DocService
	if err := a.c.Inject("ctx.doc", &doc); err != nil {
		return nil, errString("ctx.doc 未装配(host-docview): " + err.Error())
	}
	req := sdk.DocRequest{Path: path, Page: page, Sheet: sheet}
	if abs, err := a.absWorkspacePath(path); err == nil {
		req.Path = abs
	}
	v, err := doc.Preview(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return NewDocPager(v, page, sheet), nil
}

// absWorkspacePath 把工作区相对路径解析为绝对路径(TUI 侧 ctx.doc 未注入沙箱时
// resolver 以 cwd 为根,显式绝对化避免歧义)。
func (a *App) absWorkspacePath(p string) (string, error) {
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	root := "."
	var sb sdk.Sandbox
	if err := a.c.Inject("ctx.sandbox", &sb); err == nil && sb != nil && sb.Root() != "" {
		root = sb.Root()
	} else if wd, err := os.Getwd(); err == nil {
		root = wd
	}
	return filepath.Join(root, p), nil
}

// Confirm 实现 sdk.ConfirmService:弹层询问用户 y/n。
// 无 UI 运行(非 TTY 降级)时 UI 插件不装配本服务,策略拒绝(安全默认)。
func (a *App) Confirm(ctx context.Context, prompt string) (bool, error) {
	ch, cancel, err := a.Present(ctx, prompt)
	if err != nil {
		return false, err
	}
	defer cancel()
	select {
	case ok := <-ch:
		return ok, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Present 实现 sdk.ConfirmPresenter(多端融合):弹层呈现并返回应答通道;
// cancel 撤销本次待答(幂等)。融合场景(Fusion 广播)与 Web 共用同一确认。
func (a *App) Present(_ context.Context, prompt string) (<-chan bool, func(), error) {
	ch := make(chan bool, 1)
	a.pendMu.Lock()
	a.pending = append(a.pending, ch)
	a.pendMu.Unlock()
	if a.program != nil && a.started.Load() { // 未启动(测试/装配期)仅登记待答,不 Send
		a.program.Send(confirmMsg{prompt})
	}
	cancel := func() {
		a.pendMu.Lock()
		for i, c := range a.pending {
			if c == ch {
				a.pending = append(a.pending[:i], a.pending[i+1:]...)
				break
			}
		}
		a.pendMu.Unlock()
	}
	return ch, cancel, nil
}

// confirmResult 用户应答:广播给全部待答通道(融合场景各渠道独立通道;通常 1 个),
// 并兼容旧 confirmCh(单通道路径)。
func (a *App) confirmResult(ok bool) {
	select {
	case a.confirmCh <- ok:
	default:
	}
	a.pendMu.Lock()
	pends := a.pending
	a.pending = nil
	a.pendMu.Unlock()
	for _, ch := range pends {
		select {
		case ch <- ok:
		default:
		}
	}
}

// PresentQuestion sdk.QuestionPresenter(P3 语义交互):问题与编号选项入会话流,
// 返回作答通道;cancel 幂等撤销。**同一问题可并存**:作答按 id 定向回填(不误答栈内其它提问)。
func (a *App) PresentQuestion(_ context.Context, q sdk.Question) (<-chan sdk.QuestionAnswer, func(), error) {
	ch := make(chan sdk.QuestionAnswer, 1)
	if q.ID == "" { // 直调 presenter(未经 host-confirm-fusion 补 id)时本地兜底编号
		q.ID = fmt.Sprintf("tui-%d", a.qSeq.Add(1))
	}
	a.askMu.Lock()
	a.qPend = append(a.qPend, pendingQ{id: q.ID, ch: ch})
	a.askMu.Unlock()
	if a.program != nil && a.started.Load() {
		a.program.Send(questionMsg{q: q})
	}
	cancel := func() {
		a.askMu.Lock()
		for i, p := range a.qPend {
			if p.ch == ch {
				a.qPend = append(a.qPend[:i], a.qPend[i+1:]...)
				break
			}
		}
		a.askMu.Unlock()
		// 呈现者撤销 = 该提问不再可答(其它渠道已答/超时/回合取消)→ 待答栈同步出栈,
		// 否则状态栏会永久显示“待答 N”而输入框做着无效作答。
		if a.program != nil && a.started.Load() {
			a.program.Send(questionGoneMsg{id: q.ID})
		}
	}
	return ch, cancel, nil
}

// pendingQ 一个待答通道(与提问 id 绑定;定向回填用)。
type pendingQ struct {
	id string
	ch chan sdk.QuestionAnswer
}

// NoteInteraction 追加一条交互审计行(G-E5-4):宿主/插件订阅 confirm|question 事件后落进会话流。
// 典型用场:多端并存时告知“该审批/提问已在其它渠道处理”(只读展示,不影响会话事实)。
func (a *App) NoteInteraction(text string) {
	if text == "" || a.program == nil || !a.started.Load() {
		return
	}
	a.program.Send(interactionMsg{text: text})
}

// answerQuestion 用户作答(S-P0-2):按提问 id 定向回填对应通道(空 id = 回填全部,兼容旧调用),
// 并出栈清理。多问并存时不会把同一作答发给其它提问。
func (a *App) answerQuestion(id string, ans sdk.QuestionAnswer) {
	a.askMu.Lock()
	var hit []chan sdk.QuestionAnswer
	rest := a.qPend[:0]
	for _, p := range a.qPend {
		if p.id == id || id == "" {
			hit = append(hit, p.ch)
			continue
		}
		rest = append(rest, p)
	}
	a.qPend = rest
	a.askMu.Unlock()
	for _, ch := range hit {
		select {
		case ch <- ans:
		default:
		}
	}
}

// Start 启动 TUI(goroutine 跑 Run),挂接事件订阅。
func (a *App) Start() error {
	a.started.Store(true)
	// 会话事件 → UI
	d1 := a.c.Subscribe(sdk.EventSession, func(ctx context.Context, ev *sdk.Event) error {
		if sev, ok := ev.Payload.(*sdk.SessionEvent); ok {
			a.program.Send(sessionEventMsg{sev})
		}
		return nil
	})
	// agent/status → UI 状态栏
	d2 := a.c.Subscribe(sdk.EventAgentStatus, func(ctx context.Context, ev *sdk.Event) error {
		if s, ok := ev.Payload.(string); ok {
			a.program.Send(statusMsg{s})
		}
		return nil
	})
	// 会话/工作区切换事件(B3 命令下沉):宿主命令执行切换后 UI 经此重放刷新
	// (替代原命令内 afterSessionSwitch 直调——判重跳过后命令走宿主版本)
	d3 := a.c.Subscribe("cwd/session-switched", func(context.Context, *sdk.Event) error {
		a.onSessionSwitched()
		return nil
	})
	d4 := a.c.Subscribe("cwd/workspace-switched", func(context.Context, *sdk.Event) error {
		a.onSessionSwitched()
		return nil
	})
	// NOND-N1 提示 → 状态栏(订阅回调在总线 goroutine,经 program.Send 递进 UI 循环)
	d5 := a.c.Subscribe(sdk.EventNotice, func(_ context.Context, ev *sdk.Event) error {
		switch p := ev.Payload.(type) {
		case *sdk.Notice:
			a.program.Send(noticeMsg{p})
		case sdk.Notice:
			n := p
			a.program.Send(noticeMsg{&n})
		}
		return nil
	})
	a.subs = []sdk.Disposer{d1, d2, d3, d4, d5}

	go func() {
		_, err := a.program.Run()
		if err != nil {
			a.model.state.SetError("TUI 退出异常: " + err.Error())
		}
		// UI 退出 = 应用退出(main 监听 system/shutdown 后关停)
		a.c.Emit(context.Background(), "system/shutdown", nil, sdk.Emit)
	}()
	return nil
}

// Close 撤销订阅并退出程序。
func (a *App) Close() {
	a.started.Store(false)
	for _, d := range a.subs {
		d()
	}
	a.program.Quit()
}

// submit 普通输入:异步跑一轮(持有取消句柄,Esc 中断)。
// 提交瞬间同步置运行态(状态栏立即显示旋转 logo + 思考中,不等 agent/status 事件广播),
// 回合结束(agentDoneMsg)再回空闲。
func (a *App) submit(input string) {
	// S-P2-4 「!」shell 直通:以 ! 开头 → 沙箱/策略管线内执行并就地回显,不起模型回合
	// (不进上下文、不写会话账本;见 shell.go 文件头的三条硬约束)。
	if cmdStr, ok := isShellPassthrough(input); ok {
		// 注册取消句柄:长命令可 Esc 中断(与回合同一条取消链;进程被杀)
		ctx, cancel := context.WithCancel(context.Background())
		a.cancelFn.Store(&cancel)
		a.model.state.Running = true // 状态栏显示进行中(便于察觉长命令)
		a.model.state.LastTool = shellPassthroughTool
		a.runShellPassthrough(ctx, cmdStr)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelFn.Store(&cancel)
	a.model.state.Running = true
	a.model.state.LastTool = ""
	go func() {
		err := a.loop.Run(ctx, input)
		a.cancelFn.Store(nil)
		a.program.Send(agentDoneMsg{err})
	}()
}

// cycleThinking Tab(前进)/Shift+Tab(后退)循环思考等级(off→low→medium→high);
// 写会话级 + TUI 显示,状态栏感知当前等级。
func (a *App) cycleThinking(dir int) {
	next := nextThinking(a.llm.Thinking(), dir)
	a.llm.SetThinking(next)
	a.model.state.Thinking = next.String()
}

// nextThinking 思考等级循环(纯函数):dir=1 前进(off→low→medium→high),-1 后退。

func nextThinking(cur sdk.ThinkingLevel, dir int) sdk.ThinkingLevel {
	names := sdk.ThinkingLevel(0).Names()
	n := (int(cur) + dir + len(names)) % len(names)
	return sdk.ThinkingLevel(n)
}

// workspaceName 当前工作区目录名(状态栏显示;取不到时空串)。
func workspaceName() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Base(wd)
}

// cancelCurrent 取消进行中的回合(取消链:turn → LLM 流 → 工具进程,见设计 §8)。
func (a *App) cancelCurrent() {
	if c := a.cancelFn.Load(); c != nil {
		(*c)()
	}
}

// command 处理 / 命令:查注册表分发(内部命令与插件命令统一;
// host-commands 未装配时命令不可用,显式提示)。Run 输出文本显示为 meta 行。
func (a *App) command(raw string) error {
	if a.cmds == nil {
		return errString("命令不可用: ctx.commands 未装配(host-commands)")
	}
	fields := sdk.SplitArgs(strings.TrimPrefix(raw, "/"))
	if len(fields) == 0 {
		return nil
	}
	spec, ok := a.cmds.Get(fields[0])
	if !ok {
		return errString("未知命令 /" + fields[0] + "(输入 /help 查看全部)")
	}
	out, err := spec.Run(fields[1:])
	if err != nil {
		return err
	}
	if out != "" {
		a.model.state.Lines = append(a.model.state.Lines, Line{Kind: "meta", Text: out})
	}
	return nil
}

func (a *App) cmdApproval(args []string) (string, error) {
	var ap sdk.ApprovalService
	if err := a.c.Inject("ctx.approval", &ap); err != nil {
		return "", errString("ctx.approval 未装配: " + err.Error())
	}
	if len(args) < 1 {
		return approvalStatusText(ap.Mode(), a.sandboxOrNil()), nil
	}
	var mode sdk.ApprovalMode
	switch args[0] {
	case "open":
		mode = sdk.ApprovalOpen
	case "smart":
		mode = sdk.ApprovalSmart
	case "strict":
		mode = sdk.ApprovalStrict
	default:
		return "", errString("/approval open|smart|strict")
	}
	ap.SetMode(mode)
	a.model.state.Approval = string(mode)
	a.refreshSandboxDisplay() // 审批档是沙箱有效档的权威来源:改档必须同步状态栏
	prefs.SetApproval(string(mode))
	return approvalStatusText(mode, a.sandboxOrNil()), nil
}

func (a *App) cmdSandbox(args []string) (string, error) {
	var sb sdk.Sandbox
	if err := a.c.Inject("ctx.sandbox", &sb); err != nil {
		return "", errString("ctx.sandbox 未装配: " + err.Error())
	}
	if len(args) < 1 {
		return sandboxStatusText(sb, a.approvalMode()), nil
	}
	// /sandbox sync [on|off]:审批档 → 沙箱有效档 的联动开关(R10 ②-2)。
	// 无参只回显;开关关掉后沙箱档位独立生效,不再被 approval 覆盖(可见性由 ②-1 解决,
	// 这里解决可控性:此前只能改 config 重启)。
	if args[0] == "sync" {
		return a.sandboxSync(args[1:])
	}
	var mode sdk.SandboxMode
	switch args[0] {
	case "ro":
		mode = sdk.SandboxReadOnly
	case "ws":
		mode = sdk.SandboxWorkspace
	case "full":
		mode = sdk.SandboxFullAccess
	default:
		return "", errString("/sandbox ro|ws|full|sync [on|off]")
	}
	sb.SetMode(mode)
	a.model.state.Sandbox = sandboxDisplay(sb)
	prefs.SetSandbox(string(mode)) // 退出即记(与 Web 共享偏好)
	return sandboxSetText(sb, a.approvalMode()), nil
}

// sandboxSync 联动开关子命令(无参回显 / on|off 切换 + 退出即记偏好)。
func (a *App) sandboxSync(args []string) (string, error) {
	sb := a.sandboxOrNil()
	if sb == nil {
		return "", errString("ctx.sandbox 未装配: 无法查看联动开关")
	}
	sc, ok := sb.(sdk.SandboxSync)
	if !ok {
		return "", errString("该沙箱不支持联动开关(仅声明档)")
	}
	if len(args) < 1 {
		return sandboxSyncText(sb, a.approvalMode()), nil
	}
	var on bool
	switch args[0] {
	case "on":
		on = true
	case "off":
		on = false
	default:
		return "", errString("/sandbox sync on|off")
	}
	sc.SetSyncEnabled(on)
	prefs.SetSandboxSync(on)  // 退出即记(与 Web 共享偏好)
	a.refreshSandboxDisplay() // 联动变化会改有效档:状态栏必须同步
	return sandboxSyncSetText(sb, on, a.approvalMode()), nil
}

// sandboxOrNil 宽松取沙箱服务(未装配返回 nil:档位回显可降级)。
func (a *App) sandboxOrNil() sdk.Sandbox {
	var sb sdk.Sandbox
	if err := a.c.Inject("ctx.sandbox", &sb); err != nil {
		return nil
	}
	return sb
}

// approvalMode 当前审批档(未装配返回空串;仅用于回显来源标注)。
func (a *App) approvalMode() sdk.ApprovalMode {
	var ap sdk.ApprovalService
	if err := a.c.Inject("ctx.approval", &ap); err != nil || ap == nil {
		return ""
	}
	return ap.Mode()
}

// refreshSandboxDisplay 刷新状态栏沙箱段(档位联动后有效档会变)。
func (a *App) refreshSandboxDisplay() {
	if sb := a.sandboxOrNil(); sb != nil {
		a.model.state.Sandbox = sandboxDisplay(sb)
	}
}

// sandboxDisplay 状态栏沙箱段:声明档;被联动覆盖时附有效档与来源标注。
// 只读 Mode() 会把 approval=open 下的全放行说成 workspace-write(显示与行为不一致),
// 故实现 sdk.EffectiveSandbox 时以有效档为准。
func sandboxDisplay(sb sdk.Sandbox) string {
	declared := string(sb.Mode())
	es, ok := sb.(sdk.EffectiveSandbox)
	if !ok {
		return declared
	}
	eff := string(es.EffectiveMode())
	if eff == declared {
		return declared
	}
	return declared + "→" + eff + "(审批联动)"
}

// sandboxSyncLevel /sandbox 二级参数(sync → on|off;其它子命令无二级,选完即执行)。
func sandboxSyncLevel(picked []string) []sdk.Option {
	if len(picked) < 2 || picked[1] != "sync" {
		return nil
	}
	return []sdk.Option{{Value: "on", Desc: "开启联动:审批档覆盖沙箱有效档"}, {Value: "off", Desc: "关闭联动:沙箱档位独立生效"}}
}

// sandboxSyncText 联动开关回显:开关状态 + 它此刻是否真在覆盖。
// 覆盖与否以 EffectiveMode() 实报为准(不按 approval 猜),语义与沙箱档位回显同一纪律。
func sandboxSyncText(sb sdk.Sandbox, approval sdk.ApprovalMode) string {
	sc, ok := sb.(sdk.SandboxSync)
	if !ok {
		return "沙箱联动: 该沙箱不支持联动开关(仅声明档)"
	}
	if !sc.SyncEnabled() {
		return "沙箱联动: off;沙箱档位独立生效,不被审批档覆盖(当前有效档 " + string(sb.Mode()) + ")"
	}
	if es, ok := sb.(sdk.EffectiveSandbox); ok {
		if eff := string(es.EffectiveMode()); eff != string(sb.Mode()) {
			return "沙箱联动: on;当前有效档 " + eff + "(" + approvalSource(approval) + ")"
		}
	}
	return "沙箱联动: on"
}

// sandboxSyncSetText 联动开关切换回显(切完立刻报当前有效档:生效与否一眼可见)。
func sandboxSyncSetText(sb sdk.Sandbox, on bool, approval sdk.ApprovalMode) string {
	if !on {
		return "沙箱联动 -> off;沙箱档位独立生效(当前有效档 " + string(sb.Mode()) + ")"
	}
	if es, ok := sb.(sdk.EffectiveSandbox); ok {
		if eff := string(es.EffectiveMode()); eff != string(sb.Mode()) {
			return "沙箱联动 -> on;当前有效档 " + eff + "(" + approvalSource(approval) + ")"
		}
	}
	return "沙箱联动 -> on"
}

// sandboxStatusText 沙箱档位回显(与 host-internal-commands 同文案):声明档 + 有效档。
func sandboxStatusText(sb sdk.Sandbox, approval sdk.ApprovalMode) string {
	declared := string(sb.Mode())
	es, ok := sb.(sdk.EffectiveSandbox)
	if !ok {
		return "沙箱: " + declared
	}
	eff := string(es.EffectiveMode())
	if eff == declared {
		return "沙箱: " + declared + "(有效一致)"
	}
	return "沙箱: " + declared + ";有效: " + eff + "(联动来源 " + approvalSource(approval) + ")"
}

// sandboxSetText 切档回显:被联动覆盖时显式提示(不再静默失效)。
func sandboxSetText(sb sdk.Sandbox, approval sdk.ApprovalMode) string {
	declared := string(sb.Mode())
	es, ok := sb.(sdk.EffectiveSandbox)
	if !ok {
		return "沙箱 -> " + declared
	}
	eff := string(es.EffectiveMode())
	if eff == declared {
		return "沙箱 -> " + declared
	}
	return "沙箱 -> " + declared + ";注意:联动覆盖生效,当前有效档 " + eff + "(" + approvalSource(approval) + "),该设置暂不生效"
}

// approvalSource 联动来源标注(approval=open → "approval=open";未装配 → "审批档联动")。
// 只标注来源,不推断覆盖结果——覆盖结果以 EffectiveMode() 实报为准。
func approvalSource(approval sdk.ApprovalMode) string {
	if approval == "" {
		return "审批档联动"
	}
	return "approval=" + string(approval)
}

// approvalStatusText 审批档回显:档位 + 它对沙箱有效档的影响。
func approvalStatusText(mode sdk.ApprovalMode, sb sdk.Sandbox) string {
	txt := "审批: " + string(mode)
	if sb == nil {
		return txt
	}
	if es, ok := sb.(sdk.EffectiveSandbox); ok {
		return txt + ";沙箱有效: " + string(es.EffectiveMode())
	}
	switch mode {
	case sdk.ApprovalOpen:
		return txt + "(联动开启时沙箱有效档 = full-access)"
	case sdk.ApprovalStrict:
		return txt + "(联动开启时沙箱有效档 = read-only)"
	}
	return txt
}

func (a *App) cmdPlugins(args []string) (string, error) {
	var mgr sdk.PluginManager
	if err := a.c.Inject("ctx.pluginManager", &mgr); err != nil {
		return "", errString("ctx.pluginManager 未装配: " + err.Error())
	}
	if len(args) < 1 {
		return pluginRows(mgr, a.pluginHome()), nil
	}
	switch args[0] {
	case "on", "load":
		if len(args) < 2 {
			return "", errString("/plugins on <id>")
		}
		if err := mgr.Load(args[1]); err != nil {
			return "", errString(err.Error())
		}
		// 持久化开关:patch-runtime.yaml 记录 enabled:true,重启发仍生效
		if err := a.persistPlugin(args[1], true); err != nil {
			return "", errString("已加载,但持久化失败: " + err.Error())
		}
		return "已加载并持久启用 " + args[1] + "(重启仍生效)", nil
	case "off", "unload":
		if len(args) < 2 {
			return "", errString("/plugins off <id>")
		}
		if err := mgr.Unload(args[1]); err != nil {
			return "", errString(err.Error())
		}
		// 持久化开关:patch-runtime.yaml 记录 enabled:false,重启仍关闭
		if err := a.persistPlugin(args[1], false); err != nil {
			return "", errString("已卸载,但持久化失败: " + err.Error())
		}
		return "已卸载并持久关闭 " + args[1] + "(重启仍关闭)", nil
	case "default":
		if len(args) < 2 {
			return "", errString("/plugins default <id>")
		}

		if err := install.RemoveEntry(install.RuntimePatch(a.pluginHome()), args[1]); err != nil {
			return "", errString(err.Error())
		}
		return "已清除持久覆盖 " + args[1] + "(恢复配置树默认,重启生效)", nil
	case "list", "":
		return pluginRows(mgr, a.pluginHome()), nil
	default:
		return "", errString("/plugins list|on|off|default <id>")
	}
}

// pluginRows 插件列表文本(含持久开关后缀)。
func pluginRows(mgr sdk.PluginManager, home string) string {
	rows := "插件:"
	persist := install.ReadEnablements(install.RuntimePatch(home))
	for _, info := range mgr.List() {
		suffix := ""
		if on, ok := persist[info.ID]; ok {
			if on {
				suffix = " (持久开)"
			} else {
				suffix = " (持久关)"
			}
		}
		rows += "\n  " + info.ID + " [" + info.Type + "] " + info.State + suffix
	}
	return rows
}

// persistPlugin 持久化插件开关:写入 patch-runtime.yaml 并让全部 profile 引用(重启生效)。
func (a *App) persistPlugin(id string, enabled bool) error {
	patched := install.RuntimePatch(a.pluginHome())
	if err := install.EnsurePatch(patched, install.Entry{ID: id, Enabled: enabled}); err != nil {
		return err
	}
	return install.EnsureProfileRef(a.pluginHome(), "patch-runtime.yaml")
}

// pluginHome 运行时数据根(GAH_HOME,与 boot 一致;~/.gah 兜底已弃用 2026-09;
// 空仅嵌入/单测,宁回 TempDir 也不落 cwd/根)。
func (a *App) pluginHome() string {
	if h := os.Getenv("GAH_HOME"); h != "" {
		return h
	}
	return os.TempDir()
}

func (a *App) cmdSettings(args []string) (string, error) {
	var sessions sdk.SessionLog
	if err := a.c.Inject("ctx.sessions", &sessions); err != nil {
		return "", errString("ctx.sessions 未装配")
	}
	if len(args) < 2 || args[0] != "history" {
		return "", errString("/settings history N|off|unlimited")
	}
	var n int
	switch args[1] {
	case "off":
		n = -1
	case "unlimited":
		n = 0
	default:
		if _, err := fmt.Sscanf(args[1], "%d", &n); err != nil || n < 0 {
			return "", errString("/settings history N|off|unlimited")
		}
	}
	sessions.SetHistory(n)
	prefs.SetHistory(n) // 全局历史注入偏好(会话级 sidecar 之外,跨新会话记忆)
	return "/settings history -> " + args[1], nil
}

// forkSessions 注入 ForkableSessions(host-cwd-sessions 实现;/fork /clone 依赖)。
func (a *App) forkableSessions() (sdk.ForkableSessions, error) {
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return nil, errString("ctx.cwdSessions 未装配")
	}
	fs, ok := cs.(sdk.ForkableSessions)
	if !ok {
		return nil, errString("会话分支不可用: CwdSessions 未实现 ForkableSessions(host-cwd-sessions)")
	}
	return fs, nil
}

// lastUserSeq 当前会话最近提问 seq(/fork 缺省分支点;无提问返回 0)。
func lastUserSeq(fs sdk.ForkableSessions, cur string) uint64 {
	pts, err := fs.ForkPoints(cur)
	if err != nil || len(pts) == 0 {
		return 0
	}
	return pts[len(pts)-1].Seq
}

// forkSeqOptions /fork 一级枚举:当前会话可分支提问点(seq + 摘要,最新在前,最多 12 项);
// 无提问点/服务缺失 = 空(选择器回退 FreeArgs 手输 seq)。
func (a *App) forkSeqOptions([]string) []sdk.Option {
	fs, err := a.forkableSessions()
	if err != nil {
		return nil
	}
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return nil
	}
	pts, err := fs.ForkPoints(cs.CurrentSession())
	if err != nil || len(pts) == 0 {
		return nil
	}
	if len(pts) > 12 {
		pts = pts[len(pts)-12:]
	}
	opts := make([]sdk.Option, 0, len(pts))
	for i := len(pts) - 1; i >= 0; i-- {
		opts = append(opts, sdk.Option{Value: strconv.FormatUint(pts[i].Seq, 10), Desc: forkPointDesc(pts[i])})
	}
	return opts
}

// forkPointDesc 提问点一行摘要(seq + 文本截断 40 字)。
func forkPointDesc(p sdk.ForkPoint) string {
	s := strings.Join(strings.Fields(p.Text), " ")
	if r := []rune(s); len(r) > 40 {
		s = string(r[:39]) + "…"
	}
	if s == "" {
		s = "(无文本)"
	}
	return fmt.Sprintf("seq %d · %s", p.Seq, s)
}

// cmdFork /fork [seq]:从历史 seq 处派生分支会话(继承到该点),切换过去从该点续聊。
func (a *App) cmdFork(args []string) (string, error) {
	fs, err := a.forkableSessions()
	if err != nil {
		return "", err
	}
	var cs sdk.CwdSessions
	_ = a.c.Inject("ctx.cwdSessions", &cs)
	cur := cs.CurrentSession()
	seq := uint64(0)
	if len(args) >= 1 {
		n, perr := strconv.ParseUint(args[0], 10, 64)
		if perr != nil {
			return "", errString("/fork [seq]: seq 须为数字(/tree 查看各会话提问点)")
		}
		seq = n
	} else {
		seq = lastUserSeq(fs, cur)
		if seq == 0 {
			return "", errString("当前会话无提问点可分支(先发消息或 /tree 查 seq)")
		}
	}
	id, err := fs.ForkAt(seq)
	if err != nil {
		return "", errString(err.Error())
	}
	a.afterSessionSwitch(cs)
	return "已从 seq " + fmt.Sprintf("%d", seq) + " 派生分支会话 " + id +
		"(继承到该点历史;后续对话只写本分支;切换回源:/session switch)", nil
}

// cmdClone /clone:复制当前会话(同一分支另一路演进),切换过去继续。
func (a *App) cmdClone(_ []string) (string, error) {
	fs, err := a.forkableSessions()
	if err != nil {
		return "", err
	}
	var cs sdk.CwdSessions
	_ = a.c.Inject("ctx.cwdSessions", &cs)
	id, err := fs.CloneCurrent()
	if err != nil {
		return "", errString(err.Error())
	}
	a.afterSessionSwitch(cs)
	return "已复制当前会话为分支 " + id + "(独立演进;切换回源:/session switch)", nil
}

// cmdTree /tree:会话分支树——树形展示派生关系(fork/clone 溯源,P5.2-B3)+ 每会话
// 可 fork 的提问点(seq + 摘要)。树由 host-cwd-sessions fork-tree.json 派生关系组装;
// 无派生记录时回退平铺(各会话独立成根),行为向后兼容。
func (a *App) cmdTree(_ []string) (string, error) {
	fs, err := a.forkableSessions()
	if err != nil {
		return "", err
	}
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return "", errString("ctx.cwdSessions 未装配")
	}
	nodes, _ := fs.ForkTree()
	cur := cs.CurrentSession()
	// 节点显示名表 + 当前标记
	disp := map[string]string{}
	curMark := map[string]bool{}
	for _, si := range cs.Sessions() {
		name := si.Name
		if name == "" {
			if si.ID == "" {
				name = "主会话"
			} else {
				name = si.ID
			}
		}
		disp[si.ID] = name
		curMark[si.ID] = si.ID == cur
	}
	body := treeRenderNodes(nodes, disp, curMark, fs.ForkPoints)
	body += "  (分支: /fork [seq] 从此点派生;复制当前: /clone;切换:/session switch)"
	return "会话分支树:\n" + body, nil
}

// treeRenderNodes 会话分支树渲染(纯函数,可单测):由派生关系 nodes 组装树并输出文本。
// 根判定:无父 / 父不在节点集 → 根;完全无记录 → 按 disp 全部平铺为根(向后兼容)。
// 递归深度 ≤12 + visited 防环。forkPts 注入各会话分支点列表(错误忽略→空)。
func treeRenderNodes(nodes []sdk.ForkNode, disp map[string]string, curMark map[string]bool,
	forkPts func(id string) ([]sdk.ForkPoint, error)) string {
	children := map[string][]sdk.ForkNode{}
	known := map[string]bool{}
	for _, n := range nodes {
		known[n.ID] = true
	}
	for _, n := range nodes {
		children[n.Parent] = append(children[n.Parent], n)
	}
	// 根判定:主会话(id 空)恒为根且其派生挂其下;其它节点 Parent 非空但父不在
	// 节点集(孤儿/记录不全)独立成根;完全无记录 → 平铺 disp(向后兼容)。
	roots := []string{}
	for _, n := range nodes {
		if n.ID == "" {
			roots = append(roots, "") // 主会话根(派生经 children[""] 收录,不重复)
			continue
		}
		if n.Parent == "" {
			// fork 自主(父=主会话):主节点在 known 则挂其下(children 已收录,非根);
			// 节点集缺主(异常)→ 独立成根兜底
			if !known[""] {
				roots = append(roots, n.ID)
			}
			continue
		}
		if !known[n.Parent] {
			roots = append(roots, n.ID) // 孤儿(父记录缺失):独立根
		}
	}
	if len(roots) == 0 {
		for id := range disp {
			roots = append(roots, id)
		}
	}
	sort.Strings(roots)
	var b strings.Builder
	var walk func(id string, depth int, visited map[string]bool)
	walk = func(id string, depth int, visited map[string]bool) {
		if depth > 12 || visited[id] {
			return
		}
		visited[id] = true
		pad := strings.Repeat("    ", depth)
		if depth > 0 {
			pad += "└─ "
		}
		mark := "  "
		if curMark[id] {
			mark = "★"
		}
		name := disp[id]
		if name == "" {
			if id == "" {
				name = "主会话"
			} else {
				name = id
			}
		}
		var pts []sdk.ForkPoint
		if forkPts != nil {
			pts, _ = forkPts(id)
		}
		fmt.Fprintf(&b, "  %s %s %s(提问 %d 个;/fork 取 seq)\n", pad, mark, name, len(pts))
		start := 0
		if len(pts) > 5 {
			start = len(pts) - 5
			fmt.Fprintf(&b, "  %s    …(更早 %d 个,/tree 截断)\n", pad, start)
		}
		for _, pt := range pts[start:] {
			fmt.Fprintf(&b, "  %s    #%d %s\n", pad, pt.Seq, pt.Text)
		}
		kids := append([]sdk.ForkNode{}, children[id]...)
		sort.Slice(kids, func(i, j int) bool { return kids[i].ID < kids[j].ID })
		for _, k := range kids {
			walk(k.ID, depth+1, visited)
		}
	}
	for _, id := range roots {
		walk(id, 0, map[string]bool{})
	}
	return b.String()
}

func (a *App) cmdSessions() (string, error) {
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return "", errString("ctx.cwdSessions 未装配: " + err.Error())
	}
	// F 组:F2 置顶 ★ + F3 概述一行(只读缓存,不触发模型)
	infos := cs.Sessions()
	if len(infos) == 0 {
		return "当前项目: " + cs.Current() + "\n已有会话: (无)", nil
	}
	var sb strings.Builder
	sb.WriteString("当前项目: " + cs.Current() + "\n会话列表(★ = 置顶):")
	cur := cs.CurrentSession()
	for _, si := range infos {
		mark := " "
		if si.Pinned {
			mark = "★"
		}
		now := ""
		if si.ID == cur {
			now = " ←当前"
		}
		label := orDefault(si.Name, orDefault(si.ID, "主会话"))
		line := "\n" + mark + " " + label
		if si.MTime > 0 {
			line += " · " + time.Unix(si.MTime, 0).Format("01-02 15:04")
		}
		line += " · " + fmt.Sprint(si.Frames) + " 条" + now
		if si.Summary != "" {
			line += "\n    " + truncWidthRunes(si.Summary, 60)
			if si.SummaryState == "stale" {
				line += " (待更新)"
			}
		}
		sb.WriteString(line)
	}
	sb.WriteString("\n(/session summary 生成概述;/session pin|unpin 置顶)")
	return sb.String(), nil
}

// truncWidthRunes 按 rune 截断(TUI 文本列;中文不切半字)。
func truncWidthRunes(s string, n int) string {
	if n < 1 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// cmdCompact /compact [指示词]:手动触发滚动摘要压缩(经会话日志 CompactService)。
// 结果回显摘要;无可折叠/未启用明确提示,不静默降级。
func (a *App) cmdCompact(args []string) (string, error) {
	var sessions sdk.SessionLog
	if err := a.c.Inject("ctx.sessions", &sessions); err != nil {
		return "", errString("ctx.sessions 未装配")
	}
	cs, ok := sessions.(sdk.CompactService)
	if !ok {
		return "", errString("手动压缩不可用: 会话日志未实现 CompactService(host-session-log)")
	}
	prompt := strings.Join(args, " ")
	summary, folded, err := cs.Compact(prompt)
	if err != nil {
		return "", errString("/compact: " + err.Error())
	}
	if folded <= 0 {
		return "无可压缩历史(会话较短或已是最新;超出预算时仍会自动压缩)", nil
	}
	return compactSummaryLine(summary, folded), nil
}

// compactSummaryLine 压缩结果回显文案(折叠事件数 + 单行截断摘要;纯函数可测)。
func compactSummaryLine(summary string, folded int) string {
	s := strings.Join(strings.Fields(summary), " ") // 换行压空格
	if n := len([]rune(s)); n > 160 {
		s = string([]rune(s)[:159]) + "…"
	}
	return fmt.Sprintf("已折叠 %d 条事件为滚动摘要。当前摘要: %s", folded, s)
}

// AddWidget 注册一条输入区上方 widget(宿主/未来插件;Text 每次渲染求值,空返回不显示)。
func (a *App) AddWidget(id string, text func() string) {
	if id == "" || text == nil {
		return
	}
	a.widgets = append(a.widgets, Widget{ID: id, Text: text})
}

// cmdWidgets /widgets on|off:输入区上方 widget 区开关(无参默认开启)。
// cmdReload /reload:热重载指令文件(全局/多级项目/附加 AGENTS.md)。
// 外部编辑无需重启;读取失败保留旧值(错误回滚)并显式提示。
func (a *App) cmdReload(_ []string) (string, error) {
	var sp sdk.SystemPromptService
	if err := a.c.Inject("ctx.systemPrompt", &sp); err != nil {
		return "", errString("ctx.systemPrompt 未装配")
	}
	rl, ok := sp.(sdk.ReloadableInstructions)
	if !ok {
		return "", errString("指令重载不可用: SystemPromptService 未实现 ReloadableInstructions")
	}
	if err := rl.ReloadInstructions(); err != nil {
		return "", errString("/reload 失败(旧值保留): " + err.Error())
	}
	return "已热重载指令文件(全局/项目层级/附加;下次回合的 system prompt 生效)", nil
}

func (a *App) cmdWidgets(args []string) (string, error) {
	if len(args) > 0 && args[0] == "off" {
		a.model.state.WidgetOn = false
		return "widget 区已关闭(输入行上方空间交还主区)", nil
	}
	a.model.state.WidgetOn = true
	if len(a.widgets) == 0 {
		return "widget 区已开启(当前无宿主注册条目;未来插件/内部功能可 AddWidget)", nil
	}
	return fmt.Sprintf("widget 区已开启(%d 条已注册,输入行上方显示)", len(a.widgets)), nil
}

// answerSkipValue 选择器“跳过作答”哨兵值(与选项 Value 不可能撞车;命令面亦接受字面量 skip)。
const answerSkipValue = "__skip__"

// cmdAnswer S-P0-2 作答命令(无参 = 回到作答态并展示当前提问):
//
//	/answer          → 进入作答态(栈首提问;选项由选择器列出,亦可直接输入内容)
//	/answer <编号|值|说明> → 直接作答(解析同输入框作答;多选逗号分隔)
//	/answer skip     → 跳过(回填空作答,不强迫作答)
//
// 多问并存时按栈序逐个作答(栈首 = 最早到达的阻塞提问),不提供乱序跳答(保持语义简单)。
func (a *App) cmdAnswer(args []string) (string, error) {
	p := a.model.state.ActiveQuestion()
	if p == nil {
		return "当前没有待答提问", nil
	}
	if len(args) == 0 {
		a.model.state.Answering = true
		return "进入作答态(回车提交;Esc 退出,提问保持等待)", nil
	}
	arg := strings.Join(args, " ")
	if arg == "skip" || arg == answerSkipValue {
		a.model.resolveQuestion(p.ID, sdk.QuestionAnswer{})
		return fmt.Sprintf("已跳过作答(待答 %d 条)", len(a.model.state.Questions)), nil
	}
	ans, ok := parseTUIAnswer(p.Q, arg)
	if !ok {
		return "", fmt.Errorf("无法识别作答(%q):回复编号/选项值或直接输入内容", arg)
	}
	a.model.resolveQuestion(p.ID, ans)
	return "已作答", nil
}

// answerOptions 作答选择器选项(栈首提问的编号选项 + 跳过);无待答返回 nil。
func (a *App) answerOptions([]string) []sdk.Option {
	p := a.model.state.ActiveQuestion()
	if p == nil {
		return nil
	}
	opts := make([]sdk.Option, 0, len(p.Q.Options)+1)
	for i, o := range p.Q.Options {
		d := o.Desc
		if d == "" {
			d = o.Value
		}
		opts = append(opts, sdk.Option{Value: fmt.Sprint(i + 1), Desc: d})
	}
	opts = append(opts, sdk.Option{Value: answerSkipValue, Desc: "跳过(空作答,不强迫作答)"})
	return opts
}

func (a *App) cmdExport(args []string) (string, error) {
	var sessions sdk.SessionLog
	if err := a.c.Inject("ctx.sessions", &sessions); err != nil {
		return "", errString("ctx.sessions 未装配")
	}
	// 默认导出到当前会话存档路径(host-cwd-sessions),可指定 /export <path>
	path := ""
	if len(args) > 0 {
		path = args[0]
	} else {
		var cs sdk.CwdSessions
		if err := a.c.Inject("ctx.cwdSessions", &cs); err == nil {
			path = cs.Path()
		}
	}
	evs := sessions.Replay()
	if path == "" {
		// 无落盘配置:仅统计(兜底)
		return "会话事件数: " + fmt.Sprint(len(evs)), nil
	}
	var sb strings.Builder
	for _, ev := range evs {
		b, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", errString("导出失败: " + err.Error())
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return "", errString("导出失败: " + err.Error())
	}
	return fmt.Sprintf("已导出 %d 条事件 → %s", len(evs), path), nil
}

type errString string

func (e errString) Error() string { return string(e) }

// cmdSession /session list|switch|new|current:列出/切换/新建/查看会话(会话管理统一入口)。
// switch 二级枚举(选择器)选会话进入;切换后清流、重放该会话历史(继续上下文可见)、
// 重置 token 统计(新会话从零累计),状态栏显示当前会话 id。
func (a *App) cmdSession(args []string) (string, error) {
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return "", errString("ctx.cwdSessions 未装配: " + err.Error())
	}
	if len(args) < 1 {
		return "", errString("/session list|switch|new|current")
	}
	switch args[0] {
	case "list":
		return a.cmdSessions() // 列出已有会话文件(原 /sessions)
	case "current":
		return "当前项目: " + cs.Current() +
			"\n当前会话: " + orDefault(sessionLabel(cs), "主会话") +
			"\n落盘: " + cs.Path(), nil
	case "new":
		id, err := cs.New()
		if err != nil {
			return "", errString(err.Error())
		}
		a.afterSessionSwitch(cs)
		return "已新建会话 " + id + "(空历史,后续对话记入新会话)", nil
	case "switch":
		if len(args) < 2 {
			return "", errString("/session switch <会话 id>(二级选择或手动输入;main=主会话)")
		}
		id := args[1]
		if id == "main" {
			id = "" // 主会话(跨期共享历史)
		}
		if err := cs.Open(id); err != nil {
			return "", errString("切换失败: " + err.Error())
		}
		a.afterSessionSwitch(cs)
		return "已切换到会话 " + orDefault(cs.CurrentSession(), "主会话"), nil
	case "pin", "unpin":
		// F 组 F2:置顶/取消置顶(缺 id = 当前会话;上限 8 显式报错)
		id := cs.CurrentSession()
		if len(args) >= 2 && args[1] != "" && args[1] != "current" {
			id = args[1]
			if id == "main" {
				id = ""
			}
		}
		if err := cs.SetPinned(id, args[0] == "pin"); err != nil {
			return "", errString(err.Error())
		}
		if args[0] == "pin" {
			return "已置顶会话 " + orDefault(labelOfSession(cs, id), "主会话") + "(列表顺序:置顶区在前)", nil
		}
		return "已取消置顶 " + orDefault(labelOfSession(cs, id), "主会话"), nil
	case "summary":
		// F 组 F3:生成/查看概述(会调用模型;force 语义 = 手动恒重新生成)
		var ss sdk.SessionSummaryService
		if err := a.c.Inject("ctx.sessionSummary", &ss); err != nil {
			return "", errString("会话概述未装配(ctx.sessionSummary / host-session-summary): " + err.Error())
		}
		id := cs.CurrentSession()
		if len(args) >= 2 && args[1] != "" && args[1] != "current" {
			id = args[1]
			if id == "main" {
				id = ""
			}
		}
		sum, err := ss.Summary(context.Background(), id, true)
		if err != nil {
			return "", errString("概述生成失败: " + err.Error())
		}
		out := "概述: " + sum.Text
		if len(sum.Topics) > 0 {
			out += "\n主题: " + strings.Join(sum.Topics, " / ")
		}
		if sum.Model != "" {
			out += "\n(模型 " + sum.Model + ";仅展示,不进入后续上下文)"
		}
		return out, nil
	default:
		return "", errString("/session list|switch|new|current|pin|unpin|summary")
	}
}

// labelOfSession 按 id 取展示名(名优先,回退 id/主会话)。
func labelOfSession(cs sdk.CwdSessions, id string) string {
	for _, si := range cs.Sessions() {
		if si.ID == id {
			if si.Name != "" {
				return si.Name
			}
			break
		}
	}
	return id
}

// sessionSwitchOptions /session switch 的二级动态枚举:当前项目会话列表。
// 主会话用 main 标识(选择器选项 Value 非空);切换会话用其 id。
func (a *App) sessionSwitchOptions(picked []string) []sdk.Option {
	if len(picked) < 2 {
		return nil
	}
	switch picked[1] {
	case "switch", "pin", "unpin", "summary":
		// 均以会话为二级目标
	default:
		return nil // 其它分支无二级 → 直接执行
	}
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return nil // 未装配:无选项,回退手动输入
	}
	var opts []sdk.Option
	for _, si := range cs.Sessions() {
		v := si.ID
		if v == "" {
			v = "main"
		}
		opts = append(opts, sdk.Option{Value: v, Desc: sessionDesc(si)})
	}
	return opts
}

// sessionLabel 当前会话状态栏/展示标签:显示名优先,无名称回退会话 id
// (未命名主会话 id 为空 → 返回空串,状态栏不显示会话段)。
func sessionLabel(cs sdk.CwdSessions) string {
	if n := cs.SessionName(); n != "" {
		return n
	}
	return cs.CurrentSession()
}

// sessionDesc 会话选项描述:主会话标注跨期共享;切换会话带最后修改时间与事件条数;
// 显示名优先(名 + id/主会话),无名称回退 id。
func sessionDesc(si sdk.SessionInfo) string {
	main := si.ID == ""
	label := si.Name
	switch {
	case label == "" && main:
		label = "主会话"
	case label == "":
		label = "会话 " + si.ID
	case main:
		label += "(主会话)"
	}
	if !main {
		if si.MTime > 0 {
			label += " · " + time.Unix(si.MTime, 0).Format("01-02 15:04")
		}
		if si.Frames >= 0 {
			label += " · " + fmt.Sprint(si.Frames) + " 条"
		}
	}
	// P5.2 会话内容预览(对齐 web Sidebar Preview):首条用户消息截 20 字,
	// /session switch 选择器一眼识别会话内容(超长选择器行由渲染截断)。
	if si.Preview != "" {
		p := []rune(si.Preview)
		if len(p) > 20 {
			p = p[:20]
		}
		label += " ─ " + string(p)
	}
	return label
}

// afterSessionSwitch 切换会话后的界面同步:状态栏会话标签(名优先)、重置 token 统计、
// 清空 TUI 会话流并重放新会话历史(继续上下文可见)。
func (a *App) afterSessionSwitch(cs sdk.CwdSessions) {
	a.model.state.Workspace = workspaceName() // 工作区切换后 cwd 已更新,状态栏同步
	a.model.state.Session = sessionLabel(cs)
	var us sdk.UsageStatsService
	if err := a.c.Inject("ctx.usageStats", &us); err == nil {
		us.Reset() // 新会话从零累计(窗口保留)
		a.model.state.Stats = us.Stats()
	} else {
		a.model.state.Stats = sdk.UsageStats{}
	}
	a.model.state.Lines = nil
	a.model.state.Traj.Reset() // S-P0-1:轨迹随会话重置(随后 ApplyReplay 从头重建)
	a.model.state.LastTool = ""
	a.model.state.ClearQueue() // P4-1:切会话丢弃旧队列(防错发到新会话上下文)
	var sessions sdk.SessionLog
	if err := a.c.Inject("ctx.sessions", &sessions); err == nil {
		for _, ev := range sessions.Replay() {
			a.model.state.ApplyReplay(&ev) // 与启动重放同款:跳过轮次分隔行
		}
	}
	a.model.state.Lines = append(a.model.state.Lines,
		Line{Kind: "meta", Text: "—— 已切换到会话: " + orDefault(sessionLabel(cs), "主会话") + " ——"})
}

// onSessionSwitched 会话/工作区切换事件驱动刷新(B3 命令下沉后:宿主命令执行切换经
// cwd/session-switched|cwd/workspace-switched 广播,此处 Inject 服务取当前态重放。
// 替代原命令内 afterSessionSwitch 直调)。
func (a *App) onSessionSwitched() {
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return
	}
	a.afterSessionSwitch(cs)
}

// workspaceNewSentinel 选择器哨兵项:选中后进入二级自由断点输入新目录路径。
const workspaceNewSentinel = "__new_dir__"

// workspaceOptions /workspace 一级枚举:最近使用工作区(按最近使用时间倒序,
// 宿主已排)+ 哨兵“输入新目录路径…”。无记录 = nil(回退一级自由输入)。
func (a *App) workspaceOptions([]string) []sdk.Option {
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return nil
	}
	recs := cs.RecentProjects()
	if len(recs) == 0 {
		return nil
	}
	opts := make([]sdk.Option, 0, len(recs)+1)
	for _, r := range recs {
		opts = append(opts, sdk.Option{Value: r.Dir, Desc: workspaceTimeFmt(r.TS) + " · " + r.Key})
	}
	opts = append(opts, sdk.Option{Value: workspaceNewSentinel, Desc: "输入新目录路径…"})
	return opts
}

// workspaceTimeFmt 最近使用时间显示:今日 = HH:MM,更早 = MM-DD HH:MM。
func workspaceTimeFmt(ts int64) string {
	t := time.Unix(ts, 0)
	if time.Since(t) < 24*time.Hour && t.Day() == time.Now().Day() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// cmdWorkspace /workspace [目录]:切工作区(项目)。
// 流程:展开/校验目标目录 → os.Chdir(后续回合/新进程按新 cwd)→ 会话重绑
// (ctx.cwdSessions.SwitchProject,新建空会话:上下文切到新项目文件,旧项目历史经
// /session switch 回溯)→ 界面同步(afterSessionSwitch:清流/重放/统计重置)→
// 状态栏工作区名刷新。args 含哨兵(选择器“新路径”入口)时去掉哨兵取剩余路径。
// 注意(收敛版):沙箱 root 与已运行的外部工具进程 cwd 仍按启动工作区——
// 工具进程重启(host-bridge 重载/热更新)后按新 cwd;会话/上下文/展示即时切换。
func (a *App) cmdWorkspace(args []string) (string, error) {
	raw := strings.Join(args, " ")
	raw = strings.TrimSpace(strings.TrimPrefix(raw, workspaceNewSentinel))
	if raw == "" {
		return "", errString("/workspace [目录]")
	}
	if raw == "~" || strings.HasPrefix(raw, "~/") {
		if uh, err := os.UserHomeDir(); err == nil {
			raw = filepath.Join(uh, strings.TrimPrefix(raw, "~"))
		}
	}
	target := raw
	if !filepath.IsAbs(target) {
		abs, err := filepath.Abs(target)
		if err != nil {
			return "", errString("路径解析失败: " + err.Error())
		}
		target = abs
	}
	target = filepath.Clean(target)
	// 归一化符号链接与 Windows 8.3 短名:C:\Users\RUNNER~1\... 与
	// C:\Users\runneradmin\... 指向同一目录,但 os.Getwd 会原样返回 chdir 时的形态
	// → 同一目录派生出两个 ProjectKey(工作区历史重复、判等失败)。
	// 解析失败(不存在/无权限)回退原值,由下方 Stat 给出具体错误。
	if r, err := filepath.EvalSymlinks(target); err == nil {
		target = r
	}
	fi, err := os.Stat(target)
	if err != nil {
		return "", errString("/workspace: 目录不存在或不可访问: " + target)
	}
	if !fi.IsDir() {
		return "", errString("/workspace: 非目录: " + target)
	}
	if err := os.Chdir(target); err != nil {
		return "", errString("/workspace: chdir 失败: " + err.Error())
	}
	a.mFilesDir = "" // @ 引用文件索引缓存失效(下个 cwd 重新索引)
	a.mFiles = nil
	a.model.state.Workspace = workspaceName() // 状态栏工作区名随切换刷新
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err == nil {
		id, err := cs.SwitchProject(sdk.ProjectKeyFromCwd())
		if err != nil {
			return "", errString("/workspace: 会话切换失败: " + err.Error())
		}
		a.afterSessionSwitch(cs)
		return "已切换工作区 → " + target + "\n会话 key: " + cs.Current() +
			"(新会话 " + id + ",历史经 /session switch 回溯;工具进程 cwd 于重启后生效)", nil
	}
	return "已切换工作区(未装配 cwdSessions,仅 chdir)→ " + target, nil
}

// cmdThinking /thinking off|low|medium|high:设置会话级思考等级(Shift+Tab 循环同效)。
// applyPrefs 启动恢复上次退出偏好(思考/沙箱/历史;与 Web 共享 internal/prefs,
// model 由 providerfile 链自行恢复)。单条非法/服务缺失跳过,不阻塞 TUI 启动。
func (a *App) applyPrefs() {
	defer func() { _ = recover() }() // 偏好恢复非关键:测试/极简宿主缺实现时兜底不崩
	p := prefs.Load()
	// S-P2-4 状态栏项(纯本地状态赋值,放最前:后面沙箱/审批恢复失败也不影响它)
	// 过滤未知/重复项:设置时已严格校验,此处兼容手改偏好文件的旧值。
	if items := filterStatusline(p.Statusline); len(items) > 0 {
		a.model.state.Statusline = items
	}
	if p.Thinking != "" {
		if lvl := sdk.ParseThinking(p.Thinking); lvl.String() == p.Thinking {
			a.llm.SetThinking(lvl)
			a.model.state.Thinking = lvl.String()
		}
	}
	if p.Sandbox != "" {
		var sb sdk.Sandbox
		if err := a.c.Inject("ctx.sandbox", &sb); err == nil && sb != nil {
			sb.SetMode(sdk.SandboxMode(p.Sandbox))
			a.model.state.Sandbox = p.Sandbox
		}
	}
	if p.Approval != "" {
		var ap sdk.ApprovalService
		if err := a.c.Inject("ctx.approval", &ap); err == nil && ap != nil {
			ap.SetMode(sdk.ApprovalMode(p.Approval))
			a.model.state.Approval = p.Approval
		}
	}
	a.refreshSandboxDisplay() // 沙箱/审批偏好都恢复后再统一刷新有效档(两者共同决定)
	if p.History != nil {
		var sess sdk.SessionLog
		if err := a.c.Inject("ctx.sessions", &sess); err == nil && sess != nil {
			sess.SetHistory(*p.History)
		}
	}
}

// cmdStatusline /statusline [项...]|reset:状态栏项集合与顺序(持久化偏好,重启生效)。
// 无参 = 查看当前生效项与可用项清单(可发现性:项名不自猜)。
func (a *App) cmdStatusline(args []string) (string, error) {
	toks := parseStatuslineArgs(args)
	if len(toks) == 0 {
		cur := a.model.state.Statusline
		src := ""
		if len(cur) == 0 {
			cur, src = defaultStatusline, "(基线默认)"
		}
		var b strings.Builder
		b.WriteString("状态栏: " + strings.Join(cur, " ") + src + "\n")
		b.WriteString("可用项(按配置顺序渲染;回合态项之间用 · 、其余用 | 分隔):\n")
		for _, t := range statuslineTokens {
			b.WriteString(fmt.Sprintf("  %-10s %s\n", t, statuslineTokenDesc[t]))
		}
		b.WriteString("用法: /statusline <项...>(空格或逗号分隔,如 `state workspace session`);reset 恢复基线默认")
		return b.String(), nil
	}
	if toks[0] == "reset" {
		if len(toks) > 1 { // 不静默忽略多余参数(用户以为配了项,实际被 reset 吃掉)
			return "", errString(fmt.Sprintf("reset 不接受附加项(收到 %s)", strings.Join(toks[1:], " ")))
		}
		prefs.SetStatusline(nil)
		a.model.state.Statusline = nil
		return "状态栏已恢复基线默认: " + strings.Join(defaultStatusline, " "), nil
	}
	seen := map[string]bool{}
	for _, t := range toks {
		if statuslineTokenDesc[t] == "" {
			return "", errString(fmt.Sprintf("未知项 %q;可用项: %s", t, strings.Join(statuslineTokens, " ")))
		}
		if seen[t] {
			return "", errString(fmt.Sprintf("项 %q 重复(每项只能出现一次)", t))
		}
		seen[t] = true
	}
	prefs.SetStatusline(toks)
	a.model.state.Statusline = toks
	return "状态栏已更新: " + strings.Join(toks, " ") + "(重启后仍生效;/statusline reset 恢复默认)", nil
}

// cmdTraj /traj:TUI 侧轨迹/可观测视图(S-P0-1 TUI 端)。数据全部来自本进程已收到的
// 会话事件(与 Web 轨迹视图同源同口径),只呈现过程与成本(回合 → 步 → 工具 + 时长/用量),
// 不复制会话正文——看内容请回会话流视图。复用文本 pager 呈现(与 /jobs output、/diff 同款浮层)。
func (a *App) cmdTraj(args []string) (string, error) {
	if st := a.model.state.Traj.Overview(); st.Turns == 0 {
		return "暂无轨迹:本会话还没有回合事件(跑一个回合后再试)", nil
	}
	// Run 在 UI 循环内被调(与 command 的 meta 行同路),此处只赋模型状态,不经 program.Send。
	a.model.state.Doc = NewTextPager(TextPagerSpec{
		Title:  "轨迹 · 可观测",
		Format: "text",
		Status: "本机事件账本派生:时长只取事件时间戳(进行中不给时长)",
		Lines:  a.model.state.Traj.Render(),
	})
	return "轨迹已打开(浮层内 ↑/↓/PgUp/PgDn 滚动,q/Esc 关闭)", nil
}

// cmdNotice /notice:提示详情浮层(NOND-N1;状态栏 notice 项只放标题,正文在这里)。
// 数据源 = ctx.notices.List(0)(与 Web `/api/notices?since=0` 同一接口、同一缓冲),
// 不另搵开一个 TUI 专属缓存 —— 提示的单一事实源在 host-notices。
func (a *App) cmdNotice(args []string) (string, error) {
	var ns sdk.NoticeService
	if err := a.c.Inject("ctx.notices", &ns); err != nil || ns == nil {
		return "", errString("/notice 需要 host-notices 插件(ctx.notices 未装配),提示通道不可用")
	}
	page := ns.List(0)
	if len(page.Items) == 0 {
		return "暂无提示。提示面向「需要人回来的时刻」:后台任务终态 / 定时计划失败或跳过 / 回合报错", nil
	}
	// 最新在前(与 Web toast 同口径):先看当下最急的,历史往下翻。
	lines := []string{"提示共 " + fmt.Sprint(len(page.Items)) + " 条(本进程内缓冲,不落盘、不进会话记录)"}
	if page.Gap {
		lines = append(lines, "更早的提示已被缓冲丢弃:下方列表不完整（服务端环形缓冲上限）")
	}
	if page.Suppressed > 0 {
		lines = append(lines, "另有 "+fmt.Sprint(page.Suppressed)+" 条重复提示已被去重（同一 Key 60s 窗口内）")
	}
	lines = append(lines, "")
	for i := len(page.Items) - 1; i >= 0; i-- {
		n := page.Items[i]
		head := "[" + string(n.Level) + "] " + n.TS.Format("01-02 15:04:05")
		if n.Source != "" && n.Source != "unknown" {
			head += "  " + n.Source
		}
		lines = append(lines, head, n.Title)
		if n.Body != "" {
			for _, bl := range strings.Split(n.Body, "\n") {
				lines = append(lines, "    "+bl)
			}
		}
		lines = append(lines, "")
	}
	// Run 在 UI 循环内被调,此处只赋模型状态,不经 program.Send(与 /traj 同纪律)。
	a.model.state.Doc = NewTextPager(TextPagerSpec{
		Title:  "提示(NOND-N1)",
		Format: "text",
		Status: "来自 ctx.notices(与 Web toast / 桌面壳通知同一事实源);Esc/q 关闭",
		Lines:  lines,
	})
	return "提示已打开（浮层内 ↑/↓/PgUp/PgDn 滚动，q/Esc 关闭）", nil
}

// systemNotify NOND-N2 提示 → 系统级通知:只在 warn/error 时发(info 只更新状态栏),
// 写控制终端(/dev/tty)。失败静默降级 —— 应用内落点(状态栏)本身还在,不值得报错;
// 到底会不会发、发到哪,用 /notify 查。
func (a *App) systemNotify(n *sdk.Notice) {
	if n == nil || a.notifier == nil || !notifyLevelAllows(n.Level) {
		return
	}
	a.notifier.emit(n.Title, n.Body, n.ID)
}

// cmdNotify /notify:系统级通知落点的探测结果/测试/运行期开关(NOND-N2)。
// 无参 = 回显会怎么发;test = 真发一条(真机矩阵靠它逐环境手动跑);
// auto|osc|bell|off = 本次会话切模式(不持久化:env GAH_TUI_NOTIFY 是唯一持久开关)。
func (a *App) cmdNotify(args []string) (string, error) {
	if a.notifier == nil {
		return "系统级通知: 未装配(仅状态栏)", nil
	}
	if len(args) == 0 {
		return a.notifier.statusText(), nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "test":
		if !a.notifier.emit("gah 通知测试", "看到这条(或听到响铃)说明系统级通知可用", 0) {
			return "未发出:" + a.notifier.statusText(), nil
		}
		return "已发出(落点 " + a.notifierTargetText() + ");未看到/未听到说明该终端不认这个序列 —— " +
			"可换 /notify bell 或 /notify osc 再试", nil
	case "auto", "osc", "bell", "off":
		a.notifier.setMode(ParseNotifyMode(args[0]))
		return "系统级通知模式 -> " + string(a.notifier.mode) + "(本次会话;持久开关 = env " + NotifyEnv + ")", nil
	default:
		return "", errString("/notify [test|auto|osc|bell|off]")
	}
}

// notifierTargetText 当前模式下的实际落点文案(off/bell 下探测值无意义,如实区分)。
func (a *App) notifierTargetText() string {
	switch a.notifier.mode {
	case NotifyOff:
		return "off(零输出)"
	case NotifyBell:
		return "bell"
	case NotifyOSC:
		if a.notifier.target == targetNone || a.notifier.target == targetBell {
			return "OSC 9(强制)"
		}
	}
	return a.notifier.target.String()
}

// parseStatuslineArgs 解析 /statusline 参数(空格/逗号分隔,忽略空项)。
func parseStatuslineArgs(args []string) []string {
	var out []string
	for _, raw := range args {
		for _, t := range strings.FieldsFunc(raw, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' }) {
			out = append(out, t)
		}
	}
	return out
}

// filterStatusline 过滤未知/重复项(加载偏好时 fail-soft:坏项丢弃,不因一个拼错整条失效)。
func filterStatusline(items []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range items {
		if statuslineTokenDesc[t] == "" || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

func (a *App) cmdThinking(args []string) (string, error) {
	if len(args) < 1 {
		return "", errString("/thinking off|low|medium|high")
	}
	a.llm.SetThinking(sdk.ParseThinking(args[0]))
	a.model.state.Thinking = args[0]
	return "思考等级 -> " + args[0], nil
}

// cmdProvider /provider show|set|clear:LLM 提供商运行时配置(TUI 入口)。
// set 写 provider.yaml(0600)并立即生效;重启后 env 显式优先、其次本文件。
func (a *App) cmdProvider(args []string) (string, error) {
	if len(args) < 1 {
		return "", errString("/provider show|add|use|set|unset|clear")
	}
	switch args[0] {
	case "show":
		return a.providerShow()
	case "add":
		return a.providerAdd(args[1:])
	case "use":
		return a.providerUse(args[1:])
	case "set":
		return a.providerSet(args[1:])
	case "unset":
		return a.providerUnset(args[1:])
	case "clear":
		if err := providerfile.Clear(); err != nil {
			return "", errString("清除失败: " + err.Error())
		}
		if err := a.llm.ResetProvider(); err != nil {
			return "", errString("已删除 provider.yaml,但运行时复位失败: " + err.Error())
		}
		a.syncDisplay()
		return "已清除全部 provider 并复位运行期(回退 env/样板)", nil
	default:
		return "", errString("/provider show|add|use|set|unset|clear")
	}
}

// multiSvc 多 provider 服务断言(失败 = 显式错误,不静默回退旧单逻辑)。
func (a *App) multiSvc() (sdk.MultiProviderService, error) {
	ms, ok := a.llm.(sdk.MultiProviderService)
	if !ok {
		return nil, errString("多 provider 不可用: LLM 服务未实现 MultiProviderService")
	}
	return ms, nil
}

// switchProvider 切换活跃 provider(端点+模型即切)。
func (a *App) switchProvider(name string) error {
	ms, err := a.multiSvc()
	if err != nil {
		return err
	}
	return ms.SetActiveProvider(name)
}

// providerShow 列出全部 provider(name/端点/模型/key 打码/活跃★)。
func (a *App) providerShow() (string, error) {
	ms, err := a.multiSvc()
	if err != nil {
		return "", err
	}
	ps := ms.Providers()
	if len(ps) == 0 {
		return "提供商: (可用 /provider add <baseUrl> <apiKey> [model] 配置 openai 兼容端点)", nil
	}
	var b strings.Builder
	for i, p := range ps {
		mark := " "
		if p.Active {
			mark = "★"
		}
		fmt.Fprintf(&b, "  %s %s | %s | 模型: %s | Key: %s\n",
			mark, p.Name, orDefault(p.BaseURL, "未配置端点"), orDefault(p.Model, "未设置"), maskKey(p.APIKey))
		_ = i
	}
	b.WriteString("  (切换: /provider use <name>;新增: /provider add <baseUrl> <apiKey> [model];编辑活跃: /provider set)")
	return "提供商: \n" + b.String(), nil
}

// providerAdd 新增并存(名自动=域短名;首个自动活跃,同名 upsert 更新并激活)。
func (a *App) providerAdd(args []string) (string, error) {
	if len(args) < 2 {
		return "", errString("/provider add <baseUrl> <apiKey> [model]\n示例: /provider add https://api.siliconflow.cn/v1 sk-xxxx deepseek-ai/DeepSeek-V3")
	}
	ms, err := a.multiSvc()
	if err != nil {
		return "", err
	}
	model := ""
	if len(args) > 2 {
		model = args[2]
	}
	if err := ms.AddProvider("", args[0], args[1], model); err != nil {
		return "", errString(err.Error())
	}
	name := providerfile.ShortNameOf(args[0])
	a.syncDisplay()
	if providerfile.Active() == name {
		return "已添加并激活 provider " + name + "(" + args[0] + ")\n模型: " + orDefault(a.llm.Model(), "未设置") + " | Key: " + maskKey(args[1]), nil
	}
	return "已添加 provider " + name + "(当前活跃保持 " + providerfile.Active() + ";切换: /provider use " + name + ")", nil
}

// providerUse 切换活跃(端点+模型即切)。
func (a *App) providerUse(args []string) (string, error) {
	if len(args) < 1 {
		return "", errString("/provider use <name>(/provider show 查看名称)")
	}
	if err := a.switchProvider(args[0]); err != nil {
		return "", errString(err.Error())
	}
	a.syncDisplay()
	return "已切换 → " + args[0] + " | 模型: " + orDefault(a.llm.Model(), "未设置") + " | 端点: " + orDefault(providerBaseOf(args[0]), "?"), nil
}

// providerBaseOf 活跃 provider 端点(展示用;空 = 未知)。
func providerBaseOf(name string) string {
	f, err := providerfile.LoadFile()
	if err != nil {
		return ""
	}
	for _, p := range f.Providers {
		if p.Name == name {
			return p.BaseURL
		}
	}
	return ""
}

// providerSet 编辑当前活跃(base/key 必给,model 可选;无活跃时自动新建首条)。
// 旧单 provider 流程不变:set 即编辑/新建默认活跃。
func (a *App) providerSet(args []string) (string, error) {
	if len(args) < 2 {
		return "", errString("/provider set <baseUrl> <apiKey> [model]\n示例: /provider set https://api.siliconflow.cn/v1 sk-xxxx deepseek-ai/DeepSeek-V3")
	}
	name := providerfile.Active()
	model := ""
	if len(args) > 2 {
		model = args[2]
	}
	if name == "" {
		// 无 provider:按 add 语义新建首条并激活
		name = providerfile.ShortNameOf(args[0])
		if err := a.switchAddAsSet(name, args[0], args[1], model); err != nil {
			return "", errString(err.Error())
		}
	} else {
		// 编辑活跃:持久化字段(base/key 覆盖,model 空保留)→ 重新激活生效
		if err := providerfile.SetFields(name, args[0], args[1], model); err != nil {
			return "", errString("持久化失败: " + err.Error())
		}
		if err := a.switchProvider(name); err != nil {
			return "", errString("已持久化,但运行期切换失败: " + err.Error())
		}
	}
	a.syncDisplay()
	return "已更新活跃 provider " + name + "(" + orDefault(providerBaseOf(name), "?") + ")\n模型: " + orDefault(a.llm.Model(), "未设置") + " | Key: " + maskKey(providerKeyOf(name)), nil
}

// switchAddAsSet 无 provider 时 set = 新建并激活(AddProvider 首条自动激活)。
func (a *App) switchAddAsSet(name, base, key, model string) error {
	ms, err := a.multiSvc()
	if err != nil {
		return err
	}
	if err := ms.AddProvider(name, base, key, model); err != nil {
		return err
	}
	if providerfile.Active() == name {
		return nil
	}
	return a.switchProvider(name)
}

// providerKeyOf provider 的 api_key(展示打码;空 = 未知)。
func providerKeyOf(name string) string {
	f, err := providerfile.LoadFile()
	if err != nil {
		return ""
	}
	for _, p := range f.Providers {
		if p.Name == name {
			return p.APIKey
		}
	}
	return ""
}

// providerUnset 逐项删除活跃字段;字段删空 → 移除该 provider 并重切活跃。
func (a *App) providerUnset(args []string) (string, error) {
	if len(args) < 1 {
		return "", errString("/provider unset base_url|api_key|model")
	}
	oldActive := providerfile.Active()
	if err := providerfile.Unset(args[0]); err != nil {
		return "", errString(err.Error())
	}
	if err := a.llm.UnsetProvider(args[0]); err != nil {
		return "", errString("已删除持久化项,但运行时回退失败: " + err.Error())
	}
	// 活跃被移除(删空)时重切到剩余活跃(若存在)
	newActive := providerfile.Active()
	if newActive != "" && newActive != oldActive {
		if err := a.switchProvider(newActive); err != nil {
			return "", errString("活跃已重置为 " + newActive + ",但运行期切换失败: " + err.Error())
		}
	}
	a.syncDisplay()
	return "已删除 " + args[0] + "(持久化与运行期均已回退)", nil
}

// providerLevel2 /provider 二级:use → 枚举现有 provider 名;unset → 字段枚举。
func (a *App) providerLevel2(picked []string) []sdk.Option {
	if len(picked) < 2 {
		return nil
	}
	switch picked[1] {
	case "use":
		f, err := providerfile.LoadFile()
		if err != nil {
			return nil
		}
		opts := make([]sdk.Option, 0, len(f.Providers))
		for _, p := range f.Providers {
			opts = append(opts, sdk.Option{Value: p.Name, Desc: p.BaseURL})
		}
		return opts
	case "unset":
		return []sdk.Option{{Value: "base_url", Desc: "删除端点,回退 env/样板"}, {Value: "api_key", Desc: "删除凭据,回退 env"}, {Value: "model", Desc: "删除模型,回退默认"}}
	}
	return nil // add/set/clear → 自由参数或直接执行
}

// providerFree2 /provider 二级自由参数:add/set → baseUrl/apiKey/model?(尾可选可跳过)。
func (a *App) providerFree2(picked []string) []string {
	if len(picked) < 2 {
		return nil
	}
	switch picked[1] {
	case "add", "set":
		return []string{"baseUrl", "apiKey", "model?"}
	}
	return nil
}

// modelOptions 动态模型枚举:聚合所有 provider 端点模型,选项携带来源
// (Value=provider|model;选中后 /model Run 解析并自动切所属 provider)。
// 单 provider 拉取失败只缺该家模型(经 /provider show 可见状态);无可用 → nil 回退手动输入。
func (a *App) modelOptions([]string) []sdk.Option {
	ms, err := a.multiSvc()
	if err != nil {
		infos, lerr := a.llm.ListModels()
		if lerr != nil {
			return nil
		}
		src := a.providerShort()
		opts := make([]sdk.Option, 0, len(infos))
		for _, m := range infos {
			opts = append(opts, sdk.Option{Value: m.ID, Desc: modelDesc(m.ID, m.OwnedBy, src)})
		}
		return opts
	}
	all := ms.ListAllModels()
	opts := make([]sdk.Option, 0, 16)
	for _, pl := range all {
		if pl.Err != nil || len(pl.Models) == 0 {
			continue // 不可达/无模型:仅 show 可见,不提供无效选项
		}
		for _, m := range pl.Models {
			opts = append(opts, sdk.Option{Value: pl.Name + "|" + m.ID, Desc: modelDesc(m.ID, m.OwnedBy, pl.Name)})
		}
	}
	return opts
}

// syncDisplay 状态栏模型与来源随 provider/模型配置刷新(启动、/model、provider set/unset/clear)。
// 模型 id = 适配器实际生效值;来源 = 当前 provider 域名短名(未配置则空,不显示)。
func (a *App) syncDisplay() {
	a.model.state.Model = a.llm.Model()
	src := ""
	if base, _, ok := a.llm.ProviderInfo(); ok {
		src = providerShortFromURL(base)
	}
	a.model.state.ModelSrc = src
}

// providerShort 当前 provider 域名短名(api.siliconflow.cn → siliconflow;模型来源备注)。
func (a *App) providerShort() string {
	base, _, ok := a.llm.ProviderInfo()
	if !ok {
		return "?"
	}
	return providerShortFromURL(base)
}

// providerShortFromURL URL → 来源短名(纯函数,可测):api.siliconflow.cn → siliconflow。
func providerShortFromURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Hostname() == "" {
		// 非法/无主机:回退原始串(避免凭空猜来源)
		return strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	}
	h := strings.TrimPrefix(parsed.Hostname(), "api.")
	if i := strings.Index(h, "."); i > 0 {
		h = h[:i]
	}
	if h == "" {
		return "?"
	}
	return h
}

// modelDesc 模型选项来源备注(纯函数):(来源),归属前缀不同时附带 (来源/归属)。
func modelDesc(id, ownedBy, src string) string {
	if ownedBy != "" && ownedBy != strings.Split(id, "/")[0] {
		return "(" + src + "/" + ownedBy + ")"
	}
	return "(" + src + ")"
}

// maskKey 凭据打码(尾 4 位;短 key 全掩)。
func maskKey(k string) string {
	if k == "" {
		return "(未设置)"
	}
	if len(k) <= 6 {
		return "***"
	}
	return "***" + k[len(k)-4:]
}

// registerInternalCommands 注册宿主级内部命令(与插件命令共表,
// host-commands 未装配时跳过)。Run 参数为去掉命令名后的剩余参数。
func (a *App) registerInternalCommands() {
	if a.cmds == nil {
		return
	}
	internal := []sdk.CommandSpec{
		{Name: "thinking", Usage: "/thinking off|low|medium|high", Desc: "思考等级(快捷键 Shift+Tab 循环)", Run: a.cmdThinking,
			Args: []sdk.ArgLevel{{Options: func([]string) []sdk.Option {
				return []sdk.Option{{Value: "off", Desc: "关闭思考"}, {Value: "low", Desc: "低等级"}, {Value: "medium", Desc: "中等级"}, {Value: "high", Desc: "高等级"}}
			}}}},
		{Name: "model", Usage: "/model <名>", Desc: "切换模型(枚举聚合全部 provider,选中自动切所属 provider)", Args: []sdk.ArgLevel{{Options: a.modelOptions, FreeArgs: func([]string) []string { return []string{"模型名"} }}}, Run: func(args []string) (string, error) {
			if len(args) < 1 {
				return "", errString("/model <名称> 切换模型")
			}
			model := args[0]
			name := ""
			// 枚举选项(多 provider 聚合)携带来源:Value=provider|model;手动输入无分隔符 → 当前活跃
			if i := strings.Index(model, "|"); i > 0 {
				name, model = model[:i], model[i+1:]
			}
			if name != "" {
				if err := a.switchProvider(name); err != nil {
					return "", errString(err.Error())
				}
			}
			a.llm.SetModel(model)
			a.syncDisplay()
			// 联动:持久化活跃 provider 的 model(重启后模型与端点保持一致)
			if err := providerfile.UpdateModel(model); err != nil {
				return "已切换模型 " + model + ",但持久化同步失败: " + err.Error(), nil
			}
			return "", nil
		}},
		{Name: "provider", Usage: "/provider show|add|use|set|unset|clear", Desc: "配置 LLM 提供商(多 provider 并存/show|add|use|set|unset|clear)", Run: a.cmdProvider,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{
						{Value: "show", Desc: "列出全部提供商(活跃标★,凭据打码)"},
						{Value: "add", Desc: "新增并存(baseUrl apiKey [model];名自动=域短名,首个自动活跃)"},
						{Value: "use", Desc: "切换活跃(枚举现有)"},
						{Value: "set", Desc: "编辑当前活跃的端点/凭据/模型(立即生效+持久化)"},
						{Value: "unset", Desc: "逐项删除活跃字段(恢复 env/样板)"},
						{Value: "clear", Desc: "全部清除+运行时复位"},
					}
				}},
				// 二级:use → 枚举现有 provider 名;add/set → 自由参数(baseUrl/apiKey/model?);unset → 枚举字段
				{Options: a.providerLevel2, FreeArgs: a.providerFree2},
			}},
		{Name: "sandbox", Usage: "/sandbox ro|ws|full|sync [on|off]", Desc: "运行期切沙箱档/切换审批档联动", Run: a.cmdSandbox,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{{Value: "ro", Desc: "只读"}, {Value: "ws", Desc: "工作区写入"}, {Value: "full", Desc: "完全访问"},
						{Value: "sync", Desc: "审批档联动开关(off = 沙箱档位独立生效)"}}
				}},
				{Options: sandboxSyncLevel},
			}},
		{Name: "approval", Usage: "/approval open|smart|strict", Desc: "运行期切审批档", Run: a.cmdApproval,
			Args: []sdk.ArgLevel{{Options: func([]string) []sdk.Option {
				return []sdk.Option{{Value: "open", Desc: "开放:危险操作直接放行"}, {Value: "smart", Desc: "智能:命中危险模式弹确认"}, {Value: "strict", Desc: "严格:危险操作直接拒绝"}}
			}}}},
		{Name: "plugins", Usage: "/plugins list|on|off|default <id>", Desc: "插件插拔/持久开关", Run: a.cmdPlugins,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{{Value: "list", Desc: "列出插件"}, {Value: "on", Desc: "加载并持久启用"}, {Value: "off", Desc: "卸载并持久关闭"}, {Value: "default", Desc: "恢复配置默认"}}
				}},
				// 二级动态:on/off/default 枚举当前插件;list 无二级 → 直接执行
				{Options: func(picked []string) []sdk.Option {
					if len(picked) < 2 || picked[1] == "list" {
						return nil
					}
					var mgr sdk.PluginManager
					if err := a.c.Inject("ctx.pluginManager", &mgr); err != nil {
						return nil
					}
					var opts []sdk.Option
					for _, info := range mgr.List() {
						opts = append(opts, sdk.Option{Value: info.ID, Desc: info.Type})
					}
					return opts
				}},
			}},
		{Name: "settings", Usage: "/settings history N|off|unlimited", Desc: "历史注入", Run: a.cmdSettings,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option { return []sdk.Option{{Value: "history", Desc: "历史条数"}} }},
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{
						{Value: "off", Desc: "关闭历史注入"},
						{Value: "unlimited", Desc: "不限条数"},
						{Value: "20", Desc: "最近 20 条"},
						{Value: "50", Desc: "最近 50 条"},
						{Value: "100", Desc: "最近 100 条"},
						{Value: "200", Desc: "最近 200 条"},
					}
				}, FreeArgs: func([]string) []string { return []string{"条数(off|unlimited|数字)"} }},
			}},
		{Name: "export", Usage: "/export [path]", Desc: "导出会话为 jsonl 文本", Run: a.cmdExport,
			// 自由级断点:回车直接执行(默认路径 jsonl);输入路径回车则导出到该路径
			Args: []sdk.ArgLevel{{FreeArgs: func([]string) []string { return []string{"路径?"} }}}},
		{Name: "compact", Usage: "/compact [指示词]", Desc: "手动滚动摘要压缩(立即折叠旧历史;指示词仅作记录)", Run: a.cmdCompact,
			Args: []sdk.ArgLevel{{FreeArgs: func([]string) []string { return []string{"指示词?"} }}}},
		{Name: "widgets", Usage: "/widgets on|off", Desc: "输入区上方 widget 区开关(宿主注册的动态信息行)", Run: a.cmdWidgets,
			Args: []sdk.ArgLevel{{Options: func([]string) []sdk.Option {
				return []sdk.Option{{Value: "on", Desc: "显示 widget 行"}, {Value: "off", Desc: "隐藏 widget 行"}}
			}}}},
		{Name: "reload", Usage: "/reload", Desc: "热重载指令文件(AGENTS.md 层级/全局/附加;外部编辑即生效)", Run: a.cmdReload},
		{Name: "statusline", Usage: "/statusline [项...]|reset", Desc: "状态栏项集合与顺序(无参=查看当前与可用项;reset=恢复基线默认)",
			// 自由级断点:选中后输入项名回车执行(可多项空格分隔;同 /search 语义)
			Args: []sdk.ArgLevel{{FreeArgs: func([]string) []string {
				return []string{"项(reset? | 空格分隔的多项)"}
			}}},
			Run: a.cmdStatusline},
		{Name: "traj", Usage: "/traj", Desc: "轨迹/可观测视图(回合 → 步 → 工具 + 时长/用量;本机会话事件派生)", Run: a.cmdTraj},
		{Name: "notice", Usage: "/notice", Desc: "提示详情(NOND-N1):无人值守场景的主动提示(后台任务终态/计划失败/回合报错)", Run: a.cmdNotice},
		{Name: "notify", Usage: "/notify [test|auto|osc|bell|off]", Desc: "系统级通知(NOND-N2):探测落点/发测试/切模式;env GAH_TUI_NOTIFY 持久生效", Run: a.cmdNotify,
			Args: []sdk.ArgLevel{{Options: func([]string) []sdk.Option {
				return []sdk.Option{
					{Value: "test", Desc: "发一条测试通知"},
					{Value: "auto", Desc: "按探测结果发(默认)"},
					{Value: "osc", Desc: "强制 OSC 序列"},
					{Value: "bell", Desc: "只响铃"},
					{Value: "off", Desc: "零输出(状态栏仍显示提示)"},
				}
			}}}},
		{Name: "search", Usage: "/search <词>", Desc: "会话内搜索(命中高亮,n/N/F3 循环跳转,Esc 退出)",
			// 自由级断点:选中后光标停留输入框提示继续输入,输入词回车才执行——
			// 否则选中即提交(无参报错),再输入的文字会误走普通消息发给大模型。
			Args: []sdk.ArgLevel{{FreeArgs: func([]string) []string { return []string{"搜索词"} }}},
			Run: func(args []string) (string, error) {
				if len(args) < 1 {
					return "", errString("/search <词>")
				}
				return a.model.searchRun(strings.Join(args, " ")), nil
			}},
		{Name: "workspace", Usage: "/workspace [目录]", Desc: "切换工作区(项目):最近使用列表选择或输入新目录,切换即开新会话",
			// 一级:最近使用工作区枚举(选历史目录直接执行)+ 哨兵“输入新路径”→ 二级自由断点;
			// 无历史记录时一级回退 FreeArgs(直接输入目录)。
			Args: []sdk.ArgLevel{
				{Options: a.workspaceOptions, FreeArgs: func([]string) []string { return []string{"目录路径"} }},
				{FreeArgs: func(picked []string) []string {
					if len(picked) >= 2 && picked[1] == workspaceNewSentinel {
						return []string{"目录路径"}
					}
					return nil // 选了具体历史目录:直接执行
				}},
			},
			Run: a.cmdWorkspace},
		{Name: "fork", Usage: "/fork [seq]", Desc: "从历史任意点派生分支会话(/tree 查看 seq;缺省=最近提问)", Run: a.cmdFork,
			// 一级枚举可分支提问点(seq + 摘要,最新在前);无提问点回退手输 seq
			Args: []sdk.ArgLevel{{Options: a.forkSeqOptions, FreeArgs: func([]string) []string { return []string{"seq"} }}}},
		{Name: "clone", Usage: "/clone", Desc: "复制当前会话(同一分支另一路演进)", Run: a.cmdClone},
		{Name: "tree", Usage: "/tree", Desc: "会话分支树(会话 + 可 fork 的提问点)", Run: a.cmdTree},
		{Name: "session", Usage: "/session list|switch|new|current|pin|unpin|summary", Desc: "会话管理:列出/切换/新建/查看/置顶/概述", Run: a.cmdSession,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{{Value: "list", Desc: "列出已有会话文件(★ = 置顶)"}, {Value: "switch", Desc: "切换到已有会话(二级选择)"}, {Value: "new", Desc: "新建会话(空历史)"}, {Value: "current", Desc: "查看当前会话"}, {Value: "pin", Desc: "置顶当前/指定会话"}, {Value: "unpin", Desc: "取消置顶"}, {Value: "summary", Desc: "生成/查看会话概述(调用模型)"}}
				}},
				// 二级:仅 switch 分支动态枚举会话列表;list/new/current 无二级直接执行
				{Options: a.sessionSwitchOptions},
			}},
		{Name: "name", Usage: "/name <显示名>", Desc: "给当前会话加显示名(- 清除;状态栏/切换列表名优先)",
			// 自由级断点:选中后输入显示名回车执行(可含空格;同 /search 语义)。
			Args: []sdk.ArgLevel{{FreeArgs: func([]string) []string { return []string{"显示名"} }}},
			Run: func(args []string) (string, error) {
				var cs sdk.CwdSessions
				if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
					return "", errString("ctx.cwdSessions 未装配: " + err.Error())
				}
				name := strings.Join(args, " ")
				if name == "-" {
					name = "" // 清除显示名
				}
				if err := cs.Rename(name); err != nil {
					return "", errString("命名失败: " + err.Error())
				}
				a.model.state.Session = sessionLabel(cs)
				if name == "" {
					return "已清除当前会话显示名", nil
				}
				return "已命名当前会话: " + name, nil
			}},
		{Name: "theme", Usage: "/theme <主题名|default>", Desc: "切换配色主题(config/themes/*.yaml;default=恢复启动活动覆盖链)",
			Run: a.cmdTheme, Args: []sdk.ArgLevel{{Options: themeOptions}}},
		{Name: "answer", Usage: "/answer [编号|内容|skip]", Desc: "作答结构化提问(无参=回到作答态;多问按栈序逐个答)",
			Run: a.cmdAnswer, Args: []sdk.ArgLevel{{Options: a.answerOptions}}},
		{Name: "help", Usage: "/help", Desc: "命令帮助", Run: a.cmdHelp},
		{Name: "exit", Usage: "/exit", Desc: "退出", Run: func([]string) (string, error) {
			a.program.Quit()
			return "", nil
		}},
	}
	for _, spec := range internal {
		if _, ok := a.cmds.Get(spec.Name); ok {
			// 已下沉宿主(host-internal-commands)/外部命令插件先注册:判重跳过,
			// TUI 共用注册表命令(node UI 专属命令外,执行即宿主版本,UI 刷新经事件驱动)
			continue
		}
		if _, err := a.cmds.Register(spec); err != nil {
			// 同名冲突:注册表拒绝(宿主命令与插件命令共存时先到先得,不覆盖)
			a.model.state.Lines = append(a.model.state.Lines,
				Line{Kind: "error", Text: err.Error()})
		}
	}
}

// cmdTheme /theme [主题名|default]:运行期切换配色主题
// (config/themes/<名>.yaml 经 ApplyTheme 覆盖当前调色板,下一帧即时重绘);
// default = 重置回启动活动覆盖链(data.palette+theme.yaml)。
func (a *App) cmdTheme(args []string) (string, error) {
	name := strings.TrimSpace(strings.Join(args, " "))
	if name == "" {
		return "", errString("/theme <主题名|default> 切换配色主题")
	}
	if name == themeResetSentinel {
		ResetTheme()
		if len(a.themeBase) > 0 {
			if err := ApplyTheme(a.themeBase); err != nil {
				return "", errString("恢复活动覆盖失败: " + err.Error())
			}
		}
		return "已恢复默认配色(活动覆盖: data.palette + theme.yaml)", nil
	}
	over, err := loadThemeNamed(name)
	if err != nil {
		return "", errString(err.Error())
	}
	if err := ApplyTheme(over); err != nil {
		return "", errString(err.Error())
	}
	return "已切换主题 " + name, nil
}

// cmdHelp 动态命令帮助:遍历注册表输出 usage(插件命令自动纳入,提示前缀过滤说明)。
func (a *App) cmdHelp([]string) (string, error) {
	var b strings.Builder
	b.WriteString("命令(输入 / 实时提示,前缀过滤):")
	for _, spec := range a.cmds.List() {
		b.WriteString("\n  " + spec.Usage + " — " + spec.Desc)
	}
	// S-P2-4:! 直通不是注册命令(不走命令表),故在 help 里显式列出
	b.WriteString("\n  ! <命令> — 直接执行 shell 命令(走沙箱与审批管线;结果本地回显,不进模型上下文)")
	b.WriteString("\n(快捷键:Tab 思维级 / Ctrl+T 思维块 / Ctrl+O 折叠工具 / Ctrl+P·N 历史 / Ctrl+G 外部编辑器 / F3 会话内搜索 / F6 后台坞 / Esc 取消)")
	return b.String(), nil
}

// suggestHints 命令选项:注册表按输入前缀过滤(空前缀=全部,无匹配=空)。
// 输入 / 时显示所有命令,/s 时仅 s 开头——插件注册命令自动进入选项。
func (a *App) suggestHints(prefix string) []sdk.Option {
	if a.cmds == nil {
		return nil
	}
	return filterHints(a.cmds.List(), prefix)
}

// filterHints 命令选项过滤(纯函数):空前缀=全部,按名前缀过滤,无匹配=空。
func filterHints(specs []sdk.CommandSpec, prefix string) []sdk.Option {
	var out []sdk.Option
	for _, spec := range specs {
		if prefix == "" || strings.HasPrefix(spec.Name, prefix) {
			out = append(out, sdk.Option{Value: spec.Name, Desc: spec.Desc})
		}
	}
	return out
}

// levels 取命令的参数级枚举器(选择器级联推进数据源)。
func (a *App) levels(name string) []sdk.ArgLevel {
	if a.cmds == nil {
		return nil
	}
	spec, ok := a.cmds.Get(name)
	if !ok {
		return nil
	}
	return spec.Args
}
