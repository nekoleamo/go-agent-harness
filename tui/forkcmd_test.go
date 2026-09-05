// P4-10 TUI 命令辅助单测:lastUserSeq(缺省分支点取最近提问 seq)。
// 完整 /fork 命令依赖 Ctx 注入;host-cwd-sessions fork 测试已覆盖核心派生逻辑。
package tui

import (
	"errors"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

type fakeForkable struct {
	pts []sdk.ForkPoint
}

func (f *fakeForkable) ForkAt(seq uint64) (string, error) { return "", nil }
func (f *fakeForkable) CloneCurrent() (string, error)     { return "", nil }
func (f *fakeForkable) ForkPoints(id string) ([]sdk.ForkPoint, error) {
	if f.pts == nil {
		return nil, errors.New("n/a")
	}
	return f.pts, nil
}

func TestLastUserSeq(t *testing.T) {
	f := &fakeForkable{pts: []sdk.ForkPoint{{Seq: 2, Text: "a"}, {Seq: 5, Text: "b"}, {Seq: 9, Text: "c"}}}
	if got := lastUserSeq(f, "cur"); got != 9 {
		t.Fatalf("应取最近提问 seq: %d", got)
	}
	// 无提问 → 0;读取失败 → 0
	if got := lastUserSeq(&fakeForkable{pts: nil}, "cur"); got != 0 {
		t.Fatalf("无提问应 0: %d", got)
	}
	empty := &fakeForkable{}
	if got := lastUserSeq(empty, "cur"); got != 0 {
		t.Fatalf("读取失败应 0: %d", got)
	}
}
