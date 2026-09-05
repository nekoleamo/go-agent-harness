// P4-12 T5 widget 槽位单测:行内容求值/开关/布局扣减(不挤输入)、/widgets 命令、AddWidget 注册。
package tui

import (
	"strings"
	"testing"
)

func TestWidgetLinesRendering(t *testing.T) {
	s := &State{WidgetOn: true, Widgets: []Widget{
		{ID: "todo", Text: func() string { return "任务: 1 进行中" }},
		{ID: "empty", Text: func() string { return "" }},          // 空返回不显示
		{ID: "multi", Text: func() string { return "多\n行 信息" }}, // 单行化
		{ID: "nil", Text: nil},                                    // nil 跳过
	}}
	lines := widgetLines(s)
	if len(lines) != 2 {
		t.Fatalf("应显示 2 条(空/nil 跳过): %+v", lines)
	}
	if lines[0] != "任务: 1 进行中" {
		t.Fatalf("动态文本: %q", lines[0])
	}
	if lines[1] != "多 行 信息" {
		t.Fatalf("应单行化: %q", lines[1])
	}
	// 开关关:不显示
	s.WidgetOn = false
	if lines := widgetLines(s); lines != nil {
		t.Fatalf("开关关应无 widget 行: %+v", lines)
	}
}

func TestWidgetLinesTruncate(t *testing.T) {
	long := strings.Repeat("长", 200)
	s := &State{WidgetOn: true, Widgets: []Widget{{ID: "l", Text: func() string { return long }}}}
	lines := widgetLines(s)
	if len([]rune(lines[0])) > 100 {
		t.Fatalf("超长应截断: %d", len([]rune(lines[0])))
	}
	if !strings.HasSuffix(lines[0], "…") {
		t.Fatalf("截断应带省略号: %q", lines[0])
	}
}

func TestWidgetLayoutDeduction(t *testing.T) {
	// 6 行 user + widget 1 条 + 单行输入:widget 占一行 → 主区少一行(输出行数恒定)
	s := &State{Input: "hi", Cursor: 2, WidgetOn: true, Widgets: []Widget{{ID: "w", Text: func() string { return "动态" }}}}
	for i := 0; i < 6; i++ {
		s.Lines = append(s.Lines, Line{Kind: "user", Text: "行" + string(rune('0'+i))})
	}
	out := Render(s, 80, 10)
	lines := strings.Split(out, "\n")
	if len(lines) != 9 { // 恒定 height-1
		t.Fatalf("输出行数应恒定: %d", len(lines))
	}
	if !strings.Contains(stripColor(lines[6]), "◇ 动态") {
		t.Fatalf("widget 应渲染于输入行上方: %q", lines[6])
	}
	// 关闭后 widget 行消失,主区恢复一行(最早行可见范围变化)
	s.WidgetOn = false
	out2 := Render(s, 80, 10)
	lines2 := strings.Split(out2, "\n")
	for _, l := range lines2 {
		if strings.Contains(stripColor(l), "◇") {
			t.Fatalf("关闭后不应渲染 widget: %q", l)
		}
	}
}

func TestWidgetsCommandToggle(t *testing.T) {
	a := &App{model: &Model{state: &State{}}}
	a.AddWidget("demo", func() string { return "演示" })
	if out, err := a.cmdWidgets(nil); err != nil || out == "" {
		t.Fatalf("开启命令应返回提示: %v %v", out, err)
	}
	if !a.model.state.WidgetOn {
		t.Fatal("无参应开启")
	}
	if out, err := a.cmdWidgets([]string{"off"}); err != nil || a.model.state.WidgetOn {
		t.Fatalf("off 应关闭: %v %v", out, err)
	}
	// AddWidget 校验:空 ID/nil Text 忽略
	before := len(a.widgets)
	a.AddWidget("", nil)
	if len(a.widgets) != before {
		t.Fatal("空注册应忽略")
	}
}
