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
// 高度验证:18 条历史行时,无提示窗口 mainH=16 可见末尾 16 条(输入区含圆角框边框);
// 2 条提示再挤出两行(mainH 收缩)。
func TestRenderHintArea(t *testing.T) {
	s := &State{Input: "/"}
	for i := 0; i < 18; i++ {
		s.Lines = append(s.Lines, Line{Kind: "user", Text: "行序号 " + fmt.Sprint(i)})
	}
	out := Render(s, 80, 24)
	if strings.Contains(out, "/jobs 后台任务") {
		t.Fatal("无输入状态不应出现提示行")
	}
	// 无提示:F15.5 空隙行后 mainH=16,窗口显示末尾 16 条(行序号 2 起)
	if !strings.Contains(out, "行序号 2") || strings.Contains(out, "行序号 0") {
		t.Fatal("无提示时窗口应显示末尾 16 条(mainH=16): 最早行被挤出")
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

// TestStatusBarSpinnerAndWorkspace 思考动画/工具执行态/工作区显示(状态栏)。
func TestStatusBarSpinnerAndWorkspace(t *testing.T) {
	s := &State{Profile: "tui", Workspace: "go-agent-harness", Model: "mock"}
	// 空闲:无动画、显示工作区
	out := Render(s, 80, 24)
	if !strings.Contains(out, "工作区: go-agent-harness") {
		t.Fatalf("状态栏应显示工作区:\n%s", out)
	}
	// 思考中:动画帧 + Esc 取消提示
	s.Running = true
	out2 := Render(s, 80, 24)
	frame := spinnerFrames[s.SpinnerIdx%len(spinnerFrames)]
	if !strings.Contains(out2, "思考中") || !strings.Contains(out2, frame) {
		t.Fatalf("回合中应显示思考动画:\n%s", out2)
	}
	if !strings.Contains(out2, "Esc 取消") {
		t.Fatal("应提示 Esc 可取消")
	}
	// 工具执行:显示工具名
	s.LastTool = "shell"
	out3 := Render(s, 80, 24)
	if !strings.Contains(out3, "执行工具: shell") {
		t.Fatalf("工具执行应显示工具名:\n%s", out3)
	}
}

// TestStatusBarUsageStats 状态栏统计段:上下文使用率/缓存命中率;无请求显示 -;会话 id 显示。
func TestStatusBarUsageStats(t *testing.T) {
	s := &State{Profile: "tui", Workspace: "go-agent-harness"}
	// 无请求:上下文 -
	out := Render(s, 80, 24)
	if !strings.Contains(out, "上下文 -") {
		t.Fatalf("无请求应显示 上下文 -:\n%s", out)
	}
	// 有统计:使用率 + 缓存命中率;窗口来自 Stats.Window
	s.Stats = sdk.UsageStats{PromptTokens: 16384, CachedTokens: 8192, Requests: 3, Window: 65536}
	out2 := Render(s, 80, 24)
	if !strings.Contains(out2, "上下文 16.0K/64.0K (25%)") {
		t.Fatalf("应显示上下文使用率: 16.0K/64.0K (25%%):\n%s", out2)
	}
	if !strings.Contains(out2, "缓存 50%") {
		t.Fatalf("应显示缓存命中率 50%%:\n%s", out2)
	}
	// 缓存为 0:不显示缓存段(避免误导 0%)
	s.Stats = sdk.UsageStats{PromptTokens: 1000, Requests: 1, Window: 65536}
	out3 := Render(s, 80, 24)
	if strings.Contains(out3, "缓存") {
		t.Fatalf("无缓存命中不应显示缓存段:\n%s", out3)
	}
	// 会话 id 显示
	s.Session = "20240103-1400"
	out4 := Render(s, 80, 24)
	if !strings.Contains(out4, "会话: 20240103-1400") {
		t.Fatalf("状态栏应显示会话 id:\n%s", out4)
	}
	// 主会话(空 id)不显示会话段
	s.Session = ""
	out5 := Render(s, 80, 24)
	if strings.Contains(out5, "会话:") {
		t.Fatalf("主会话不应显示会话段:\n%s", out5)
	}
}

// TestUsageStatsZeroWindow 窗口未配置(0)时使用率不除零崩溃,显示 -。
func TestUsageStatsZeroWindow(t *testing.T) {
	s := &State{Profile: "tui", Stats: sdk.UsageStats{PromptTokens: 100, Requests: 1}}
	out := Render(s, 80, 24)
	if strings.Contains(out, "(NaN%)") || strings.Contains(out, "(+Inf%)") {
		t.Fatalf("窗口 0 不应出现 NaN/Inf:\n%s", out)
	}
}

// TestUsageStatsUnknownWindow 窗口未知(0,未知/空模型):仅显示使用量,不显示总量/百分比。
func TestUsageStatsUnknownWindow(t *testing.T) {
	s := &State{Profile: "tui", Stats: sdk.UsageStats{PromptTokens: 1234, Requests: 1, Window: 0}}
	out := Render(s, 80, 24)
	if !strings.Contains(out, "上下文 1.2K") {
		t.Fatalf("未知窗口应仅显示使用量 1.2K:\n%s", out)
	}
	if strings.Contains(out, "1.2K/") {
		t.Fatalf("未知窗口不应显示总量:\n%s", out)
	}
	if strings.Contains(out, "%") && strings.Contains(out, "1.2K") {
		t.Fatalf("未知窗口不应显示百分比:(%%)附近:\n%s", out)
	}
	// 窗口已知时显示 使用量/总量 (百分比)
	s.Stats = sdk.UsageStats{PromptTokens: 1234, Requests: 1, Window: 65536}
	out2 := Render(s, 80, 24)
	if !strings.Contains(out2, "1.2K/64.0K (1%)") {
		t.Fatalf("窗口已知应显示总量与百分比:\n%s", out2)
	}
}
