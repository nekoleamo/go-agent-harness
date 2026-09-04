// 命令提示与渲染联动单测:前缀过滤(纯函数)+ 提示区渲染动态高度。
package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func testSpec(name, desc string) sdk.CommandSpec {
	return sdk.CommandSpec{Name: name, Desc: desc, Usage: "/" + name, Run: func([]string) (string, error) { return "", nil }}
}

// TestFilterHintsPrefix 输入 / 显示全部,/s 仅 s 开头,无匹配为空(需求语义)。
func TestFilterHintsPrefix(t *testing.T) {
	specs := []sdk.CommandSpec{
		testSpec("jobs", "后台任务"),
		testSpec("sandbox", "切沙箱档"),
		testSpec("settings", "历史注入"),
		testSpec("model", "切换模型"),
	}
	// 空前缀 = 全部(输入 / 时)
	all := filterHints(specs, "")
	if len(all) != 4 {
		t.Fatalf("'/': 应显示全部 4 条, got %d: %v", len(all), all)
	}
	for _, h := range all {
		if h.Value == "" {
			t.Fatalf("选项 Value 不应为空: %+v", h)
		}
	}
	// /s → sandbox/settings(s 开头)
	s := filterHints(specs, "s")
	if len(s) != 2 || s[0].Value != "sandbox" || s[1].Value != "settings" {
		t.Fatalf("'/s': 应只显示 s 开头 2 条: %+v", s)
	}
	// /set → settings(唯一)
	set := filterHints(specs, "set")
	if len(set) != 1 || set[0].Value != "settings" {
		t.Fatalf("'/set': 应只显示 settings: %+v", set)
	}
	// /x → 无匹配
	x := filterHints(specs, "x")
	if len(x) != 0 {
		t.Fatalf("'/x': 无匹配应为空: %+v", x)
	}
}

// TestRenderPickHighlight 选择器激活:高亮行带头 ▸,非选中行不带。
func TestRenderPickHighlight(t *testing.T) {
	s := &State{Input: "/"}
	s.Pick = &Pick{Items: []sdk.Option{{Value: "sandbox", Desc: "切沙箱档"}, {Value: "settings", Desc: "历史注入"}}, Cursor: 0}
	out := Render(s, 80, 24)
	if !strings.Contains(out, "▸ /sandbox") {
		t.Fatalf("选中行应带头 ▸:\n%s", out)
	}
	if strings.Contains(out, "▸ /settings") {
		t.Fatal("非选中行不应带头 ▸")
	}
	// 移动后高亮切换
	s.Pick.Cursor = 1
	out2 := Render(s, 80, 24)
	if !strings.Contains(out2, "▸ /settings") || strings.Contains(out2, "▸ /sandbox") {
		t.Fatalf("光标移动后高亮应切换\n%s", out2)
	}
}

// TestRenderHintArea 提示区渲染:有 Suggestions 时出现提示行且会话流高度收缩。
// 高度验证:20 条历史行时,无提示可见全部;2 条提示挤出最早行(mainH 收缩)。
func TestRenderHintArea(t *testing.T) {
	s := &State{Input: "/"}
	for i := 0; i < 20; i++ {
		s.Lines = append(s.Lines, Line{Kind: "user", Text: "行序号 " + fmt.Sprint(i)})
	}
	out := Render(s, 80, 24)
	if strings.Contains(out, "/jobs 后台任务") {
		t.Fatal("无输入状态不应出现提示行")
	}
	// 无提示:mainH=21,20 行全可见(含最早行)
	if !strings.Contains(out, "行序号 0") {
		t.Fatal("无提示时最早行应可见(mainH=21)")
	}
	// 有提示:mainH 收缩,最早行被挤出
	s.Suggestions = []string{" /jobs 后台任务", " /sandbox 切沙箱档"}
	out2 := Render(s, 80, 24)
	if !strings.Contains(out2, "/jobs 后台任务") {
		t.Fatal("Suggestions 应渲染为提示行")
	}
	if strings.Contains(out2, "行序号 0") {
		t.Fatal("提示区存在时最早行应被挤出(mainH 动态收缩)")
	}
	// 提示行数上限:超 maxHintRows 截断
	s.Suggestions = make([]string, 20)
	out3 := Render(s, 80, 24)
	if strings.Count(out3, "…") == 0 && strings.Contains(out3, "/") {
		t.Fatal("超限提示应截断")
	}
}
