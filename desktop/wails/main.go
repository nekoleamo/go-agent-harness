// gah 桌面壳(Go + Wails v3)—— 替换原 Tauri v2(Rust)壳。
//
// 为什么换:壳逻辑回到 Go(与 gah 同语言、可正常单测、不再需要 Rust 工具链),
// 托盘/菜单/通知/自启/单实例/更新器/打包都由 v3 框架内置。
// 背景与取舍见 docs/DESKTOP_FEASIBILITY.md §11。
//
// 与旧壳一致的四条主链路:
//  1. 壳挑一个空闲回环端口,经 GAH_WEB_ADDR 交给 sidecar `gah --profile web`;
//  2. 轮询 /api/state 就绪 → 主窗口导航到 `http://127.0.0.1:<port>/?shell=desktop`;
//  3. 页面仍由 gah 自己提供(同源直连,SSE/WS/cookie 全不动);
//  4. 退出链:POST /api/shutdown → 等端口释放 → 超时强杀;壳被强杀时 sidecar 靠 stdin EOF 自己退。
//
// 关键差异(旧壳没有的一层):**v3.0 里从外部 URL 加载的页面拿不到 bindings**
// (页面源是 gah 自己,不是 wails:// 或 wails.localhost),所以壳侧能力不再走 IPC,
// 改由壳自建的**本地控制面 HTTP API**(见 control.go:回环 + 一次性 token + 只放行
// gah 那个源)提供。页面侧经 URL fragment 拿到控制面端口与 token。
package main

import (
	"fmt"
	"net/url"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/icons"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

// version 由构建时 -ldflags "-X main.version=..." 注入(发行版跟 tag 走)。
var version = "dev"

// shell 桌面壳的运行态(单实例,全字段在 main 里填好后再起协程)。
type shell struct {
	app      *application.App
	window   *application.WebviewWindow
	ctl      *controlPlane
	addr     string      // gah web 的回环地址
	gahToken string      // gah 的 data.auth_token(空 = 未开 token 模式)
	ready    atomic.Bool // 界面是否已就绪(就绪前退出要铺诊断页)
	mu       sync.Mutex  // 保护 sc(tray 退出与 OnShutdown 会并发取)
	sc       *sidecarProc
	quitting atomic.Bool
	notifier *notifier // 系统通知(未装配时为 nil,调用方需容忍)

	noticeSvcMissing atomic.Bool      // 宿主未装配 ctx.notices:只记一次
	dataRoot         string           // 本次运行实际使用的数据根(备份/诊断用)
	updater          *desktopProvider // 更新源(未装配时为 nil ⇒ 控制面如实报不可用)
}

func main() {
	initLog(version)

	addr, err := pickFreeAddr()
	if err != nil {
		shellLogf("挑空闲端口失败: %v", err)
		fmt.Fprintf(os.Stderr, "gah: 挑空闲端口失败: %v\n", err)
		os.Exit(1)
	}
	sh := &shell{addr: addr, gahToken: os.Getenv("GAH_WEB_TOKEN")}
	shellLogf("gah web 地址: %s(token 模式: %v)", addr, sh.gahToken != "")

	// 通知服务:macOS 只在 .app 内可注册(见 notify.go 的说明),否则整个应用起不来。
	var svcs []application.Service
	if notificationsSupported() {
		nf := &notifier{svc: notifications.New()}
		sh.notifier = nf
		svcs = append(svcs, application.NewService(nf.svc))
	} else {
		shellLogf("通知: 未打包运行(macOS 需要 bundle identifier),本轮禁用系统通知")
	}

	app := application.New(application.Options{
		Name:        "gah",
		Description: "gah 桌面壳",
		Services:    svcs,
		Mac: application.MacOptions{
			// 关掉主窗口不退出:gah 驻留托盘(后台任务/计划仍在跑),与旧壳一致。
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: appID,
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				shellLogf("第二次启动:聚焦已有窗口")
				sh.focus()
			},
		},
		// 退出链的第一跳:托盘退出/关窗退出都走这里 → 优雅停 sidecar。
		OnShutdown: func() { sh.stopSidecar(true) },
	})
	sh.app = app

	cp, err := startControlPlane(sh)
	if err != nil {
		// 控制面起不来 = 壳侧能力(选目录/导出/更新)全废。显式失败,不静默降级。
		shellLogf("控制面启动失败: %v", err)
		fmt.Fprintf(os.Stderr, "gah: 控制面启动失败: %v\n", err)
		os.Exit(1)
	}
	sh.ctl = cp
	shellLogf("控制面: http://127.0.0.1:%s(仅放行 http://%s)", cp.port(), addr)

	sh.window = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:           "main",
		Title:          "gah",
		Width:          1200,
		Height:         800,
		Hidden:         true, // 就绪后再显示:避免先闪一帧空白页(parent 的旧壳同理)
		EnableFileDrop: false,
	})
	sh.buildTray()

	if err := sh.initUpdater(); err != nil {
		// 更新器起不来不该拦住整个应用(界面/会话全不能用比没有更新更糟);
		// 但要留痕,并让控制面如实报「不可用」。
		shellLogf("更新器初始化失败(本次禁用检查更新): %v", err)
		sh.updater = nil
	}

	// 启动链放协程:spawn sidecar → 等就绪 → 导航 → 显示窗口。
	// 就绪前失败不 panic:把原因铺在窗口里,比白屏闪退有用得多。
	go sh.boot()

	if err := app.Run(); err != nil {
		shellLogf("app.Run 返回错误: %v", err)
		fmt.Fprintf(os.Stderr, "gah: %v\n", err)
	}
	sh.stopSidecar(false) // Run 返回后的兜底:确保 sidecar 不留在后台
	shellLogf("=== 壳退出 ===")
}

