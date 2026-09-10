// Package uitui 提供 ui-tui-app 插件:挂载 TUI 界面(设计:UI 本身也是插件)。
// 非 TTY 环境自动降级(跳过 TUI);GAH_NO_TUI 环境变量强制关闭(headless/CI)。
package uitui

import (
	"context"
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
	// 确认服务(审批弹层):单 profile 自 Provide;P3 融合(host-confirm-fusion 装配)
	// 时注册为 tui 呈现者,与 web/im 同进程并存同卡(不再 Provide 防同名冲突)。
	var confirmReg sdk.Disposer = func() {}
	var questionReg sdk.Disposer = func() {}
	var fusion sdk.ConfirmFusion
	if err := c.Inject("ctx.confirmFusion", &fusion); err == nil && fusion != nil {
		confirmReg = fusion.Register("tui", app)
		// P3 语义交互:同一 App 作为提问呈现者注册(与确认同管道,首答生效)
		var qs sdk.QuestionService
		if err := c.Inject("ctx.question", &qs); err == nil && qs != nil {
			questionReg = qs.RegisterQuestioner("tui", app)
		}
	} else {
		if err := c.Provide("ctx.confirm", app); err != nil {
			return nil, err
		}
		// 单 tui profile(无 fusion):App 自身提供结构化提问(问题入会话流,输入框作答)
		if err := c.Provide("ctx.question", tuiQuestionService{app: app}); err != nil {
			return nil, err
		}
	}
	// 文档预览意图(doc/open;D 组 D1):TUI 本地打开 pager 浮层(与 Web/IM 同事件源)
	docOpen := c.Subscribe(sdk.EventDocOpen, func(_ context.Context, ev *sdk.Event) error {
		switch p := ev.Payload.(type) {
		case sdk.DocOpenEvent:
			app.OpenDoc(p.Path, p.Page, p.Sheet)
		case *sdk.DocOpenEvent:
			app.OpenDoc(p.Path, p.Page, p.Sheet)
		}
		return nil
	})
	if err := app.Start(); err != nil {
		docOpen()
		confirmReg()
		return nil, err
	}
	return func() {
		docOpen()
		confirmReg()
		questionReg()
		app.Close()
	}, nil
}

// tuiQuestionService 单 tui profile 的提问服务适配:Ask 直连 App 呈现(cancel 清理)。
type tuiQuestionService struct{ app *tui.App }

func (s tuiQuestionService) Ask(ctx context.Context, q sdk.Question) (sdk.QuestionAnswer, error) {
	ch, cancel, err := s.app.PresentQuestion(ctx, q)
	if err != nil {
		return sdk.QuestionAnswer{}, err
	}
	defer cancel()
	select {
	case a := <-ch:
		return a, nil
	case <-ctx.Done():
		return sdk.QuestionAnswer{}, ctx.Err()
	}
}

func (s tuiQuestionService) RegisterQuestioner(string, sdk.QuestionPresenter) sdk.Disposer {
	return func() {}
}

// stdinIsTTY 检测 stdin 是否为交互终端(管道/重定向时降级文本)。
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
