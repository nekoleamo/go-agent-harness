// shell 路径裁决的装配级验证(R10 ①):host-tools + policy-guard + shell 工具替身。
//
// 主证明用例刻意选不命中危险模式的命令(`echo hi > 文件`),使拒绝只可能来自新增的
// 路径裁决层;命中危险模式的用例统一给"批准"确认通道,隔离审批层与路径层两种拒绝来源
// (并据此断言报错文本来自路径层,而非审批层)。
package policyguard

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubShellTool shell 工具替身:只记录是否真正被执行。
type stubShellTool struct{ called bool }

func (s *stubShellTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "shell", Description: "shell 替身", InputSchema: map[string]any{"type": "object"},
	}
}

func (s *stubShellTool) Execute(_ context.Context, args string) (any, error) {
	s.called = true
	return map[string]any{"ok": args}, nil
}

// shellArgs 生成 shell 工具的标准参数 JSON(命令含引号/反斜杠时也安全)。
func shellArgs(cmd string) string {
	b, _ := json.Marshal(map[string]any{"command": cmd})
	return string(b)
}

// buildShellEnv 装配 host-tools + policy-guard(data)+ shell 工具替身。
func buildShellEnv(t *testing.T, confirm sdk.ConfirmService, data map[string]any) (sdk.Ctx, *stubShellTool) {
	t.Helper()
	c := buildTools(t, confirm, data)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	sh := &stubShellTool{}
	tools.Register(sh)
	return c, sh
}

// TestGuardShellWriteOutsideWorkspaceVetoed 主证明:不命中危险模式的越界写被路径层拦下。
func TestGuardShellWriteOutsideWorkspaceVetoed(t *testing.T) {
	withWinSemantics(t, false)
	outside := filepath.Join(t.TempDir(), "out.txt")
	c, sh := buildShellEnv(t, nil, nil) // smart 档且无确认通道:`echo` 不命中危险模式
	// 路径必须经 ShellPath 再拼进命令:Windows 上反斜杠是 shell 转义符
	// (`> C:\Users\a` 会被写成 `C:Usersa`),正斜杠在 Git Bash 下同等可用。
	res := execTool(t, c, "shell", shellArgs("echo hi > "+testutil.ShellPath(outside)))
	if res.Error == "" || !strings.Contains(res.Error, "写目标被拒") {
		t.Fatalf("越界写应被路径层拒绝,got %+v", res)
	}
	if sh.called {
		t.Fatal("被拒绝的命令不得真正执行")
	}

	// 工作区内同类命令放行且确实执行(证明拒绝来自路径归属,而非命令形态)
	c2, sh2 := buildShellEnv(t, nil, nil)
	if res := execTool(t, c2, "shell", shellArgs("echo hi > ./shellguard-ok.txt")); res.Error != "" {
		t.Fatalf("工作区内写应放行,got %+v", res)
	}
	if !sh2.called {
		t.Fatal("放行的命令应真正执行")
	}
}

