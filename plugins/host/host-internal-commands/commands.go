// Package hostintcmd 提供 host-internal-commands 插件:B3 命令下沉——
// 原 TUI 内部命令(thinking/model/provider/sandbox/plugins/settings/export/compact/workspace/session/reload)
// 注册进宿主 ctx.commands,使任意 profile(web/TUI/headless)经 / 前缀通用。
// 命令逻辑纯服务注入(经 Ctx 逐运行期取服务,不依赖 UI);UI 刷新由标准事件驱动
// (cwd/workspace-switched、cwd/session-switched),TUI 不再重复注册同名(判重跳过)。
package hostintcmd

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-internal-commands。requires ctx.commands(可选:未装配跳过)。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-internal-commands" }

// Start 注册内部命令进 ctx.commands(同名已存在则跳过:防与 TUI 双注册冲突,先到先得)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		return func() {}, nil // 无命令注册表:跳过(命令不可用,不静默报错于装配)
	}
	h := &Host{c: c, m: m}
	disposers := []sdk.Disposer{}
	for _, spec := range h.specs() {
		if _, ok := cmds.Get(spec.Name); ok {
			continue // 防重:已有注册(如 TUI 先装配)不覆盖
		}
		d, err := cmds.Register(spec)
		if err != nil {
			continue // 注册表拒绝(冲突等):跳过,不中断装配
		}
		disposers = append(disposers, d)
	}
	return func() {
		for i := len(disposers) - 1; i >= 0; i-- {
			disposers[i]()
		}
	}, nil
}

// Host 命令执行器:经 Ctx 逐次注入服务(与 TUI 原实现同款,去 UI 状态耦合)。
// m 只用于读 data 配置(如 export_open_browser),为空时全走缺省值。
type Host struct {
	c sdk.Ctx
	m *sdk.Manifest
}

