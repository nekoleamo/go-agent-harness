// B2(2026-09-27 安全审计观察项):审批的危险模式表是**枚举**正则,新增一种写法就多一个洞 ——
// 实测漏网:`echo x >> /etc/hosts`(原 `>\s*/etc/` 不匹配双箭头)、`> ~/.zshrc`、
// `mv x /etc/y`、`>> ~/.ssh/authorized_keys`(持久化后门)。
//
// 修法:加一条**派生**路 —— 看命令的写目标落在哪(复用 shellpaths.go 已有的写目标解析),
// 与枚举互补。本文件钉三件事:①漏网写法被认出来;②常规落点**不**弹窗(零噪音回归);
// ③端到端确实走到了审批(prompts 有记录 + 拒绝即 veto)。
package policyguard

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestProtectedWriteTargetDerivation 纯函数级:派生判据认什么、不认什么。
func TestProtectedWriteTargetDerivation(t *testing.T) {
	withCaseFold(t, false)
	home := t.TempDir()
	t.Setenv("HOME", home)

	type hitCase struct{ cmd, wantLabel string }
	var hits []hitCase
	if runtime.GOOS == "windows" {
		// 系统目录类在 Windows 上是另一批(%SystemRoot%/%ProgramFiles% 现算),且命令文本里的
		// `C:\…` 会被 POSIX 词法扫描吃掉反斜杠(scanWord 转义分支)—— 那是**另一条**已登记缺口,
		// 故这里跳过命令层、直接打判据(目录表与大小写折叠由 TestProtectedWriteTargetWindowsDirs 钉)。
	} else {
		hits = []hitCase{
			{"echo x >> /etc/hosts", "写系统目录 /etc"},                                // 枚举漏(双箭头)
			{"echo x > /etc/hosts", "写系统目录 /etc"},                                 // 枚举原有,派生也覆盖
			{"mv /tmp/payload /etc/y", "写系统目录 /etc"},                              // 枚举漏(覆盖系统文件)
			{"cp /tmp/x /usr/local/bin/gah", "写系统目录 /usr"},                        // 枚举漏
			{"install -m 755 x /Library/LaunchDaemons/x.plist", "写系统目录 /Library"}, // 枚举只认 LaunchAgents 字面
		}
	}
	// 家目录/凭据类两边都成立(不平台相关:家目录由 sdk.UserHome() 给,值就是本机家目录下的路径)
	hits = append(hits,
		hitCase{"printf x >> ~/.zshrc", "写敏感配置 ~/.zshrc"},          // 枚举漏(shell 配置劫持)
		hitCase{"echo key >> ~/.ssh/authorized_keys", "写凭据路径"},     // 枚举漏(持久化后门)
		hitCase{"echo key >> $HOME/.ssh/authorized_keys", "写凭据路径"}, // 枚举漏 + 变量写法(展开后判)
	)
	for _, c := range hits {
		label, hit := derivedApprovalTarget("shell", c.cmd)
		if !hit {
			t.Fatalf("%q 应派生出审批项(枚举表的漏网写法)", c.cmd)
		}
		if !strings.Contains(label, c.wantLabel) {
			t.Fatalf("%q 罪名应含 %q,got %q", c.cmd, c.wantLabel, label)
		}
	}

	misses := []string{
		"echo hi > ./notes.txt",                               // 工作区相对路径:路径裁决那条路管,审批层不噪
		"mv a.txt b.txt",                                      // 同上
		"go build -o /tmp/out ./...",                          // /tmp 刻意不收(常规落点)
		"rm -rf /var/tmp/cache",                               // /var 刻意不收(macOS TMPDIR 在 /var/folders)
		"echo x >> " + filepath.Join(home, "proj", "log.txt"), // 家目录普通文件
		"ln -s /etc/passwd /tmp/link",                         // 写目标是 /tmp/link(/etc/passwd 是读)
		"grep -r credentials .",                               // 裸词不是路径
	}
	for _, cmd := range misses {
		if label, hit := derivedApprovalTarget("shell", cmd); hit {
			t.Fatalf("%q 不应派生审批项(避免确认框噪音),got %q", cmd, label)
		}
	}
}