// TestGuardShellWriteMatrixApproved 审批已批准(危险模式层放行)后,路径层仍独立裁决。
func TestGuardShellWriteMatrixApproved(t *testing.T) {
	posixSemantics(t)
	cases := []struct {
		name    string
		cmd     string
		veto    bool
		wantMsg string
	}{
		{"重定向越界", "echo hi > /tmp/out.txt", true, "写目标被拒"},
		{"rm 越界", "rm -rf /tmp/x", true, "写目标被拒"},
		{"dd of 越界", "dd if=/dev/zero of=/tmp/x bs=1", true, "写目标被拒"},
		{"sed -i 越界", "sed -i s/a/b/ /tmp/f", true, "写目标被拒"},
		{"tee 越界", "echo x 2>&1 | tee /tmp/log", true, "写目标被拒"},
		{"sudo 前缀越界", "sudo rm -rf /tmp/x", true, "写目标被拒"},
		{"嵌套 shell 越界", `bash -c "echo x > /tmp/y"`, true, "写目标被拒"},
		{"变量写目标", "echo x > $HOME/f", true, "无法裁决"},
		{"cd 出工作区后相对写", "cd /tmp && rm -rf x", true, "无法裁决"},
		{"读凭据", "cat ~/.ssh/id_rsa", true, "凭据"},
		{"工作区内写", "echo hi > ./shellguard-mx.txt", false, ""},
		{"工作区内删", "rm -rf ./shellguard-mx-dir", false, ""},
		{"cd 子目录后相对写", "cd shellguard-sub && rm -rf x", false, ""},
		{"读系统文件", "cat /etc/passwd", false, ""},
		{"外部读入工作区", "cp /etc/hosts ./shellguard-mx2.txt", false, ""},
		{"写伪设备", "echo hi > /dev/null", false, ""},
		{"归档解到工作区内", "tar -xzf a.tgz -C ./shellguard-out", false, ""},
	}
	c, sh := buildShellEnv(t, &recordingConfirm{resp: true}, nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sh.called = false
			res := execTool(t, c, "shell", shellArgs(tc.cmd))
			if tc.veto {
				if res.Error == "" || !strings.Contains(res.Error, tc.wantMsg) {
					t.Fatalf("应被路径层拒绝(含 %q),got %+v", tc.wantMsg, res)
				}
				if sh.called {
					t.Fatal("被拒绝的命令不得真正执行")
				}
				return
			}
			if res.Error != "" {
				t.Fatalf("应放行,got %+v", res)
			}
			if !sh.called {
				t.Fatal("放行的命令应真正执行")
			}
		})
	}
}

// TestGuardShellReadChecksAreNotOverbroad 读语义只做凭据判定,不因"工作区之外"误拦常规读。
func TestGuardShellReadChecksAreNotOverbroad(t *testing.T) {
	withWinSemantics(t, false)
	c, sh := buildShellEnv(t, nil, nil)
	for _, cmd := range []string{
		"cat /etc/passwd",
		"ls -la /usr/include",
		"grep -rn credentials .", // 搜索词不是路径:不得误判为凭据路径
		"go test ./...",
	} {
		sh.called = false
		if res := execTool(t, c, "shell", shellArgs(cmd)); res.Error != "" {
			t.Fatalf("常规读/构建命令 %q 不应被拦,got %+v", cmd, res)
		}
		if !sh.called {
			t.Fatalf("命令 %q 未真正执行", cmd)
		}
	}
	res := execTool(t, c, "shell", shellArgs("cat .env"))
	if res.Error == "" || !strings.Contains(res.Error, "凭据") {
		t.Fatalf("读工作区内 .env 应被拒,got %+v", res)
	}
}

// TestGuardShellModeInteraction read-only 拒执行器 / full-access 放行写目标。
func TestGuardShellModeInteraction(t *testing.T) {
	withWinSemantics(t, false)
	// read-only:执行器类工具整体拒绝(既有语义,先于路径裁决)
	c, sh := buildShellEnv(t, nil, map[string]any{"sandbox": "read-only"})
	if res := execTool(t, c, "shell", shellArgs("echo hi > ./x.txt")); res.Error == "" ||
		!strings.Contains(res.Error, "read-only") {
		t.Fatalf("read-only 档应拒绝 shell 执行器,got %+v", res)
	}
	if sh.called {
		t.Fatal("被拒绝的命令不得真正执行")
	}

	// full-access:路径裁决放行(档位语义:开放档不拦写)
	c2, sh2 := buildShellEnv(t, &recordingConfirm{resp: true}, map[string]any{"sandbox": "full-access"})
	for _, cmd := range []string{"echo hi > /tmp/x", "rm -rf /tmp/x", "dd if=/dev/zero of=/tmp/y bs=1"} {
		sh2.called = false
		if res := execTool(t, c2, "shell", shellArgs(cmd)); res.Error != "" {
			t.Fatalf("full-access 档应放行 %q,got %+v", cmd, res)
		}
		if !sh2.called {
			t.Fatalf("命令 %q 未真正执行", cmd)
		}
	}
}