// specs 命令定义(与 TUI registerInternalCommands 同源;仅执行层去 UI 同步)。
func (h *Host) specs() []sdk.CommandSpec {
	return []sdk.CommandSpec{
		{Name: "thinking", Usage: "/thinking off|low|medium|high", Desc: "思考等级", Run: h.cmdThinking,
			Args: []sdk.ArgLevel{{Options: func([]string) []sdk.Option {
				return []sdk.Option{{Value: "off", Desc: "关闭思考"}, {Value: "low", Desc: "低等级"}, {Value: "medium", Desc: "中等级"}, {Value: "high", Desc: "高等级"}}
			}}}},
		{Name: "model", Usage: "/model <名>", Desc: "切换模型(枚举聚合全部 provider,选中自动切所属 provider)", Args: []sdk.ArgLevel{{Options: h.modelOptions, FreeArgs: func([]string) []string { return []string{"模型名"} }}}, Run: func(args []string) (string, error) {
			if len(args) < 1 {
				return "", errString("/model <名称> 切换模型")
			}
			model := args[0]
			name := ""
			if i := strings.Index(model, "|"); i > 0 {
				name, model = model[:i], model[i+1:]
			}
			if name != "" {
				if err := h.switchProvider(name); err != nil {
					return "", errString(err.Error())
				}
			}
			llm, err := h.llm()
			if err != nil {
				return "", errString(err.Error())
			}
			llm.SetModel(model)
			// 联动:持久化活跃 provider 的 model(重启后模型与端点保持一致)
			if err := providerfile.UpdateModel(model); err != nil {
				return "已切换模型 " + model + ",但持久化同步失败: " + err.Error(), nil
			}
			return "", nil
		}},
		{Name: "provider", Usage: "/provider show|add|use|set|unset|remove|clear", Desc: "配置 LLM 提供商(多 provider 并存/show|add|use|set|unset|remove|clear)", Run: h.cmdProvider,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{
						{Value: "show", Desc: "列出全部提供商(活跃标★,凭据打码)"},
						{Value: "add", Desc: "新增并存(baseUrl apiKey [model];名自动=域短名,首个自动活跃)"},
						{Value: "use", Desc: "切换活跃(枚举现有)"},
						{Value: "set", Desc: "编辑当前活跃的端点/凭据/模型(立即生效+持久化)"},
						{Value: "unset", Desc: "逐项删除活跃字段(恢复 env/样板)"},
						{Value: "remove", Desc: "删除单条(枚举现有;删活跃自动顺延,删空回退 env/样板)"},
						{Value: "clear", Desc: "全部清除+运行时复位"},
					}
				}},
				{Options: h.providerLevel2, FreeArgs: h.providerFree2},
			}},
		{Name: "sandbox", Usage: "/sandbox ro|ws|full|sync [on|off]", Desc: "运行期切沙箱档/切换审批档联动", Run: h.cmdSandbox,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{{Value: "ro", Desc: "只读"}, {Value: "ws", Desc: "工作区写入"}, {Value: "full", Desc: "完全访问"},
						{Value: "sync", Desc: "审批档联动开关(off = 沙箱档位独立生效)"}}
				}},
				{Options: sandboxSyncLevel},
			}},
		{Name: "approval", Usage: "/approval open|smart|strict", Desc: "运行期切审批档", Run: h.cmdApproval,
			Args: []sdk.ArgLevel{{Options: func([]string) []sdk.Option {
				return []sdk.Option{{Value: "open", Desc: "开放:危险操作直接放行"}, {Value: "smart", Desc: "智能:命中危险模式弹确认"}, {Value: "strict", Desc: "严格:危险操作直接拒绝"}}
			}}}},
		{Name: "plugins", Usage: "/plugins list|on|off|default <id>", Desc: "插件插拔/持久开关", Run: h.cmdPlugins,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{{Value: "list", Desc: "列出插件"}, {Value: "on", Desc: "加载并持久启用"}, {Value: "off", Desc: "卸载并持久关闭"}, {Value: "default", Desc: "恢复配置默认"}}
				}},
				{Options: func(picked []string) []sdk.Option {
					if len(picked) < 2 || picked[1] == "list" {
						return nil
					}
					var mgr sdk.PluginManager
					if err := h.c.Inject("ctx.pluginManager", &mgr); err != nil {
						return nil
					}
					var opts []sdk.Option
					for _, info := range mgr.List() {
						opts = append(opts, sdk.Option{Value: info.ID, Desc: info.Type})
					}
					return opts
				}},
			}},
		{Name: "settings", Usage: "/settings history N|off|unlimited", Desc: "历史注入", Run: h.cmdSettings,
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
		{Name: "export", Usage: "/export [path]", Desc: "导出会话(.html 结尾→自包含网页;否则 jsonl)", Run: h.cmdExport,
			// 自由级断点:回车直接执行(默认路径 jsonl);输入路径回车则导出到该路径
			Args: []sdk.ArgLevel{{FreeArgs: func([]string) []string { return []string{"路径?"} }}}},
		{Name: "compact", Usage: "/compact [指示词]", Desc: "手动滚动摘要压缩(立即折叠旧历史;指示词仅作记录)", Run: h.cmdCompact,
			Args: []sdk.ArgLevel{{FreeArgs: func([]string) []string { return []string{"指示词?"} }}}},
		{Name: "workspace", Usage: "/workspace [目录]", Desc: "切换工作区(项目):最近使用列表选择或输入新目录,切换即开新会话",
			Args: []sdk.ArgLevel{
				{Options: h.workspaceOptions, FreeArgs: func([]string) []string { return []string{"目录路径"} }},
				{FreeArgs: func(picked []string) []string {
					if len(picked) >= 2 && picked[1] == workspaceNewSentinel {
						return []string{"目录路径"}
					}
					return nil // 选了具体历史目录:直接执行
				}},
			},
			Run: h.cmdWorkspace},
		{Name: "session", Usage: "/session list|switch|new|current", Desc: "会话管理:列出/切换/新建/查看", Run: h.cmdSession,
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{{Value: "list", Desc: "列出已有会话文件"}, {Value: "switch", Desc: "切换到已有会话(二级选择)"}, {Value: "new", Desc: "新建会话(空历史)"}, {Value: "current", Desc: "查看当前会话"}}
				}},
				{Options: h.sessionSwitchOptions},
			}},
		{Name: "reload", Usage: "/reload", Desc: "热重载指令文件(AGENTS.md 层级/全局/附加;外部编辑即生效)", Run: h.cmdReload},
		// S 组三端优化(2026-09-18):只读诊断命令,宿主实现 → TUI/Web/headless 同源可用。
		{Name: "context", Usage: "/context [all]", Desc: "上下文占用分解(真实 token + 本地估算;不发模型请求)", Run: h.cmdContext,
			Args: []sdk.ArgLevel{{Options: func([]string) []sdk.Option {
				return []sdk.Option{{Value: "all", Desc: "展开逐工具 schema 成本"}}
			}}}},
		{Name: "recap", Usage: "/recap", Desc: "本地会话速览(轮数/工具/文件/最近问答;纯本地不调模型)", Run: h.cmdRecap},
		// S-P1-1 变更审查面:数据来自捕获的写操作(tool-files 落 file/change),不依赖 git。
		// 自由参数级断点:回车直接看清单;输入路径回车看该文件逐行 diff。
		{Name: "diff", Usage: "/diff [路径]", Desc: "本会话文件改动清单与逐行 diff(来自捕获的写操作,不依赖 git)", Run: h.cmdDiff,
			Args: []sdk.ArgLevel{{FreeArgs: func([]string) []string { return []string{"路径?"} }}}},
		// 回合取消:通用命令 —— 任意 UI(TUI/Web/headless)
		// 都能用 /stop 取消当前回合,与 TUI Esc、Web 取消按钮共用 ctx.turnControl。
		{Name: "stop", Usage: "/stop", Desc: "取消当前回合(等价 TUI Esc / Web 取消按钮)", Run: h.cmdStop},
	}
}

