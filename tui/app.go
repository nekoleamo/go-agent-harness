// App:装配事件桥,驱动 bubbletea 程序。插件 ui-tui-app 仅做薄壳挂载。
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/internal/install"
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
	confirmCh chan bool // Confirm 阻塞等待用户答复
	subs      []sdk.Disposer
	cmds      sdk.CommandRegistry // ctx.commands(可为 nil:未装配时命令不可用)

	cancelFn context.CancelFunc // 当前回合的取消函数(Esc 中断,见 model.onCancel)
}

// NewApp 构造 TUI 应用。命令注册表(ctx.commands,host-commands 提供)注入:
// 内部命令(宿主级)注册进表与插件命令共表——提示列表/分发/help 全部动态。
func NewApp(c sdk.Ctx, loop sdk.AgentLoop, llm sdk.LLMService, profile string) *App {
	// 启动即从服务拉取实际生效配置(模型/沙箱/思考等级),状态栏不显示"未设置"等假默认;
	// 装配顺序保证适配器已 SetModel(host-llm → 适配器先于 ui 启动)。
	state := &State{Profile: profile, Workspace: workspaceName(),
		Model:    llm.Model(),
		Thinking: llm.Thinking().String(),
	}
	// 沙箱档位从服务读实际值(而非展示层写死 workspace-write)
	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err == nil && sb != nil {
		state.Sandbox = string(sb.Mode())
	}
	m := &Model{state: state}
	a := &App{model: m, c: c, loop: loop, llm: llm, confirmCh: make(chan bool, 1)}
	a.syncDisplay() // 状态栏模型 + 来源(provider 域名缩写)拉实际生效值
	var reg sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &reg); err != nil {
		// host-commands 未装配:命令分发/提示不可用(不阻塞 TUI)
		reg = nil
	}
	a.cmds = reg
	m.onSubmit = a.submit
	m.onCommand = a.command
	m.onConfirm = a.confirmResult
	m.onCancel = a.cancelCurrent
	m.hints = a.suggestHints
	m.levels = a.levels
	m.onThinkingCycle = a.cycleThinking
	m.onStats = func() sdk.UsageStats {
		var us sdk.UsageStatsService
		if err := a.c.Inject("ctx.usageStats", &us); err != nil {
			return sdk.UsageStats{} // host-usage-stats 未装配:状态栏显示 上下文 -
		}
		return us.Stats()
	}
	a.registerInternalCommands()
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