// boot 启动 sidecar 并在就绪后把窗口导航到 gah 的页面。
func (s *shell) boot() {
	plan, err := resolveRuntime(localDataDir(), version)
	if err != nil {
		shellLogf("%v", err)
		s.showDiag("找不到运行文件", []string{
			err.Error(),
			"开发态:先跑 bash scripts/gen-desktop-wails.sh(或设 GAH_SIDECAR 指向已构建的 gah)。",
		})
		return
	}
	shellLogf("运行落点: bin=%s data_root=%s external=%v staged=%v",
		plan.bin, plan.dataRoot, plan.external, plan.staged)
	s.dataRoot = plan.dataRoot
	for _, n := range plan.notices {
		// TODO(notice): 便携模式说明应走系统通知;通知服务迁移在本批之后。
		shellLogf("提示: %s", n)
	}
	sc, err := startSidecar(plan.bin, s.addr, s.gahToken)
	if err != nil {
		shellLogf("%v", err)
		s.showDiag("gah 服务未能启动", []string{err.Error(), "壳日志: " + shellLogPath()})
		return
	}
	s.mu.Lock()
	s.sc = sc
	s.mu.Unlock()
	shellLogf("sidecar 已启动 pid=%d", sc.cmd.Process.Pid)

	// 进程退出监听:就绪前退出 → 立刻铺诊断页(别让用户干等 12 秒探活超时)。
	go func() {
		err := sc.wait()
		if err != nil {
			shellLogf("sidecar 已退出: %v", err)
		} else {
			shellLogf("sidecar 已退出: 正常")
		}
		if !s.ready.Load() && !s.quitting.Load() {
			s.showDiag("gah 服务进程已退出", []string{
				"后台服务在界面就绪前就退出了;壳日志里 `sidecar:` 开头的行是它自己的输出。",
				"壳日志: " + shellLogPath(),
			})
		}
	}()

	if !waitReady(s.addr, s.gahToken, 12*time.Second) {
		shellLogf("12s 内 /api/state 未就绪")
		s.showDiag("gah 服务启动超时", []string{
			"12 秒内没有等到 /api/state 应答。常见原因:数据根不可写、端口被占、插件装配失败。",
			"壳日志: " + shellLogPath(),
		})
		return
	}
	s.ready.Store(true)
	u := s.shellURL()
	shellLogf("就绪,导航: %s", redactURL(u))
	s.window.SetURL(u)
	s.window.Show()
	s.window.Focus()
	go s.startNoticePoller()
}

// shellURL 主窗口地址:`?shell=desktop` 让 web UI 走桌面壳布局;fragment 里带控制面
// 端口与 token(gah 自己的 token 必须放**最后** —— 引导页的正则是 `token=` 取到末端)。
func (s *shell) shellURL() string {
	frag := fmt.Sprintf("shellport=%s&shelltoken=%s",
		url.QueryEscape(s.ctl.port()), url.QueryEscape(s.ctl.token))
	if s.gahToken != "" {
		frag += "&token=" + url.QueryEscape(s.gahToken)
	}
	return "http://" + s.addr + "/?shell=desktop#" + frag
}

// redactURL 把 fragment 里的 token 打码后再进日志(日志是要让用户发回来的)。
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.Fragment == "" {
		return raw
	}
	u.Fragment = "shellport=<ctl>&shelltoken=***"
	return u.String()
}

// focus 把已有窗口提到前台(托盘点击 / 二次启动)。
func (s *shell) focus() {
	if s.window == nil {
		return
	}
	s.window.Show()
	s.window.Focus()
}

// stopSidecar 走优雅退出链(幂等)。fromShutdown=true 表示由 OnShutdown 触发。
func (s *shell) stopSidecar(fromShutdown bool) {
	s.mu.Lock()
	sc := s.sc
	s.mu.Unlock()
	if sc == nil {
		return
	}
	if fromShutdown {
		s.quitting.Store(true)
	}
	sc.stop(s.addr, s.gahToken)
}

// showDiag 把失败原因铺到窗口里(比白屏有用),并显示窗口。
func (s *shell) showDiag(title string, lines []string) {
	body := ""
	for _, l := range lines {
		body += "<p>" + htmlEscape(l) + "</p>"
	}
	tail := shellLogTail(16)
	html := `<html><body style="font-family:-apple-system,'Segoe UI',sans-serif;padding:36px;max-width:760px;line-height:1.7;color:#1f2328">` +
		`<h2 style="margin:0 0 12px">` + htmlEscape(title) + `</h2>` + body +
		`<p style="color:#6b7280;font-size:13px">日志文件: ` + htmlEscape(shellLogPath()) + `</p>` +
		`<pre style="white-space:pre-wrap;background:#f5f5f5;padding:12px;border-radius:6px;font-size:12px">` + htmlEscape(tail) + `</pre>` +
		`<p style="color:#6b7280;font-size:13px">版本 ` + htmlEscape(version) + ` · ` + htmlEscape(runtime.GOOS+"/"+runtime.GOARCH) + `</p>` +
		`</body></html>`
	if s.window != nil {
		s.window.SetHTML(html)
		s.window.Show()
		s.window.Focus()
	}
}

// htmlEscape 诊断页文本转义(不引第三方,够用即可)。
func htmlEscape(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch r {
		case '&':
			out = append(out, []rune("&amp;")...)
		case '<':
			out = append(out, []rune("&lt;")...)
		case '>':
			out = append(out, []rune("&gt;")...)
		case '"':
			out = append(out, []rune("&quot;")...)
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

// trayIcon 托盘图标:mac 用模板图(随明暗主题反色),win/linux 用彩色图。
func trayIcon() []byte {
	if runtime.GOOS == "darwin" {
		return icons.SystrayMacTemplate
	}
	return icons.SystrayLight
}
