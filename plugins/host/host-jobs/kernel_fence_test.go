// 后台命令的内核围栏回归(2026-10-03):围栏补上后,**根不存在**的沙箱档不能把命令
// 变成「跑不起来」。
//
// 这是一条真实踩到的 Linux 专属故障:Landlock 加规则要求路径存在(`unix.Open(O_PATH)`
// 对缺失路径 ENOENT,加不上就整体 exit 126),而 macOS seatbelt 容忍不存在的 subpath。
// 宿主-jobs 的用例把 root 指向 `/tmp/ws`(一个不存在的目录),在 Linux CI 上命令直接
// 起不来、任务 failed。修法是 kernelsandbox.Normalized:根缺失 → 降级只读(**收紧**,
// 不是放开)。本用例在 macOS 上恒绿、在 Linux 上才是那道门 —— 注释写明,免得有人以为
// 它在本地也验到了什么。
package hostjobs

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/kernelsandbox"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubSandbox 只有一个非空 Root 的沙箱(workspace-write 档)。
type stubSandbox struct{ root string }

func (s *stubSandbox) Mode() sdk.SandboxMode            { return sdk.SandboxWorkspace }
func (s *stubSandbox) SetMode(sdk.SandboxMode)          {}
func (s *stubSandbox) Root() string                     { return s.root }
func (s *stubSandbox) SetRoot(d string)                 { s.root = d }
func (s *stubSandbox) ValidatePath(p string) error      { return nil }
func (s *stubSandbox) ValidatePathAt(_, p string) error { return nil }

// TestSubmitSurvivesMissingWorkspaceRoot 根不存在的 workspace-write 档下,命令仍能跑完
// (降级只读,不是「不施加」也不是「硬失败」)。
func TestSubmitSurvivesMissingWorkspaceRoot(t *testing.T) {
	if kernelsandbox.Marked() {
		t.Skip("已在祖先的内核沙箱内,不适合验证施加路径")
	}
	missing := filepath.Join(t.TempDir(), "no-such-workspace")
	j := New(&stubSandbox{root: missing})
	defer j.Stop()

	id, err := j.Submit("echo fence-ok")
	if err != nil {
		t.Fatalf("提交应成功: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if out, ok := j.Output(id); ok && out.State != sdk.JobRunning {
			if out.State == sdk.JobFailed {
				t.Fatalf("根不存在不该让命令跑不起来(围栏应降级只读): %+v", out)
			}
			if !strings.Contains(out.Output, "fence-ok") {
				t.Fatalf("命令输出不符: %q", out.Output)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("命令未在 20 秒内结束")
}
