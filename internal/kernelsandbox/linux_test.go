//go:build linux

// linux_test.go:Linux 专属用例(Landlock 自举 helper 的 argv 编码 + 白名单锚点)。
// 单独文件的原因与 darwin_test.go 相同:llExecFlag/joinRWArg 只在本平台定义,
// 而 `GOOS=darwin go vet` 也会编译测试文件。
package kernelsandbox

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestWrapDecisionTableLinux(t *testing.T) {
	clearMarker(t)
	jail := t.TempDir()
	root := t.TempDir()
	// workspace 档缺根 → 降级 read-only:只有 jail 进白名单(判不定的写更危险)
	got := Wrap(Spec{Jail: jail, Mode: "workspace-write", Label: "ut"})
	if len(got) < 7 || got[1] != llExecFlag {
		t.Skipf("本机内核不支持 Landlock(ABI 探测失败),包装不施加:%v", got)
	}
	// argv 布局:[self, 魔数, mode, root, jail, RW(换行分隔), 真实命令…]
	if got[2] != string(sdk.SandboxReadOnly) {
		t.Fatalf("降级档应写 read-only:%v", got)
	}
	rw := got[5]
	if !strings.Contains(rw, ResolvePath(jail)) {
		t.Fatalf("read-only 降级仍须放行 jail:%s", rw)
	}
	if strings.Contains(rw, ResolvePath(root)) {
		t.Fatalf("工作根未知时不得放行任何工作根:%s", rw)
	}
	// workspace 档带根时:两个锚点都在
	got = Wrap(Spec{Jail: jail, Mode: "workspace-write", Root: root, Label: "ut"})
	if len(got) < 7 {
		t.Fatalf("workspace 档应给出包装:%v", got)
	}
	rw = got[5]
	if !strings.Contains(rw, ResolvePath(root)) || !strings.Contains(rw, ResolvePath(jail)) {
		t.Fatalf("workspace 档应放行根 + jail:%s", rw)
	}
}

// TestJoinSplitRWArgRoundTrip 换行分隔的单槽编码必须可往返(含含空格/冒号的路径)。
func TestJoinSplitRWArgRoundTrip(t *testing.T) {
	in := []string{"/a b/c", "/d:e", "/f"}
	if got := splitRWArg(joinRWArg(in)); strings.Join(got, "|") != strings.Join(in, "|") {
		t.Fatalf("往返不符: %v → %v", in, got)
	}
	if got := splitRWArg(""); got != nil {
		t.Fatalf("空编码应解出 nil: %v", got)
	}
}
