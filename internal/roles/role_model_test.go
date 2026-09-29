package roles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestRoleModelRoundTrip 角色携带 model/thinking 的读写往返,外加一条**回归护栏**。
//
// 为什么非钉不可:Save 是「用 spec 重建一份 roleFile **全量覆盖写**」,所以 roleFile 少一个字段
// 就等于"手写在 role.yaml 里的那个键会被下一次保存静默抹掉"。本批之前 model 正处于这种状态
// (方案文档写着"留字段不生效",实际连字段都没有)。取值后再跑一次 RewriteMount(技能改名/移动
// 走的那条"解析→改→整体回写"路径)确认不被吞。
func TestRoleModelRoundTrip(t *testing.T) {
	setup(t)
	s := Store{}
	if err := s.Create(sdk.RoleSpec{ID: "finance", Name: "财务",
		SkillsSet: true, Skills: []string{"report"},
		Model: "claude-sonnet-4-5", Thinking: "off"}, ""); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("finance")
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "claude-sonnet-4-5" || got.Thinking != "off" {
		t.Fatalf("Get 应读回 model/thinking,得到 model=%q thinking=%q", got.Model, got.Thinking)
	}

	// 再走一遍"改名/移动"的写路径(它同样重建并整体回写 role.yaml)
	if _, err := s.RewriteMount("report", "report-2"); err != nil {
		t.Fatal(err)
	}
	again, err := s.Get("finance")
	if err != nil {
		t.Fatal(err)
	}
	if again.Model != "claude-sonnet-4-5" || again.Thinking != "off" {
		t.Fatalf("RewriteMount 后 model/thinking 必须仍在(否则就是被静默抹掉),得到 model=%q thinking=%q",
			again.Model, again.Thinking)
	}
	// 技能挂载确实被改了(证明上面那次写真的发生了,不是空操作)
	if len(again.Skills) != 1 || again.Skills[0] != "report-2" {
		t.Fatalf("RewriteMount 应改掉挂载,得到 %v", again.Skills)
	}

	// 空值不写键(老角色文件逐字节不变:这两个键不该凭空冒出来)
	if err := s.Create(sdk.RoleSpec{ID: "plain", Name: "默认池"}, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(Dir("plain"), FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "model") || strings.Contains(string(raw), "thinking") {
		t.Fatalf("未声明时不应写出这两个键,得到:\n%s", raw)
	}
}
