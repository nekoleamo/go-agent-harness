// 档位回显文案的「同源两份」护栏:宿主内部命令(plugins/host/host-internal-commands/commands.go)
// 与 TUI(tui/app.go)各自持有一份 **字节相同** 的文案 helper。
//
// 为什么允许重复:TUI 不许 import 插件包(见 AGENTS.md 红线/import 环),而两条命令路径
// (/sandbox、/approval)必须给出一致的话术 —— 否则同一档位在两端的解释不同,用户看到的
// 「有效档」会互相矛盾。既然无法共享实现,就用本测试把「两份必须一致」钉死:
// 任一侧改了文案而另一侧没跟上 → 本测试失败(与 config/bundle-*.yaml ≡ internal/embed/seed
// 的「同源两份」护栏同一手法)。
//
// 提取方式:按函数名抓函数体源码,剥掉注释与空白后逐字比对。若 helper 被改名/删除,
// 提取结果为空 → 测试显式失败(不静默变成空转用例)。
package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// syncedHelpers 必须两端字节一致的文案 helper。
var syncedHelpers = []string{
	"sandboxStatusText",
	"sandboxSetText",
	"approvalSource",
	"approvalStatusText",
}

// extractFuncBody 取出顶层 funcName 的函数体源码(含签名行至匹配的右花括号)。
// 简化实现:按行找到 "func funcName(" 起始,数括号深度到归零;源码是 gofmt 过的,
// 字符串里不出现裸花括号(<花括号只出现在“{...}”文案中,已用待测包内实际形态覆盖)。
func extractFuncBody(t *testing.T, src, funcName string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	start := -1
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "func "+funcName+"(") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	depth := 0
	var body []string
	for _, ln := range lines[start:] {
		body = append(body, ln)
		depth += strings.Count(ln, "{") - strings.Count(ln, "}")
		if depth == 0 {
			break
		}
	}
	// 剥注释与空白:只比语义文本,避免注释措辞差异把护栏变成噪音
	var clean []string
	for _, ln := range body {
		if i := strings.Index(ln, "//"); i >= 0 {
			ln = ln[:i]
		}
		ln = strings.Join(strings.Fields(ln), "")
		if ln != "" {
			clean = append(clean, ln)
		}
	}
	return strings.Join(clean, "\n")
}

func TestDisplayTextHelpersIdenticalAcrossTUIAndHost(t *testing.T) {
	root := ".."
	tuiSrc := readRepoFile(t, filepath.Join(root, "tui", "app.go"))
	hostSrc := readRepoFile(t, filepath.Join(root, "plugins", "host", "host-internal-commands", "commands.go"))

	// 文案里出现的中文标点/长句必须真的被提取到(防改名后本测试退化成空断言)
	if len(syncedHelpers) == 0 {
		t.Fatal("护栏清单为空")
	}
	for _, name := range syncedHelpers {
		a := extractFuncBody(t, tuiSrc, name)
		b := extractFuncBody(t, hostSrc, name)
		if a == "" || b == "" {
			t.Fatalf("%s 未能在两端同时找到(tui=%q host=%q):改名/删除时必须同步更新本护栏", name, a, b)
		}
		if a != b {
			t.Fatalf("档位回显文案在 TUI 与宿主命令两侧已漂移(用户会看到互相矛盾的有效档):\n--- tui/app.go\n%s\n--- host-internal-commands/commands.go\n%s", a, b)
		}
	}

	// 语义抽查:文案必须真的包含「声明档 / 有效档 / 联动来源」三要素(而非提取到空壳函数)
	eff := extractFuncBody(t, hostSrc, "sandboxStatusText")
	for _, want := range []string{"沙箱:", "有效:", "approvalSource("} {
		if !strings.Contains(eff, want) {
			t.Fatalf("sandboxStatusText 缺少要素 %q(文案退化?): %s", want, eff)
		}
	}
	if src := extractFuncBody(t, hostSrc, "approvalSource"); !strings.Contains(src, "\"approval=\"") {
		t.Fatalf("approvalSource 应给出 approval=<mode> 来源标注,got: %s", src)
	}
}

// TestCommandTextNoEmDash 用户可见文案禁用 em-dash(AGENTS.md UI 规范/taste 纪律)。
func TestCommandTextNoEmDash(t *testing.T) {
	pats := []string{"—", "–"}
	files := []string{
		filepath.Join("..", "tui", "app.go"),
		filepath.Join("..", "plugins", "host", "host-internal-commands", "commands.go"),
	}
	for _, f := range files {
		src := readRepoFile(t, f)
		// 只看新增的档位文案 helper 区段,避免历史文案干扰
		for _, name := range syncedHelpers {
			body := extractFuncBody(t, src, name)
			for _, p := range pats {
				if regexp.MustCompile(regexp.QuoteMeta(p)).MatchString(body) {
					t.Fatalf("%s 的 %s 文案含 em-dash(%q):%s", f, name, p, body)
				}
			}
		}
	}
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("读取 %s 失败(工作目录须为仓库根的 tests/): %v", rel, err)
	}
	return string(b)
}
