// 选择器内过滤(P4 交互)测试:参数级直接打字即时子串过滤,退格/Esc 恢复,无匹配回车不提交。
package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbletea/v2"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func mkopt(v, d string) sdk.Option { return sdk.Option{Value: v, Desc: d} }

func TestFilterOptions(t *testing.T) {
	items := []sdk.Option{mkopt("web_search", "联网搜索"), mkopt("web_fetch", "抓取正文"), mkopt("read", "读文件")}
	if got := filterOptions(items, ""); len(got) != 3 {
		t.Fatalf("空过滤应原样: %d", len(got))
	}
	if got := filterOptions(items, "web"); len(got) != 2 {
		t.Fatalf("web 应命中 2: %+v", got)
	}
	if got := filterOptions(items, "WEB"); len(got) != 2 {
		t.Fatalf("大小写不敏感应命中: %+v", got)
	}
	if got := filterOptions(items, "抓取"); len(got) != 1 || got[0].Value != "web_fetch" {
		t.Fatalf("Desc 应可命中: %+v", got)
	}
	if got := filterOptions(items, "zzz"); len(got) != 0 {
		t.Fatalf("无匹配应为空")
	}
}

// pickKeyModel 构造带参数级选择器的 model(供 handleKey 驱动)。
func pickKeyModel(items []sdk.Option) *Model {
	all := append([]sdk.Option(nil), items...)
	return &Model{state: &State{Pick: &Pick{Level: 1, Items: items, All: all}}}
}

func pickKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

func TestPickFilterTyping(t *testing.T) {
	items := []sdk.Option{mkopt("web_search", "联网搜索"), mkopt("web_fetch", "抓取正文"), mkopt("bash", "执行命令")}
	m := pickKeyModel(items)
	m.handleKey(pickKey('w'))
	m.handleKey(pickKey('e'))
	if m.state.Pick.Filter != "we" || len(m.state.Pick.Items) != 2 {
		t.Fatalf("打字应过滤: %+v", m.state.Pick)
	}
	m.handleKey(pickKey('b'))
	if len(m.state.Pick.Items) != 2 {
		t.Fatalf("web 过滤应剩 2: %+v", m.state.Pick.Items)
	}
	// 退格删 'b' 恢复 web
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.state.Pick.Filter != "we" || len(m.state.Pick.Items) != 2 {
		t.Fatalf("退格应回退一步: %+v", m.state.Pick)
	}
	// 退格到空:恢复全量(过滤词为空,Items=All)
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.state.Pick.Filter != "" || len(m.state.Pick.Items) != 3 {
		t.Fatalf("过滤清空应恢复全量: %+v", m.state.Pick)
	}
	// 参数级过滤中退格空词:命令文本不被破坏
	if m.state.Input != "" {
		t.Fatalf("参数级退格不应写命令文本: %q", m.state.Input)
	}
}

func TestPickFilterEscAndDismiss(t *testing.T) {
	items := []sdk.Option{mkopt("a1", "一"), mkopt("b2", "二")}
	m := pickKeyModel(items)
	m.handleKey(pickKey('a'))
	if m.state.Pick == nil {
		t.Fatal("过滤态选择器应存在")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.state.Pick == nil || m.state.Pick.Filter != "" || len(m.state.Pick.Items) != 2 {
		t.Fatalf("首 Esc 应清过滤恢复全量: %+v", m.state.Pick)
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.state.Pick != nil {
		t.Fatal("次 Esc 应退出选择器")
	}
}

func TestPickFilterNoMatchEnterNoCommit(t *testing.T) {
	items := []sdk.Option{mkopt("read", "读"), mkopt("write", "写")}
	m := pickKeyModel(items)
	// 过滤到无匹配
	m.handleKey(pickKey('z'))
	if len(m.state.Pick.Items) != 0 {
		t.Fatalf("应过滤到空: %+v", m.state.Pick.Items)
	}
	// 回车:不提交、选择器保留(无匹配不执行当前命令文本)
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.state.Pick == nil {
		t.Fatal("无匹配回车不应退出/提交")
	}
	if m.state.Input != "" {
		t.Fatalf("无匹配回车不应改写命令文本: %q", m.state.Input)
	}
}

// TestRenderPickFilterStatus 过滤状态行渲染:首行显示过滤词与命中数,选项窗口缩小 1 行。
func TestRenderPickFilterStatus(t *testing.T) {
	all := make([]sdk.Option, 20)
	for i := range all {
		all[i] = sdk.Option{Value: "opt" + string(rune('a'+i%26)) + itoa(i), Desc: "d" + itoa(i)}
	}
	pick := &Pick{Level: 1, Items: filterOptions(all, "a"), All: all, Filter: "a", Cursor: 0}
	s := &State{Pick: pick}
	out := Render(s, 80, 24)
	if !strings.Contains(out, "过滤: a") {
		t.Fatalf("应显示过滤状态行")
	}
	if !strings.Contains(out, "▸") {
		t.Fatalf("过滤后窗口应含高亮项")
	}
}

// itoa 测试小助手。
func itoa(i int) string { return fmt.Sprint(i) }