// Confirm 实现 sdk.ConfirmService:弹层询问用户 y/n。
// 无 UI 运行(非 TTY 降级)时 UI 插件不装配本服务,策略拒绝(安全默认)。
func (a *App) Confirm(ctx context.Context, prompt string) (bool, error) {
	a.program.Send(confirmMsg{prompt})
	select {
	case ok := <-a.confirmCh:
		return ok, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (a *App) confirmResult(ok bool) {
	a.confirmCh <- ok
}

// Start 启动 TUI(goroutine 跑 Run),挂接事件订阅。
func (a *App) Start() error {
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
	a.subs = []sdk.Disposer{d1, d2}

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
	for _, d := range a.subs {
		d()
	}
	a.program.Quit()
}

// submit 普通输入:异步跑一轮(持有取消句柄,Esc 中断)。
// 提交瞬间同步置运行态(状态栏立即显示旋转 logo + 思考中,不等 agent/status 事件广播),
// 回合结束(agentDoneMsg)再回空闲。
func (a *App) submit(input string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelFn = cancel
	a.model.state.Running = true
	a.model.state.LastTool = ""
	go func() {
		err := a.loop.Run(ctx, input)
		a.cancelFn = nil
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
	if a.cancelFn != nil {
		a.cancelFn()
	}
}

// command 处理 / 命令:查注册表分发(内部命令与插件命令统一;
// host-commands 未装配时命令不可用,显式提示)。Run 输出文本显示为 meta 行。
func (a *App) command(raw string) error {
	if a.cmds == nil {
		return errString("命令不可用: ctx.commands 未装配(host-commands)")
	}
	fields := strings.Fields(strings.TrimPrefix(raw, "/"))
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

func (a *App) cmdSandbox(args []string) (string, error) {
	if len(args) < 1 {
		return "", errString("/sandbox ro|ws|full(read-only|workspace-write|full-access)")
	}
	var sb sdk.Sandbox
	if err := a.c.Inject("ctx.sandbox", &sb); err != nil {
		return "", errString("ctx.sandbox 未装配: " + err.Error())
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
		return "", errString("/sandbox ro|ws|full")
	}
	sb.SetMode(mode)
	a.model.state.Sandbox = string(mode)
	return "", nil
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

// pluginHome 运行时 home(GAH_HOME 覆盖;默认 ~/.gah,与 boot 一致)。
func (a *App) pluginHome() string {
	if h := os.Getenv("GAH_HOME"); h != "" {
		return h
	}
	if uh, err := os.UserHomeDir(); err == nil {
		return filepath.Join(uh, ".gah")
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
	return "/settings history -> " + args[1], nil
}

func (a *App) cmdSessions() (string, error) {
	var cs sdk.CwdSessions
	if err := a.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return "", errString("ctx.cwdSessions 未装配: " + err.Error())
	}
	rows := "当前会话: " + cs.Current() + "\n已有会话:"
	list := cs.List()
	if len(list) == 0 {
		rows += " (无)"
	}
	for _, k := range list {
		rows += "\n  " + k
	}
	return rows, nil
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
	default:
		return "", errString("/session switch|new|current")
	}
}

// sessionSwitchOptions /session switch 的二级动态枚举:当前项目会话列表。
// 主会话用 main 标识(选择器选项 Value 非空);切换会话用其 id。
func (a *App) sessionSwitchOptions(picked []string) []sdk.Option {
	if len(picked) < 2 || picked[1] != "switch" {
		return nil // 非 switch 分支无二级 → 直接执行
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
	return label
}

// afterSessionSwitch 切换会话后的界面同步:状态栏会话标签(名优先)、重置 token 统计、
// 清空 TUI 会话流并重放新会话历史(继续上下文可见)。
func (a *App) afterSessionSwitch(cs sdk.CwdSessions) {
	a.model.state.Session = sessionLabel(cs)
	var us sdk.UsageStatsService
	if err := a.c.Inject("ctx.usageStats", &us); err == nil {
		us.Reset() // 新会话从零累计(窗口保留)
		a.model.state.Stats = us.Stats()
	} else {
		a.model.state.Stats = sdk.UsageStats{}
	}
	a.model.state.Lines = nil
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
		return "", errString("/provider show|set <baseUrl> <apiKey> [model]|clear")
	}
	switch args[0] {
	case "show":
		u, k, ok := a.llm.ProviderInfo()
		line := "提供商: "
		if !ok {
			return line + "(可用 /provider set <baseUrl> <apiKey> [model] 配置 openai 兼容端点)", nil
		}
		line += u + " | 模型: " + orDefault(a.llm.Model(), "未设置") + " | API Key: " + maskKey(k)
		if p, err := providerfile.Load(); err == nil && (p.APIKey != "" || p.BaseURL != "") {
			line += "\n持久化: provider.yaml(" + orDefault(p.BaseURL, "仅 key") + ", 重启回退 env 优先)"
		}
		return line, nil
	case "set":
		if len(args) < 3 {
			return "", errString("/provider set <baseUrl> <apiKey> [model]\n示例: /provider set https://api.siliconflow.cn/v1 sk-xxxx deepseek-ai/DeepSeek-V3")
		}
		if err := a.llm.SetProvider(args[1], args[2]); err != nil {
			return "", errString(err.Error())
		}
		p := providerfile.Provider{BaseURL: args[1], APIKey: args[2]}
		if len(args) > 3 {
			p.Model = args[3]
			a.llm.SetModel(args[3])
		}
		if err := providerfile.Save(p); err != nil {
			return "", errString("已运行时生效,但持久化失败: " + err.Error())
		}
		a.syncDisplay() // 端点/模型变更后刷新状态栏(含来源)
		return "已切换: " + args[1] + " | 模型: " + orDefault(a.llm.Model(), "未设置") + " | Key: " + maskKey(args[2]) + "(已持久化 provider.yaml, 0600)", nil
	case "unset":
		if len(args) < 2 {
			return "", errString("/provider unset base_url|api_key|model")
		}
		if err := providerfile.Unset(args[1]); err != nil {
			return "", errString(err.Error())
		}
		if err := a.llm.UnsetProvider(args[1]); err != nil {
			return "", errString("已删除持久化项,但运行时回退失败: " + err.Error())
		}
		a.syncDisplay()
		return "已删除 " + args[1] + "(持久化与运行期均已回退)", nil
	case "clear":
		if err := providerfile.Clear(); err != nil {
			return "", errString("清除失败: " + err.Error())
		}
		if err := a.llm.ResetProvider(); err != nil {
			return "", errString("已删除 provider.yaml,但运行时复位失败: " + err.Error())
		}
		a.syncDisplay()
		return "已清除设置并复位运行期(回退 env/样板),重启后一致", nil
	default:
		return "", errString("/provider show|set|unset|clear")
	}
}

// providerUnsetLevel /provider unset 的二级枚举(可删字段)。
func providerUnsetLevel(picked []string) []sdk.Option {
	if len(picked) < 2 || picked[1] != "unset" {
		return nil // 非 unset 分支无二级 → 选中即执行
	}
	return []sdk.Option{{Value: "base_url", Desc: "删除端点,回退 env/样板"}, {Value: "api_key", Desc: "删除凭据,回退 env"}, {Value: "model", Desc: "删除模型,回退默认"}}
}

// providerSetFree /provider set 的自由参数提示(仅 set 分支;其余无 → 直接执行)。
func providerSetFree(picked []string) []string {
	if len(picked) < 2 || picked[1] != "set" {
		return nil
	}
	return []string{"baseUrl", "apiKey", "model?"}
}

// modelOptions 动态模型枚举(来源备注;失败/空 → nil 回退手动输入)。
// sdk 层面:Options 先于 FreeArgs 尝试(advanceInto 语义)。
func (a *App) modelOptions([]string) []sdk.Option {
	infos, err := a.llm.ListModels()
	if err != nil {
		return nil
	}
	src := a.providerShort()
	opts := make([]sdk.Option, 0, len(infos))
	for _, m := range infos {
		opts = append(opts, sdk.Option{Value: m.ID, Desc: modelDesc(m.ID, m.OwnedBy, src)})
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
		{Name: "model", Usage: "/model <名>", Desc: "切换模型", Args: []sdk.ArgLevel{{Options: a.modelOptions, FreeArgs: func([]string) []string { return []string{"模型名"} }}}, Run: func(args []string) (string, error) {
			if len(args) < 1 {
				return "", errString("/model <名称> 切换模型")
			}
			a.llm.SetModel(args[0])
			a.syncDisplay()
			// 联动:持久化 provider 存在时同步 model(重启后模型与端点保持一致)
			if err := providerfile.UpdateModel(args[0]); err != nil {
				return "已切换模型 " + args[0] + ",但持久化同步失败: " + err.Error(), nil
			}
			return "", nil
		}},
		{Name: "provider", Usage: "/provider show|set|unset|clear", Desc: "配置 LLM 提供商端点/凭据", Run: a.cmdProvider,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{{Value: "show", Desc: "查看当前提供商(凭据打码)"}, {Value: "set", Desc: "设置端点/凭据/模型(立即生效+持久化)"}, {Value: "unset", Desc: "逐项删除配置(恢复 env/样板)"}, {Value: "clear", Desc: "全部清除+运行时复位"}}
				}},
				// 二级:set → 自由参数(baseUrl/apiKey/model?);unset → 枚举字段;show/clear → 无定义直接执行
				{Options: providerUnsetLevel, FreeArgs: providerSetFree},
			}},
		{Name: "sandbox", Usage: "/sandbox ro|ws|full", Desc: "运行期切沙箱档", Run: a.cmdSandbox,
			Args: []sdk.ArgLevel{{Options: func([]string) []sdk.Option {
				return []sdk.Option{{Value: "ro", Desc: "只读"}, {Value: "ws", Desc: "工作区写入"}, {Value: "full", Desc: "完全访问"}}
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
					return []sdk.Option{{Value: "off", Desc: "关闭历史注入"}, {Value: "unlimited", Desc: "不限条数"}}
				}},
			}},
		{Name: "export", Usage: "/export [path]", Desc: "导出会话 jsonl", Run: a.cmdExport},
		{Name: "compact", Usage: "/compact [指示词]", Desc: "手动滚动摘要压缩(立即折叠旧历史;指示词仅作记录)", Run: a.cmdCompact},
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
		{Name: "session", Usage: "/session list|switch|new|current", Desc: "会话管理:列出/切换/新建/查看", Run: a.cmdSession,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{{Value: "list", Desc: "列出已有会话文件"}, {Value: "switch", Desc: "切换到已有会话(二级选择)"}, {Value: "new", Desc: "新建会话(空历史)"}, {Value: "current", Desc: "查看当前会话"}}
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
		{Name: "help", Usage: "/help", Desc: "命令帮助", Run: a.cmdHelp},
		{Name: "exit", Usage: "/exit", Desc: "退出", Run: func([]string) (string, error) {
			a.program.Quit()
			return "", nil
		}},
	}
	for _, spec := range internal {
		if _, err := a.cmds.Register(spec); err != nil {
			// 同名冲突:注册表拒绝(宿主命令与插件命令共存时先到先得,不覆盖)
			a.model.state.Lines = append(a.model.state.Lines,
				Line{Kind: "error", Text: err.Error()})
		}
	}
}

// cmdHelp 动态命令帮助:遍历注册表输出 usage(插件命令自动纳入,提示前缀过滤说明)。
func (a *App) cmdHelp([]string) (string, error) {
	var b strings.Builder
	b.WriteString("命令(输入 / 实时提示,前缀过滤):")
	for _, spec := range a.cmds.List() {
		b.WriteString("\n  " + spec.Usage + " — " + spec.Desc)
	}
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
