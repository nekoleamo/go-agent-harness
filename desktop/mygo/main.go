// gah 桌面壳 · mygo 0.10.0 webview 模式(**验证分支**,未接入发布链)。
//
// 要验证的只有四条主链路是否成立(与 Wails 壳同口径):
//  1. 壳挑空闲回环端口,经 GAH_WEB_ADDR 交给 sidecar `gah --profile web`;
//  2. 轮询 /api/state 就绪后,窗口导航到 `http://127.0.0.1:<port>/?shell=desktop`;
//  3. 页面仍由 gah 自己提供(同源直连,SSE/WS/cookie 不动),壳侧能力**不绑 Go**(走自建控制面);
//  4. 退出链:关窗=收进托盘,退出=POST /api/shutdown → 等端口释放 → 超时强杀。
//
// 刻意**没做**(留给"全面替换"阶段,见 docs/DESKTOP_FEASIBILITY.md §13):
// 控制面 /shell/*、窗口几何持久化、更新器、单实例、通知、自启、发布链。
package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/egoist/mygo"
)

// version 构建期 -ldflags "-X main.version=..." 注入。
var version = "dev"

//go:embed assets/tray.png
var trayPNG []byte

//go:embed assets/tray-template.png
var trayTemplatePNG []byte

// shell 验证壳的运行态。
type shell struct {
	addr string
	sc   *sidecarProc
	win  *mygo.Window
	tray *mygo.Tray

	mu       sync.Mutex
	quitting bool
}

func main() {
	initLog(version)
	shellLogf("壳版本=%s mygo=0.10.0", version)

	bin, err := findSidecar()
	if err != nil {
		fail("找不到 sidecar: %v", err)
	}
	addr, err := pickFreeAddr()
	if err != nil {
		fail("挑空闲端口失败: %v", err)
	}
	token := os.Getenv("GAH_WEB_TOKEN")

	sh := &shell{addr: addr}
	sc, err := startSidecar(bin, addr, token)
	if err != nil {
		fail("%v", err)
	}
	sh.sc = sc
	shellLogf("sidecar: %s(pid=%d)→ http://%s", bin, sc.cmd.Process.Pid, addr)

	if !waitReady(addr, token, 30*time.Second) {
		shellLogf("30s 内 sidecar 未就绪,窗口仍会打开(页面会显示连不上)")
	} else {
		shellLogf("sidecar 就绪")
	}

	// 壳被 SIGTERM/SIGINT 结束时也要带走 sidecar(真机 kill 链路)。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		shellLogf("收到退出信号,回收 sidecar")
		sh.quit()
	}()

	sh.run()
}

// run 起 mygo 事件循环:窗口 + 托盘。
func (s *shell) run() {
	mygo.App.WhenReady(func() {
		s.win = mygo.NewWindow(mygo.WindowOptions{
			Title:  "gah",
			Width:  1200,
			Height: 800,
			// 就绪后再显:避免先闪一帧空白页。
			Hidden: true,
			URL:    s.shellURL(),
		})
		s.win.OnReadyToShow(func() {
			shellLogf("页面可绘制,显示窗口")
			s.win.Show()
		})
		s.win.Page().OnPageTitleUpdated(func(e *mygo.TitleEvent) {
			shellLogf("页面标题=%q", e.Title)
		})
		// 首帧兜底:3s 内没等到 ready-to-show 也显窗(诊断页/慢机器)。
		go func() {
			time.Sleep(3 * time.Second)
			if !s.win.IsVisible() {
				shellLogf("3s 未收到 ready-to-show,兜底显窗(URL=%s)", s.win.Page().URL())
				s.win.Show()
			}
			shellLogf("窗口状态: visible=%v url=%q", s.win.IsVisible(), s.win.Page().URL())
			// 验证用:窗内截图(GUI 截图权限拿不到时,这是唯一的「看一眼」通道)。
			if p := os.Getenv("GAH_SHELL_CAPTURE"); p != "" {
				if png, err := s.win.CapturePage(); err != nil {
					shellLogf("窗内截图失败: %v", err)
				} else if err := os.WriteFile(p, png, 0o644); err != nil {
					shellLogf("写截图失败: %v", err)
				} else {
					shellLogf("窗内截图: %s(%d 字节)", p, len(png))
				}
			}
			// 验证用:关窗是否真收进托盘(进程活着 + 窗口不可见)。
			if os.Getenv("GAH_SHELL_CLOSE_TEST") != "" {
				s.win.Close()
				time.Sleep(300 * time.Millisecond)
				shellLogf("关窗测试: visible=%v(期望 false,进程仍在)", s.win.IsVisible())
			}
		}()
		// 关窗 ≠ 退出:收进托盘(后台任务/计划仍在跑)。
		// 注意:mygo 里「阻止关窗」也会**取消 App.Quit()**(prepareQuit 逐个 close),
		// 所以退出链必须先置 quitting,再调 Quit()。
		s.win.OnClose(func(e *mygo.CloseEvent) {
			s.mu.Lock()
			quitting := s.quitting
			s.mu.Unlock()
			if quitting {
				return // 真退出:允许关窗
			}
			e.PreventDefault()
			s.win.Hide()
			shellLogf("窗口收进托盘")
		})

		tray, err := mygo.NewTray(mygo.TrayOptions{
			Icon:           trayTemplatePNG,
			IconIsTemplate: true,
			ToolTip:        "gah",
			Menu: mygo.NewMenu([]*mygo.MenuItem{
				{Label: "显示 gah", Click: func(*mygo.MenuItem, *mygo.Window) { s.show() }},
				{Label: "重新加载页面", Click: func(*mygo.MenuItem, *mygo.Window) { s.win.Page().Reload() }},
				{Type: mygo.MenuItemSeparator},
				{Label: "退出 gah", Click: func(*mygo.MenuItem, *mygo.Window) { s.quit() }},
			}),
		})
		if err != nil {
			// 托盘起不来不该让整个壳失去救生口:窗口与退出菜单仍在。
			shellLogf("托盘创建失败: %v", err)
		} else {
			s.tray = tray
			shellLogf("托盘已创建")
		}
	})

	// Cmd+Q / 系统退出:先回收 sidecar。
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { s.stopSidecar() })

	if err := mygo.App.Run(); err != nil {
		fail("事件循环退出: %v", err)
	}
	s.stopSidecar()
	shellLogf("壳退出")
}

// show 从托盘召回窗口。
func (s *shell) show() {
	if s.win == nil {
		return
	}
	s.win.Show()
	s.win.Restore()
	s.win.Focus()
}

// quit 托盘「退出 gah」:先停 sidecar 再退事件循环。
func (s *shell) quit() {
	s.mu.Lock()
	if s.quitting {
		s.mu.Unlock()
		return
	}
	s.quitting = true
	s.mu.Unlock()
	s.stopSidecar()
	mygo.App.Quit()
}

// url 页面地址(?shell=desktop 让前端启用桌面壳口径)。
func (s *shell) shellURL() string {
	return fmt.Sprintf("http://%s/?shell=desktop", s.addr)
}
