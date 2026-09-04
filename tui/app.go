// App:装配事件桥,驱动 bubbletea 程序。插件 ui-tui-app 仅做薄壳挂载。
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	state := &State{Profile: profile}
	m := &Model{state: state}
	a := &App{model: m, c: c, loop: loop, llm: llm, confirmCh: make(chan bool, 1)}
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
	a.registerInternalCommands()
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
func (a *App) submit(input string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelFn = cancel
	go func() {
		err := a.loop.Run(ctx, input)
		a.cancelFn = nil
		a.program.Send(agentDoneMsg{err})
	}()
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
		return "已删除 " + args[1] + "(持久化与运行期均已回退)", nil
	case "clear":
		if err := providerfile.Clear(); err != nil {
			return "", errString("清除失败: " + err.Error())
		}
		if err := a.llm.ResetProvider(); err != nil {
			return "", errString("已删除 provider.yaml,但运行时复位失败: " + err.Error())
		}
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
		{Name: "model", Usage: "/model <名>", Desc: "切换模型", Run: func(args []string) (string, error) {
			if len(args) < 1 {
				return "", errString("/model <名称> 切换模型")
			}
			a.llm.SetModel(args[0])
			a.model.state.Model = args[0]
			// 联动:持久化 provider 存在时同步 model(重启后模型与端点保持一致)
			if err := providerfile.UpdateModel(args[0]); err != nil {
				return "已切换模型 " + args[0] + ",但持久化同步失败: " + err.Error(), nil
			}
			return "", nil
		}},
		{Name: "provider", Usage: "/provider show|set|unset|clear", Desc: "配置 LLM 提供商端点/凭据", Run: a.cmdProvider,
			Args: []func([]string) []sdk.Option{
				func([]string) []sdk.Option {
					return []sdk.Option{{Value: "show", Desc: "查看当前提供商(凭据打码)"}, {Value: "set", Desc: "设置端点/凭据/模型(立即生效+持久化)"}, {Value: "unset", Desc: "逐项删除配置(恢复 env/样板)"}, {Value: "clear", Desc: "全部清除+运行时复位"}}
				},
				// 二级:仅 unset 枚举可删字段;show/set/clear 无二级 → 选中即执行
				providerUnsetLevel,
			}},
		{Name: "sandbox", Usage: "/sandbox ro|ws|full", Desc: "运行期切沙箱档", Run: a.cmdSandbox,
			Args: []func([]string) []sdk.Option{func([]string) []sdk.Option {
				return []sdk.Option{{Value: "ro", Desc: "只读"}, {Value: "ws", Desc: "工作区写入"}, {Value: "full", Desc: "完全访问"}}
			}}},
		{Name: "plugins", Usage: "/plugins list|on|off|default <id>", Desc: "插件插拔/持久开关", Run: a.cmdPlugins,
			Args: []func([]string) []sdk.Option{
				func([]string) []sdk.Option {
					return []sdk.Option{{Value: "list", Desc: "列出插件"}, {Value: "on", Desc: "加载并持久启用"}, {Value: "off", Desc: "卸载并持久关闭"}, {Value: "default", Desc: "恢复配置默认"}}
				},
				// 二级动态:on/off/default 枚举当前插件;list 无二级 → 直接执行
				func(picked []string) []sdk.Option {
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
				},
			}},
		{Name: "settings", Usage: "/settings history N|off|unlimited", Desc: "历史注入", Run: a.cmdSettings,
			Args: []func([]string) []sdk.Option{
				func([]string) []sdk.Option { return []sdk.Option{{Value: "history", Desc: "历史条数"}} },
				func([]string) []sdk.Option {
					return []sdk.Option{{Value: "off", Desc: "关闭历史注入"}, {Value: "unlimited", Desc: "不限条数"}}
				},
			}},
		{Name: "export", Usage: "/export [path]", Desc: "导出会话 jsonl", Run: a.cmdExport},
		{Name: "sessions", Usage: "/sessions", Desc: "当前/已有会话", Run: func([]string) (string, error) { return a.cmdSessions() }},
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
func (a *App) levels(name string) []func(picked []string) []sdk.Option {
	if a.cmds == nil {
		return nil
	}
	spec, ok := a.cmds.Get(name)
	if !ok {
		return nil
	}
	return spec.Args
}