// —— 执行器 ——

func (h *Host) llm() (sdk.LLMService, error) {
	var llm sdk.LLMService
	if err := h.c.Inject("ctx.llm", &llm); err != nil {
		return nil, fmt.Errorf("ctx.llm 未装配: %w", err)
	}
	return llm, nil
}

func (h *Host) cmdThinking(args []string) (string, error) {
	if len(args) < 1 {
		return "", errString("/thinking off|low|medium|high")
	}
	llm, err := h.llm()
	if err != nil {
		return "", errString(err.Error())
	}
	llm.SetThinking(sdk.ParseThinking(args[0]))
	prefs.SetThinking(args[0]) // 偏好持久化(退出即记,与 Web 共享)
	return "思考等级 -> " + args[0], nil
}

func (h *Host) cmdApproval(args []string) (string, error) {
	var ap sdk.ApprovalService
	if err := h.c.Inject("ctx.approval", &ap); err != nil {
		return "", errString("ctx.approval 未装配: " + err.Error())
	}
	// 无参 = 查看当前档位与它对沙箱有效档的影响(档位联动,见 policy-guard link.go)
	if len(args) < 1 {
		return approvalStatusText(ap.Mode(), h.sandboxOrNil()), nil
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
	prefs.SetApproval(string(mode)) // 退出即记(与 Web 共享偏好)
	return approvalStatusText(mode, h.sandboxOrNil()), nil
}

func (h *Host) cmdSandbox(args []string) (string, error) {
	var sb sdk.Sandbox
	if err := h.c.Inject("ctx.sandbox", &sb); err != nil {
		return "", errString("ctx.sandbox 未装配: " + err.Error())
	}
	// 无参 = 查看当前声明档与有效档(联动覆盖时不再静默)
	if len(args) < 1 {
		return sandboxStatusText(sb, h.approvalMode()), nil
	}
	// /sandbox sync [on|off]:审批档 → 沙箱有效档 的联动开关(R10 ②-2)。
	// 无参只回显;开关关掉后沙箱档位独立生效,不再被 approval 覆盖(可见性由 ②-1 解决,
	// 这里解决可控性:此前只能改 config 重启)。
	if args[0] == "sync" {
		return h.sandboxSync(args[1:])
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
	prefs.SetSandbox(string(mode)) // 退出即记(与 Web 共享偏好)
	return sandboxSetText(sb, h.approvalMode()), nil
}

// sandboxSync 联动开关子命令(无参回显 / on|off 切换 + 退出即记偏好)。
func (h *Host) sandboxSync(args []string) (string, error) {
	sb := h.sandboxOrNil()
	if sb == nil {
		return "", errString("ctx.sandbox 未装配: 无法查看联动开关")
	}
	sc, ok := sb.(sdk.SandboxSync)
	if !ok {
		return "", errString("该沙箱不支持联动开关(仅声明档)")
	}
	if len(args) < 1 {
		return sandboxSyncText(sb, h.approvalMode()), nil
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
	prefs.SetSandboxSync(on) // 退出即记(与 Web 共享偏好)
	return sandboxSyncSetText(sb, on, h.approvalMode()), nil
}

// sandboxOrNil 宽松取沙箱服务(未装配返回 nil:档位回显可降级,命令主功能仍显式报错)。
func (h *Host) sandboxOrNil() sdk.Sandbox {
	var sb sdk.Sandbox
	if err := h.c.Inject("ctx.sandbox", &sb); err != nil {
		return nil
	}
	return sb
}

// approvalMode 当前审批档(未装配返回空串;仅用于回显来源标注,不影响命令语义)。
func (h *Host) approvalMode() sdk.ApprovalMode {
	var ap sdk.ApprovalService
	if err := h.c.Inject("ctx.approval", &ap); err != nil || ap == nil {
		return ""
	}
	return ap.Mode()
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

// sandboxStatusText 沙箱档位回显:声明档 + 联动后的有效档(不一致时标注联动来源)。
// 只有实现 sdk.EffectiveSandbox 的沙箱(真实 policy-guard)能给出有效档;
// 桩/旧实现只报声明档——否则就是把「没覆盖」说成「覆盖了」。
// 有效档取自实现本身(而不是按 approval 推测),换实现也不会说错。
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

// sandboxSetText 切档回显:设置结果落地,被联动覆盖时给出显式提示(不再静默失效)。
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
// 沙箱可给有效档时报真实值(覆盖了才显示被联动);否则给固定语义说明(不猜 sync 开关)。
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

func (h *Host) cmdPlugins(args []string) (string, error) {
	var mgr sdk.PluginManager
	if err := h.c.Inject("ctx.pluginManager", &mgr); err != nil {
		return "", errString("ctx.pluginManager 未装配: " + err.Error())
	}
	if len(args) < 1 {
		return pluginRows(mgr, h.pluginHome()), nil
	}
	switch args[0] {
	case "on", "load":
		if len(args) < 2 {
			return "", errString("/plugins on <id>")
		}
		if err := mgr.Load(args[1]); err != nil {
			return "", errString(err.Error())
		}
		if err := h.persistPlugin(args[1], true); err != nil {
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
		if err := h.persistPlugin(args[1], false); err != nil {
			return "", errString("已卸载,但持久化失败: " + err.Error())
		}
		return "已卸载并持久关闭 " + args[1] + "(重启仍关闭)", nil
	case "default":
		if len(args) < 2 {
			return "", errString("/plugins default <id>")
		}
		if err := install.RemoveEntry(install.RuntimePatch(h.pluginHome()), args[1]); err != nil {
			return "", errString(err.Error())
		}
		return "已清除持久覆盖 " + args[1] + "(恢复配置树默认,重启生效)", nil
	case "list", "":
		return pluginRows(mgr, h.pluginHome()), nil
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
func (h *Host) persistPlugin(id string, enabled bool) error {
	patched := install.RuntimePatch(h.pluginHome())
	if err := install.EnsurePatch(patched, install.Entry{ID: id, Enabled: enabled}); err != nil {
		return err
	}
	return install.EnsureProfileRef(h.pluginHome(), "patch-runtime.yaml")
}

// pluginHome 运行时数据根(GAH_HOME 恒设;空仅嵌入/单测 → TempDir,~/.gah 兜底已弃用)。
func (h *Host) pluginHome() string { return sdk.Home() }

func (h *Host) cmdSettings(args []string) (string, error) {
	var sessions sdk.SessionLog
	if err := h.c.Inject("ctx.sessions", &sessions); err != nil {
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
	prefs.SetHistory(n) // 全局历史偏好(跨新会话记忆)
	return "/settings history -> " + args[1], nil
}

func (h *Host) cmdCompact(args []string) (string, error) {
	var sessions sdk.SessionLog
	if err := h.c.Inject("ctx.sessions", &sessions); err != nil {
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

// compactSummaryLine 压缩结果回显文案(折叠事件数 + 单行截断摘要;纯函数)。
func compactSummaryLine(summary string, folded int) string {
	s := strings.Join(strings.Fields(summary), " ")
	if n := len([]rune(s)); n > 160 {
		s = string([]rune(s)[:159]) + "…"
	}
	return fmt.Sprintf("已折叠 %d 条事件为滚动摘要。当前摘要: %s", folded, s)
}

func (h *Host) cmdReload(_ []string) (string, error) {
	var sp sdk.SystemPromptService
	if err := h.c.Inject("ctx.systemPrompt", &sp); err != nil {
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

// cmdStop 取消当前回合(经 ctx.turnControl;未装配/无运行中回合时给出明确回执)。
func (h *Host) cmdStop(_ []string) (string, error) {
	var tc sdk.TurnControl
	if err := h.c.Inject("ctx.turnControl", &tc); err != nil || tc == nil {
		return "", fmt.Errorf("回合取消服务未装配(ctx.turnControl):无 host-agent-loop 时不可用")
	}
	if !tc.Running() {
		return "当前没有运行中的回合。", nil
	}
	tc.Cancel()
	return "⏹ 已请求停止当前回合。", nil
}

func (h *Host) cmdExport(args []string) (string, error) {
	var sessions sdk.SessionLog
	if err := h.c.Inject("ctx.sessions", &sessions); err != nil {
		return "", errString("ctx.sessions 未装配")
	}
	path := ""
	if len(args) > 0 {
		path = args[0]
	} else {
		var cs sdk.CwdSessions
		if err := h.c.Inject("ctx.cwdSessions", &cs); err == nil {
			path = cs.Path()
		}
	}
	evs := sessions.Replay()
	if path == "" {
		return "会话事件数: " + fmt.Sprint(len(evs)), nil
	}
	// B2:path 以 .html 结尾 → 自包含 HTML 渲染(对齐 pi /export HTML);否则 jsonl(既有语义)
	htmlOut := strings.HasSuffix(strings.ToLower(path), ".html")
	var out []byte
	if htmlOut {
		out = []byte(renderSessionHTML(evs))
	} else {
		var sb strings.Builder
		for _, ev := range evs {
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			sb.Write(b)
			sb.WriteByte('\n')
		}
		out = []byte(sb.String())
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", errString("导出失败: " + err.Error())
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return "", errString("导出失败: " + err.Error())
	}
	msg := fmt.Sprintf("已导出 %d 条事件 → %s", len(evs), path)
	// 导出 HTML 后默认自动打开(缺省开:导出的下一步必然是打开它;env GAH_EXPORT_OPEN=0 /
	// data.export_open_browser=false 关)。打不开只提示不报错 —— 文件已写好,导出本身是成功的。
	if htmlOut && shouldOpenExportBrowser(h.m) {
		if err := openPath(path); err != nil {
			msg += "(自动打开浏览器失败:" + err.Error() + ";可手动打开)"
		} else {
			msg += "(已在浏览器打开)"
		}
	} else if len(args) > 0 {
		// 显式给了非 .html 路径:想要网页的人往往以为“导出=自动打开”,直接告诉他怎么拿到。
		msg += "(非 .html 后缀按 jsonl 导出,不会打开浏览器;要看网页请用 <路径>.html)"
	}
	return msg, nil
}

func (h *Host) cmdSession(args []string) (string, error) {
	var cs sdk.CwdSessions
	if err := h.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return "", errString("ctx.cwdSessions 未装配: " + err.Error())
	}
	if len(args) < 1 {
		return "", errString("/session list|switch|new|current")
	}
	switch args[0] {
	case "list":
		rows := "当前会话: " + cs.Current() + "\n已有会话:"
		list := cs.List()
		if len(list) == 0 {
			rows += " (无)"
		}
		for _, k := range list {
			rows += "\n  " + k
		}
		return rows, nil
	case "current":
		return "当前项目: " + cs.Current() +
			"\n当前会话: " + orDefault(sessionLabel(cs), "主会话") +
			"\n落盘: " + cs.Path(), nil
	case "new":
		id, err := cs.New()
		if err != nil {
			return "", errString(err.Error())
		}
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
		return "已切换到会话 " + orDefault(cs.CurrentSession(), "主会话"), nil
	default:
		return "", errString("/session switch|new|current")
	}
}

// sessionSwitchOptions /session switch 的二级动态枚举(主会话用 main 标识)。
func (h *Host) sessionSwitchOptions(picked []string) []sdk.Option {
	if len(picked) < 2 || picked[1] != "switch" {
		return nil
	}
	var cs sdk.CwdSessions
	if err := h.c.Inject("ctx.cwdSessions", &cs); err != nil {
		return nil
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

// sessionLabel 当前会话展示标签:显示名优先,无名称回退会话 id。
func sessionLabel(cs sdk.CwdSessions) string {
	if n := cs.SessionName(); n != "" {
		return n
	}
	return cs.CurrentSession()
}

// sessionDesc 会话选项描述(主会话标注跨期共享;切换会话带修改时间与事件条数)。
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

// workspaceNewSentinel 选择器哨兵项:选中后进入二级自由断点输入新目录路径。
const workspaceNewSentinel = "__new_dir__"

// workspaceOptions /workspace 一级枚举:最近使用工作区 + 哨兵“输入新目录路径…”。
func (h *Host) workspaceOptions([]string) []sdk.Option {
	var cs sdk.CwdSessions
	if err := h.c.Inject("ctx.cwdSessions", &cs); err != nil {
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

func (h *Host) cmdWorkspace(args []string) (string, error) {
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
	var cs sdk.CwdSessions
	if err := h.c.Inject("ctx.cwdSessions", &cs); err == nil {
		id, err := cs.SwitchProject(sdk.ProjectKeyFromCwd())
		if err != nil {
			return "", errString("/workspace: 会话切换失败: " + err.Error())
		}
		return "已切换工作区 → " + target + "\n会话 key: " + cs.Current() +
			"(新会话 " + id + ",历史经 /session switch 回溯;工具进程 cwd 于重启后生效)", nil
	}
	return "已切换工作区(未装配 cwdSessions,仅 chdir)→ " + target, nil
}

func (h *Host) cmdProvider(args []string) (string, error) {
	if len(args) < 1 {
		return "", errString("/provider show|add|use|set|unset|remove|clear")
	}
	switch args[0] {
	case "show":
		return h.providerShow()
	case "add":
		return h.providerAdd(args[1:])
	case "use":
		return h.providerUse(args[1:])
	case "set":
		return h.providerSet(args[1:])
	case "unset":
		return h.providerUnset(args[1:])
	case "remove":
		return h.providerRemove(args[1:])
	case "clear":
		if err := providerfile.Clear(); err != nil {
			return "", errString("清除失败: " + err.Error())
		}
		llm, err := h.llm()
		if err != nil {
			return "", errString(err.Error())
		}
		if err := llm.ResetProvider(); err != nil {
			return "", errString("已删除 provider.yaml,但运行时复位失败: " + err.Error())
		}
		return "已清除全部 provider 并复位运行期(回退 env/样板)", nil
	default:
		return "", errString("/provider show|add|use|set|unset|remove|clear")
	}
}

// multiSvc 多 provider 服务断言(失败 = 显式错误,不静默回退旧单逻辑)。
func (h *Host) multiSvc() (sdk.MultiProviderService, error) {
	llm, err := h.llm()
	if err != nil {
		return nil, err
	}
	ms, ok := llm.(sdk.MultiProviderService)
	if !ok {
		return nil, errString("多 provider 不可用: LLM 服务未实现 MultiProviderService")
	}
	return ms, nil
}

// switchProvider 切换活跃 provider(端点+模型即切)。
func (h *Host) switchProvider(name string) error {
	ms, err := h.multiSvc()
	if err != nil {
		return err
	}
	return ms.SetActiveProvider(name)
}

func (h *Host) providerShow() (string, error) {
	ms, err := h.multiSvc()
	if err != nil {
		return "", err
	}
	ps := ms.Providers()
	if len(ps) == 0 {
		return "提供商: (可用 /provider add <baseUrl> <apiKey> [model] 配置 openai 兼容端点)", nil
	}
	var b strings.Builder
	for _, p := range ps {
		mark := " "
		if p.Active {
			mark = "★"
		}
		fmt.Fprintf(&b, "  %s %s | %s | 模型: %s | Key: %s\n",
			mark, p.Name, orDefault(p.BaseURL, "未配置端点"), orDefault(p.Model, "未设置"), maskKey(p.APIKey))
	}
	b.WriteString("  (切换: /provider use <name>;新增: /provider add <baseUrl> <apiKey> [model];编辑活跃: /provider set)")
	return "提供商: \n" + b.String(), nil
}

func (h *Host) providerAdd(args []string) (string, error) {
	if len(args) < 2 {
		return "", errString("/provider add <baseUrl> <apiKey> [model]\n示例: /provider add https://api.siliconflow.cn/v1 sk-xxxx deepseek-ai/DeepSeek-V3")
	}
	ms, err := h.multiSvc()
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
	if providerfile.Active() == name {
		return "已添加并激活 provider " + name + "(" + args[0] + ")", nil
	}
	return "已添加 provider " + name + "(当前活跃保持 " + providerfile.Active() + ";切换: /provider use " + name + ")", nil
}

func (h *Host) providerUse(args []string) (string, error) {
	if len(args) < 1 {
		return "", errString("/provider use <name>(/provider show 查看名称)")
	}
	if err := h.switchProvider(args[0]); err != nil {
		return "", errString(err.Error())
	}
	return "已切换 → " + args[0] + " | 端点: " + orDefault(providerBaseOf(args[0]), "?"), nil
}

// providerBaseOf provider 端点(展示用;空 = 未知)。
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

func (h *Host) providerSet(args []string) (string, error) {
	if len(args) < 2 {
		return "", errString("/provider set <baseUrl> <apiKey> [model]\n示例: /provider set https://api.siliconflow.cn/v1 sk-xxxx deepseek-ai/DeepSeek-V3")
	}
	name := providerfile.Active()
	model := ""
	if len(args) > 2 {
		model = args[2]
	}
	if name == "" {
		name = providerfile.ShortNameOf(args[0])
		if err := h.switchAddAsSet(name, args[0], args[1], model); err != nil {
			return "", errString(err.Error())
		}
	} else {
		if err := providerfile.SetFields(name, args[0], args[1], model); err != nil {
			return "", errString("持久化失败: " + err.Error())
		}
		if err := h.switchProvider(name); err != nil {
			return "", errString("已持久化,但运行期切换失败: " + err.Error())
		}
	}
	return "已更新活跃 provider " + name + "(" + orDefault(providerBaseOf(name), "?") + ")", nil
}

// switchAddAsSet 无 provider 时 set = 新建并激活(AddProvider 首条自动激活)。
func (h *Host) switchAddAsSet(name, base, key, model string) error {
	ms, err := h.multiSvc()
	if err != nil {
		return err
	}
	if err := ms.AddProvider(name, base, key, model); err != nil {
		return err
	}
	if providerfile.Active() == name {
		return nil
	}
	return h.switchProvider(name)
}

// providerRemove 删除单条 provider(provider.yaml 与运行期同步;删活跃顺延/删空回退)。
func (h *Host) providerRemove(args []string) (string, error) {
	if len(args) < 1 {
		return "", errString("/provider remove <名>(枚举现有;/provider show 查看)")
	}
	ms, err := h.multiSvc()
	if err != nil {
		return "", errString(err.Error())
	}
	name := args[0]
	oldActive := providerfile.Active()
	if err := ms.RemoveProvider(name); err != nil {
		return "", errString(err.Error())
	}
	switch newActive := providerfile.Active(); {
	case newActive == "":
		return "已删除 " + name + "(已是最后一个,运行期回退 env/样板)", nil
	case newActive != oldActive:
		return "已删除 " + name + ",活跃已顺延为 " + newActive, nil
	default:
		return "已删除 " + name + "(非活跃,运行期未变)", nil
	}
}

func (h *Host) providerUnset(args []string) (string, error) {
	if len(args) < 1 {
		return "", errString("/provider unset base_url|api_key|model")
	}
	oldActive := providerfile.Active()
	if err := providerfile.Unset(args[0]); err != nil {
		return "", errString(err.Error())
	}
	llm, err := h.llm()
	if err != nil {
		return "", errString("已删除持久化项,但运行时服务缺失: " + err.Error())
	}
	if err := llm.UnsetProvider(args[0]); err != nil {
		return "", errString("已删除持久化项,但运行时回退失败: " + err.Error())
	}
	newActive := providerfile.Active()
	if newActive != "" && newActive != oldActive {
		if err := h.switchProvider(newActive); err != nil {
			return "", errString("活跃已重置为 " + newActive + ",但运行期切换失败: " + err.Error())
		}
	}
	return "已删除 " + args[0] + "(持久化与运行期均已回退)", nil
}

// providerLevel2 /provider 二级:use/remove → 枚举现有 provider 名;unset → 字段枚举。
func (h *Host) providerLevel2(picked []string) []sdk.Option {
	if len(picked) < 2 {
		return nil
	}
	switch picked[1] {
	case "use", "remove":
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
	return nil
}

// providerFree2 /provider 二级自由参数:add/set → baseUrl/apiKey/model?。
func (h *Host) providerFree2(picked []string) []string {
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
func (h *Host) modelOptions([]string) []sdk.Option {
	ms, err := h.multiSvc()
	if err != nil {
		llm, lerr := h.llm()
		if lerr != nil {
			return nil
		}
		infos, lerr2 := llm.ListModels()
		if lerr2 != nil {
			return nil
		}
		src := h.providerShort()
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
			continue
		}
		for _, m := range pl.Models {
			opts = append(opts, sdk.Option{Value: pl.Name + "|" + m.ID, Desc: modelDesc(m.ID, m.OwnedBy, pl.Name)})
		}
	}
	return opts
}

// providerShort 当前 provider 域名短名(模型来源备注)。
func (h *Host) providerShort() string {
	llm, err := h.llm()
	if err != nil {
		return "?"
	}
	base, _, ok := llm.ProviderInfo()
	if !ok {
		return "?"
	}
	return providerShortFromURL(base)
}

// providerShortFromURL URL → 来源短名(纯函数):api.siliconflow.cn → siliconflow。
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
	if ownedBy != "" && !strings.HasPrefix(id, ownedBy+"/") {
		return fmt.Sprintf("%s (来源 %s/归属 %s)", id, src, ownedBy)
	}
	return fmt.Sprintf("%s (来源 %s)", id, src)
}

// —— 通用辅助 ——

type errString string

func (e errString) Error() string { return string(e) }

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// maskKey 凭据打码(展示用):保留前 4 后 4,中间掩码;短 key 全掩。
func maskKey(k string) string {
	if k == "" {
		return ""
	}
	r := []rune(k)
	if len(r) <= 8 {
		return "***"
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}