// TestProtectedWriteTargetWindowsDirs Windows 侧受保护目录(%SystemRoot% / %ProgramFiles% /
// %ProgramData%)与大小写折叠:POSIX 那批字面量在 Windows 上根本不存在 ⇒ 写
// `C:\Windows\System32\drivers\etc\hosts` 既不命中枚举也无派生兜底(2026-09-27 补)。
//
// 必须真机跑:判据里的 filepath.IsAbs 只在 Windows 上认 `C:\…`(开关翻不动 stdlib)。
func TestProtectedWriteTargetWindowsDirs(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("受保护目录表在非 Windows 上只有 POSIX 那批;Windows 分支只能在 Windows 上真跑")
	}
	withCaseFold(t, true)
	checked := 0
	for _, env := range []string{"SystemRoot", "ProgramFiles", "ProgramData"} {
		dir := strings.TrimSpace(os.Getenv(env))
		if dir == "" {
			continue
		}
		for _, p := range []string{filepath.Join(dir, "sub", "x.txt"), strings.ToLower(filepath.Join(dir, "x.txt"))} {
			label, hit := protectedWriteTarget(p)
			if !hit || !strings.Contains(label, "写系统目录") {
				t.Fatalf("%s 下的 %s 应派生系统目录审批项,got (%q,%v)", env, p, label, hit)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("三个环境变量都未设:没测到任何 Windows 受保护目录")
	}
}

// TestProtectedWriteTargetHomeCandidates 家目录取**候选并集**:MSYS 的 `~` 看 HOME,
// os.UserHomeDir 看 %USERPROFILE% —— 两者在 Windows 上不是一个目录(HOME 甚至可为空)。
// 过去两边各取一家 ⇒ 「$HOME 下的密钥目录」与「~/.zshrc」判出两个家,一边拦一边漏(2026-09-27)。
func TestProtectedWriteTargetHomeCandidates(t *testing.T) {
	withCaseFold(t, false)
	home := t.TempDir()
	profile := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", profile)

	for _, d := range []string{home, profile} {
		if label, hit := protectedWriteTarget(filepath.Join(d, ".zshrc")); !hit {
			t.Fatalf("%s 下的敏感文件应命中,得 (%q,%v)", d, label, hit)
		}
	}
	// HOME 缺失(Windows 上 os.UserHomeDir 未必认 HOME)不能漏:回退 %USERPROFILE%
	t.Setenv("HOME", "")
	if got := sdk.UserHome(); got != filepath.Clean(profile) {
		t.Fatalf("HOME 未设时首选家目录应为 USERPROFILE,得 %q", got)
	}
	if label, hit := protectedWriteTarget(filepath.Join(profile, ".zshrc")); !hit {
		t.Fatalf("HOME 未设时应回退 USERPROFILE 判定,得 (%q,%v)", label, hit)
	}
}

// shellStub 名为 shell 的替身工具:只记录是否被执行(测试里绝不真跑命令)。
type shellStub struct{ called bool }

func (s *shellStub) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "shell", Description: "stub",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"command": map[string]any{"type": "string"},
		}}}
}

func (s *shellStub) Execute(context.Context, string) (any, error) {
	s.called = true
	return map[string]any{"ok": true}, nil
}

// buildToolsWithShell 在既有装配上再注册 shell 替身(审批/路径裁决都按工具名与命令文本走,与实现无关)。
func buildToolsWithShell(t *testing.T, confirm sdk.ConfirmService, data map[string]any) (sdk.Ctx, *shellStub) {
	t.Helper()
	c := buildTools(t, confirm, data)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	stub := &shellStub{}
	tools.Register(stub) // 返回 Disposer;测试不注销
	return c, stub
}

// TestDerivedApprovalEndToEnd 端到端:smart 档下漏网写法确实走审批,且拒绝即 veto(工具未执行)。
func TestDerivedApprovalEndToEnd(t *testing.T) {
	withCaseFold(t, false)
	home := t.TempDir()
	t.Setenv("HOME", home)
	const cmd = `{"command":"echo x >> ~/.zshrc"}`

	// 拒绝 → veto,确认提示里带派生罪名(用户知道为什么被问),且工具未执行
	rec := &recordingConfirm{resp: false}
	c, stub := buildToolsWithShell(t, rec, map[string]any{"approval": "smart"})
	res := execTool(t, c, "shell", cmd)
	if res.Error == "" || !strings.Contains(res.Error, "用户拒绝") {
		t.Fatalf("smart 档拒绝后应 veto: %+v", res)
	}
	if len(rec.prompts) != 1 || !strings.Contains(rec.prompts[0], "写敏感配置") {
		t.Fatalf("确认提示应带派生罪名: %q", rec.prompts)
	}
	if stub.called {
		t.Fatal("审批拒绝后工具不应执行")
	}

	// 批准 → 审批放行,后续被**路径裁决**拦(家目录在工作区外),工具仍未执行
	rec2 := &recordingConfirm{resp: true}
	c2, stub2 := buildToolsWithShell(t, rec2, map[string]any{"approval": "smart"})
	res2 := execTool(t, c2, "shell", cmd)
	if len(rec2.prompts) != 1 {
		t.Fatalf("批准路径也应先弹确认: %q", rec2.prompts)
	}
	if res2.Error == "" || strings.Contains(res2.Error, "用户拒绝") {
		t.Fatalf("批准后应转为路径裁决拒绝(工作区外),got %+v", res2)
	}
	if stub2.called {
		t.Fatal("路径裁决拒绝后工具不应执行")
	}

	// 零噪音回归:workspace 内普通写**不**弹确认,且正常执行
	rec3 := &recordingConfirm{resp: false}
	c3, stub3 := buildToolsWithShell(t, rec3, map[string]any{"approval": "smart"})
	res3 := execTool(t, c3, "shell", `{"command":"echo hi > ./notes.txt"}`)
	if len(rec3.prompts) != 0 {
		t.Fatalf("工作区内写不应弹确认: %q", rec3.prompts)
	}
	if res3.Error != "" || !stub3.called {
		t.Fatalf("工作区内写应放行并执行: %+v called=%v", res3, stub3.called)
	}

	// 枚举原有命中不受影响(`rm -rf` 仍由模式表给出罪名)
	rec4 := &recordingConfirm{resp: false}
	c4, stub4 := buildToolsWithShell(t, rec4, map[string]any{"approval": "smart"})
	res4 := execTool(t, c4, "shell", `{"command":"rm -rf ./tmpdir"}`)
	if res4.Error == "" || len(rec4.prompts) != 1 || !strings.Contains(rec4.prompts[0], "删除操作") {
		t.Fatalf("枚举模式应保持原样命中: %+v / %q", res4, rec4.prompts)
	}
	if stub4.called {
		t.Fatal("枚举命中的拒绝路径不应执行")
	}
}
