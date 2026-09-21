// TUI UI 投递护栏:tui 生产代码里**不得**直接调用 program.Send。
//
// 为什么:bubbletea v2 的 msgs 通道无缓冲(`Program.Send` = `select { case p.msgs <- msg }`),
// 必须等到 UI 循环读取;而 TUI 的命令 Run / 事件订阅回调就**在 UI 循环 goroutine 上**执行,
// 同一 goroutine 等自己读 = 永久死锁 → TUI 假死(只能外部 kill)。
//
// 实测(第 41 批条目 71,/preview 真文件):SIGQUIT 栈
//
//	Program.eventLoop → Model.Update → Model.handleKey → Model.enter → Model.submit
//	→ App.command → host-docview.Run → Ctx.Emit → Bus.invoke → ui-tui-app 订阅回调
//	→ App.OpenDoc → Program.Send  ← 阻塞
//
// 修法:一律经 `App.sendToUI`(缓冲队列 + 专职转发 goroutine,UI 循环内外都安全,保序)。
// 本护栏是拦“以后又有人加一处 program.Send”这类只在 pty 真机才暴露的死锁。
package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reProgramSend 直接调用点(生产代码只允许 sendToUI 内部经局部变量转发)。
var reProgramSend = regexp.MustCompile(`(a\.|\b[pP]rogram\.)program\.Send\(|a\.program\.Send\(`)

func TestTUINoDirectProgramSend(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "tui", "*.go"))
	if err != nil {
		t.Fatalf("列 tui/*.go 失败: %v", err)
	}
	sendToUISeen := false
	scanned := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读取 %s 失败(工作目录须为仓库根): %v", f, err)
		}
		src := string(b)
		scanned++
		if strings.Contains(src, "func (a *App) sendToUI(") {
			sendToUISeen = true
		}
		for i, line := range strings.Split(src, "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") || strings.HasPrefix(code, "*") {
				continue // 注释里的提及不算
			}
			if reProgramSend.MatchString(line) {
				t.Errorf("%s:%d 直接 program.Send(%s)—— 必须经 a.sendToUI;UI 循环内调用会死锁假死",
					f, i+1, code)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("未扫描到 tui/*.go(路径不对?)")
	}
	if !sendToUISeen {
		t.Fatal("未找到 App.sendToUI(入口被删/改名,请同步本护栏)")
	}
}
