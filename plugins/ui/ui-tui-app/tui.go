// Package uitui 提供 ui-tui-app 插件:挂载 TUI 界面(设计:UI 本身也是插件)。
// 非 TTY 环境自动降级(跳过 TUI);GAH_NO_TUI 环境变量强制关闭(headless/CI)。
package uitui

import (
	"os"

	"github.com/nekoleamo/go-agent-harness/sdk"
	"github.com/nekoleamo/go-agent-harness/tui"
)

// Plugin 实现 ui-tui-app。requires ctx.agentLoop/ctx.llm(会话事件经 ctx 广播订阅)。
type Plugin struct{}

func (p *Plugin) Name() string { return "ui-tui-app" }

// Start 注入依赖并启动 TUI。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	if os.Getenv("GAH_NO_TUI") != "" {
		return func() {}, nil
	}
	if !stdinIsTTY() {
		// 非交互环境:降级,不启动 TUI
		return func() {}, nil
	}

	var loop sdk.AgentLoop
	var llm sdk.LLMService
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		return nil, err
	}
	if err := c.Inject("ctx.llm", &llm); err != nil {
		return nil, err
	}
	profile := "tui"
	var palette map[string]string
	if m != nil && m.Data != nil {
		if pr, ok := m.Data["profile"].(string); ok {
			profile = pr
		}
		// M13 主题外部化:data.palette(token→色值,可部分覆盖,缺省回落默认表)
		// 为装配层样板入口;用户全局主题另见 $GAH_HOME/config/theme.yaml(优先覆盖此值)。
		if pm, ok := m.Data["palette"].(map[string]any); ok {
			palette = make(map[string]string, len(pm))
			for k, v := range pm {
				if s, ok := v.(string); ok {
					palette[k] = s
				}
			}
		}
	}
	app := tui.NewApp(c, loop, llm, profile, palette)
	// 确认服务(审批弹层)注册到宿主;未装配时 policy-guard 按安全默认拒绝
	if err := c.Provide("ctx.confirm", app); err != nil {
		return nil, err
	}
	if err := app.Start(); err != nil {
		return nil, err
	}
	return func() { app.Close() }, nil
}

// stdinIsTTY 检测 stdin 是否为交互终端(管道/重定向时降级文本)。
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
