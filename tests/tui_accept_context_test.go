// A-1 条目 18(P4-5 多级上下文)的另一半:上层 AGENTS.md **层级块**的观测点 + 层级叠加语义。
// 两路证据:① spy provider 看模型收到的 system 段(父级 + 子级并存,子级 override 替换子级 AGENTS.md);
// ② `/context` 的「本地上限估算」逐层列出 `项目指令 <目录>/<文件>`(层级块有观测点;报告比视口长,
//
//	中段要上滚才会被渲染器画出来)。
package tests

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// squashSpace 去掉全部空白(渲染按宽度折行+缩进,含路径的长标签会被拆行)。
func squashSpace(s string) string { return strings.Join(strings.Fields(s), "") }

func TestTUIAcceptContextAgentsHierarchy(t *testing.T) {
	base, spy := newSpyProvider(t, "层级验证答复。", 0)
	bin, env, _ := tuiAcceptSetupSlow(t, base)

	// 临时根:**短路径是有意的** —— 长路径会被 TUI 行宽截断,断言就看不到完整路径。
	// macOS/Linux 用 /tmp;Windows 无 /tmp(报 GetFileAttributesEx /tmp 不存在)⇒ 退系统 temp,
	// 代价是路径超行宽,所以下面 ② 的屏幕逐字比对在 Windows 跳过(① 已覆盖层级逻辑本身)。
	tmpRoot := "/tmp"
	if runtime.GOOS == "windows" {
		tmpRoot = ""
	}
	root, err := os.MkdirTemp(tmpRoot, "gqh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	parent := filepath.Join(root, "proj")
	child := filepath.Join(parent, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAcceptFile(t, parent, "AGENTS.md", "父级指令甲(层级观测点)。\n")
	writeAcceptFile(t, child, "AGENTS.md", "子级指令乙(应被同级 override 替换)。\n")
	writeAcceptFile(t, child, "AGENTS.override.md", "子级覆盖丙(层级观测点)。\n")

	s := newTuiSessIn(t, bin, env, child, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}

	// ① 模型侧:层级叠加 + override 替换
	s.send("问一句\r")
	if !s.waitScreen("层级验证答复", 60*time.Second) {
		t.Fatalf("条目 18:回合未完成;尾段 %q", stripANSI(tailS(s.rawText(), 300)))
	}
	bodies := spy.snapshot()
	if len(bodies) == 0 {
		t.Fatal("条目 18:未捕获到模型请求")
	}
	sys := bodies[len(bodies)-1]
	if !strings.Contains(sys, "父级指令甲") {
		t.Errorf("条目 18:上层(父目录)AGENTS.md 未进入 system 段")
	}
	if !strings.Contains(sys, "子级覆盖丙") {
		t.Errorf("条目 18:子级 AGENTS.override.md 未进入 system 段")
	}
	if strings.Contains(sys, "子级指令乙") {
		t.Errorf("条目 18:子级 AGENTS.md 未被同级 override 替换")
	}

	// ② TUI 侧:层级块有观测点
	if runtime.GOOS == "windows" {
		return // 见上:临时目录路径超行宽,`项目指令 <绝对路径>` 会被截断,屏幕比对不成立
	}
	s.send("/context\r")
	time.Sleep(900 * time.Millisecond)
	s.send("\r")
	time.Sleep(2 * time.Second)
	for i := 0; i < 30; i++ { // 上滚:报告比视口长,中段需拉入视口才会被渲染
		s.send("\x1b[<64;10;10M")
		time.Sleep(120 * time.Millisecond)
	}
	if !s.waitRaw("项目指令", 10*time.Second) {
		t.Fatalf("条目 18:/context 未列出层级块;尾段 %q", stripANSI(tailS(s.rawText(), 300)))
	}
	out := squashSpace(stripANSI(s.rawText()))
	// 渲染里的路径是**规范化**后的(macOS `/tmp` → `/private/tmp`),按同一口径比对
	mustEval := func(p string) string {
		c, err := filepath.EvalSymlinks(p)
		if err != nil {
			t.Fatalf("规范化路径失败 %s: %v", p, err)
		}
		return c
	}
	parentFix, childFix := mustEval(parent), mustEval(child)
	for _, want := range []string{
		"项目指令 " + filepath.Join(parentFix, "AGENTS.md"),
		"项目指令 " + filepath.Join(childFix, "AGENTS.override.md"),
	} {
		if !strings.Contains(out, squashSpace(want)) {
			t.Errorf("条目 18:/context 层级块缺少 %q", want)
		}
	}
}
