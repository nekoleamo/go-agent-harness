// Package uiweb 提供 ui-web-app 插件:挂载 Web UI(设计 §14.1 M7;对称 ui-tui-app 先例)。
// 能力全在宿主,浏览器只订阅事件流:插件薄壳读取 data.addr/auth_token/static_dir,
// 启动 web/ 运行时(HTTP + SSE + REST),提供 ctx.confirm Web 实现(policy-guard 审批弹层)。
// profile-web 与 profile-tui 互斥(不并行装配,保 ctx.confirm 唯一)。
package uiweb

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
	"github.com/nekoleamo/go-agent-harness/web"
)

// Plugin 实现 ui-web-app。requires ctx.agentLoop/ctx.llm(依赖注入由 web.Server 完成)。
type Plugin struct{}

func (p *Plugin) Name() string { return "ui-web-app" }

// Start 注入依赖并启动 Web 服务(监听失败显式报错,不静默降级)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	cfg := web.Config{}
	if m != nil && m.Data != nil {
		if v, ok := m.Data["addr"].(string); ok && v != "" {
			cfg.Addr = v
		}
		if v, ok := m.Data["auth_token"].(string); ok {
			cfg.AuthToken = v
		}
		if v, ok := m.Data["static_dir"].(string); ok {
			cfg.StaticDir = v
		}
	}
	// GAH_WEB_STATIC 环境变量覆写(开发态快速指向 vite 产物目录,HMR 无需改配置)
	if v := os.Getenv("GAH_WEB_STATIC"); v != "" {
		cfg.StaticDir = v
	}
	// GAH_WEB_ADDR 环境变量覆写监听地址(桌面壳动态端口/多实例;对齐 STATIC/OPEN 先例)
	if v := os.Getenv("GAH_WEB_ADDR"); v != "" {
		cfg.Addr = v
	}
	// UI 插件目录(M7.2):默认 $GAH_HOME/ui-plugins(与主配置同 home,数据单根)
	cfg.UIPluginsDir = filepath.Join(uiPluginsHome(), "ui-plugins")
	// 附件目录(附件一期):$GAH_HOME/attachments(数据单根,随目录迁移)
	cfg.AttachmentsDir = filepath.Join(uiPluginsHome(), "attachments")
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		return nil, err
	}
	hub := web.NewHub()
	confirm := web.NewConfirm(hub)
	srv := web.New(cfg, hub, confirm, c.Logger())
	// POST /api/shutdown → 宿主 system/shutdown 事件(cmd/gah 订阅退出 → DisposeAll
	// 回收插件/外部进程)。桌面壳/运维跨平台优雅停机通道(Windows 无 SIGTERM)。
	srv.OnShutdown = func() {
		_, _ = c.Emit(context.Background(), "system/shutdown", nil, sdk.Emit)
	}
	if err := srv.Inject(c); err != nil {
		return nil, err
	}
	srv.ApplyPrefs() // 恢复上次退出偏好(思考/沙箱/历史;model 经 providerfile 自动恢复)
	// 启动成功回调:始终打印访问地址(token 模式含引导 fragment),按配置自动打开浏览器
	// (data.open_browser 缺省 true;env GAH_WEB_OPEN=0/false 显式关)。
	// token 模式凭据置于 URL fragment:引导页用它换 cookie,而 fragment 不会发往服务端
	// (不进访问日志/Referer);用户复制日志里的地址即可直接进入 UI。
	srv.OnReady = func(url string) {
		visit := web.FragmentURL(url, cfg.AuthToken)
		if cfg.AuthToken != "" {
			c.Logger().Info("ui-web-app: 访问地址(token 在 URL fragment 中,不会发往服务端)", "url", visit)
		} else {
			c.Logger().Info("ui-web-app: 访问地址", "url", visit)
		}
		if !shouldOpenBrowser(m) {
			return
		}
		if err := OpenBrowser(visit); err != nil {
			c.Logger().Warn("ui-web-app: 自动打开浏览器失败(可手动访问)", "url", visit, "err", err)
		}
	}
	// 事件通道订阅(会话事件/运行状态/错误;对齐 TUI 订阅集)
	unsub, err := hub.Subscribe(c, sessions)
	if err != nil {
		return nil, err
	}
	// 确认服务注册到宿主(policy-guard 经此弹层;未装配时安全默认拒绝)。
	// P3 融合:已装配 host-confirm-fusion(提供 ctx.confirmFusion)→ 注册为呈现者
	// (与其它渠道并存同卡),不再 Provide ctx.confirm;未装配 = 单 web profile 自提供。
	var fusionReg sdk.Disposer = func() {}
	var questionReg sdk.Disposer = func() {}
	var fusion sdk.ConfirmFusion
	if err := c.Inject("ctx.confirmFusion", &fusion); err == nil && fusion != nil {
		fusionReg = fusion.Register("web", confirm)
		// P3 语义交互:同一 web 服务作为提问呈现者注册(与确认同管道,首答生效)
		var qs sdk.QuestionService
		if err := c.Inject("ctx.question", &qs); err == nil && qs != nil {
			questionReg = qs.RegisterQuestioner("web", srv.Question())
		}
	} else {
		if err := c.Provide("ctx.confirm", confirm); err != nil {
			unsub()
			return nil, err
		}
		// 单 web profile(无 fusion):web 自身提供结构化提问
		// G-E5-4:SingleChannel 适配 sdk.QuestionService(Ask),再包装 ObservedQuestion
		// → 单 profile 也广播 question/requested ↔ question/resolved
		if err := c.Provide("ctx.question", sdk.ObservedQuestion(c, "web", srv.Question().SingleChannel())); err != nil {
			unsub()
			return nil, err
		}
	}
	go func() {
		if err := srv.Start(); err != nil {
			c.Logger().Error("ui-web-app: 监听失败", "err", err)
		}
	}()
	return func() {
		fusionReg()
		questionReg()
		unsub()
		srv.Shutdown()
	}, nil
}

// shouldOpenBrowser 决定启动成功后是否自动打开浏览器:
// env GAH_WEB_OPEN 优先(0/false 关,其余开);否则 data.open_browser;缺省 true。
func shouldOpenBrowser(m *sdk.Manifest) bool {
	if v := os.Getenv("GAH_WEB_OPEN"); v != "" {
		return !(v == "0" || strings.EqualFold(v, "false"))
	}
	if m != nil && m.Data != nil {
		if v, ok := m.Data["open_browser"].(bool); ok {
			return v
		}
	}
	return true
}

// OpenBrowser 以系统默认浏览器打开 URL(异步;平台分派)。
// darwin: open;linux: xdg-open;windows: rundll32。失败返回错误,由调用方记 warn 不阻塞。
func OpenBrowser(url string) error {
	return openBrowserCmd(url).Start()
}

// openBrowserCmd 平台命令构造(测式可注入,不真正启动)。
func openBrowserCmd(url string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url)
	case "linux":
		return exec.Command("xdg-open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return exec.Command("true") // 不支持平台:空操作(OpenBrowser 不报错)
	}
}

// uiPluginsHome 运行时数据根(与 cmd/gah 同语义,boot 恒设 GAH_HOME;
// ~/.gah/$HOME 兜底已弃用 2026-09;空仅嵌入/单测,宁回 TempDir 也不落 cwd/根)。
func uiPluginsHome() string {
	if h := os.Getenv("GAH_HOME"); h != "" {
		return h
	}
	return os.TempDir()
}
