// miniwindow 单测:/api/state 解析、提示筛选、阶段→界面、按钮→副作用、轮询启停。
// 全部 headless(ui.NewTester / 假数据出口),不碰网络。
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func stateJSON(t *testing.T, v map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// 连点「重启服务」只该起一次(两次会并发停/起 sidecar)。
func TestMiniRestartGuard(t *testing.T) {
	p := &miniPanel{}
	calls := 0
	// 第一次还在进行中时再点一次:守卫必须挡住(直接串两次会被「瞬时假实现」
	// 复位标记掩盖 —— 那正是这条用例第一版自证无效的原因)。
	p.doRestart = func() {
		calls++
		p.runRestart()
	}
	p.runRestart()
	if calls != 1 {
		t.Fatalf("restart 调用 = %d, want 1", calls)
	}
	if p.restarting.Load() {
		t.Fatal("重启结束后 restarting 位没复位")
	}
}

// 重启期间状态取不到,但界面说的是「正在重启服务…」而不是「服务失联」。
func TestMiniRestartingStatusWins(t *testing.T) {
	if got := miniStatusLine(miniSnap{restarting: true}); got != "正在重启服务…" {
		t.Fatalf("line = %q", got)
	}
}

// 解析:会话名/角色/版本/「另有几个在跑」都要各归各位。
func TestParseMiniState(t *testing.T) {
	raw := stateJSON(t, map[string]any{
		"running":          true,
		"model":            "deepseek-v4",
		"role":             "reviewer",
		"role_name":        "审查员",
		"version":          "0.10.1",
		"session":          map[string]any{"name": "改托盘图标"},
		"running_sessions": []string{"", "s2", "s3"},
	})
	s, ok := parseMiniState(raw)
	if !ok {
		t.Fatal("parse 失败")
	}
	if !s.reachable || !s.running {
		t.Fatalf("reachable/running = %v/%v", s.reachable, s.running)
	}
	if s.session != "改托盘图标" || s.model != "deepseek-v4" || s.version != "0.10.1" {
		t.Fatalf("字段错: %+v", s)
	}
	if s.role != "审查员" {
		t.Fatalf("role = %q(应优先 role_name)", s.role)
	}
	// 空键是主会话,不算「别的」
	if s.others != 2 {
		t.Fatalf("others = %d, want 2", s.others)
	}
}

// 形状不符不猜:不是 JSON 就 ok=false(界面走「服务失联」)。
func TestParseMiniStateRejectsGarbage(t *testing.T) {
	if _, ok := parseMiniState([]byte("<html>502</html>")); ok {
		t.Fatal("坏载荷被当成成功")
	}
}

// 只有 warn/error 值得摆,且最多 3 条、取最后几条。
func TestPickNotices(t *testing.T) {
	items := []noticeItem{
		{ID: 1, Level: "info", Title: "闲聊"},
		{ID: 2, Level: "warn", Title: "w1"},
		{ID: 3, Level: "error", Title: "e1"},
		{ID: 4, Level: "info", Title: "闲聊2"},
		{ID: 5, Level: "warn", Title: "w2"},
		{ID: 6, Level: "error", Title: "e2"},
	}
	got := pickNotices(items, 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].ID != 3 || got[2].ID != 6 {
		t.Fatalf("取的不是最后三条: %v", []uint64{got[0].ID, got[1].ID, got[2].ID})
	}
	for _, it := range got {
		if it.Level != "warn" && it.Level != "error" {
			t.Fatalf("info 混进来了: %+v", it)
		}
	}
}

// 三种状态各一句,且状态色不串。
func TestMiniStatus(t *testing.T) {
	cases := []struct {
		snap  miniSnap
		line  string
		color func(ui.Theme) ui.Color
	}{
		{miniSnap{}, "服务失联", func(t ui.Theme) ui.Color { return t.Danger }},
		{miniSnap{reachable: true}, "空闲", func(t ui.Theme) ui.Color { return t.TextMuted }},
		{miniSnap{reachable: true, running: true}, "回合进行中", func(t ui.Theme) ui.Color { return t.Success }},
	}
	theme := *ui.LightTheme()
	for _, c := range cases {
		if got := miniStatusLine(c.snap); got != c.line {
			t.Fatalf("line = %q, want %q", got, c.line)
		}
		if got, want := miniStatusColor(theme, c.snap), c.color(theme); got != want {
			t.Fatalf("color(%q) = %v, want %v", c.line, got, want)
		}
	}
}

// 界面:会话名缺省回落「主会话」;失联文案要出现;提示条按级别上色。
func TestMiniViewRenders(t *testing.T) {
	p := &miniPanel{snap: miniSnap{
		reachable: true, running: true, model: "deepseek-v4", role: "审查员",
		notices: []noticeItem{{Level: "error", Title: "回合失败", Body: "第一行\n第二行"}},
	}}
	tt := ui.NewTester(p.view, 360, 300)
	tt.Frame()
	joined := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"回合进行中", "主会话", "deepseek-v4", "角色 审查员", "回合失败", "第一行", "打开 gah", "新会话", "重启服务"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("缺 %q;现有 %q", want, tt.Texts())
		}
	}

	p2 := &miniPanel{snap: miniSnap{}}
	tt2 := ui.NewTester(p2.view, 360, 300)
	tt2.Frame()
	if !strings.Contains(strings.Join(tt2.Texts(), "\n"), "服务失联") {
		t.Fatalf("失联文案缺失: %q", tt2.Texts())
	}
}

// 三个按钮各走各的出口。
func TestMiniActions(t *testing.T) {
	p := &miniPanel{snap: miniSnap{reachable: true}}
	opens, news, restarts := 0, 0, 0
	p.doOpen = func() { opens++ }
	p.doNew = func() { news++ }
	p.doRestart = func() { restarts++ }

	tt := ui.NewTester(p.view, 360, 300)
	for _, label := range []string{"打开 gah", "新会话", "重启服务"} {
		if err := tt.Click(label); err != nil {
			t.Fatalf("点 %s 失败: %v", label, err)
		}
	}
	if opens != 1 || news != 1 || restarts != 1 {
		t.Fatalf("opens=%d news=%d restarts=%d, want 1/1/1", opens, news, restarts)
	}
}

// 一轮轮询:状态拿到、提示只留 warn/error、游标推进。
func TestMiniPollOnce(t *testing.T) {
	p := &miniPanel{}
	p.getState = func() ([]byte, bool) {
		return stateJSON(t, map[string]any{"running": true, "model": "m1", "version": "0.10.1"}), true
	}
	p.notices = func(since uint64) (noticePage, bool) {
		return noticePage{
			MaxID: 7,
			Items: []noticeItem{{ID: 6, Level: "info", Title: "i"}, {ID: 7, Level: "warn", Title: "w"}},
		}, true
	}
	p.pollOnce()

	p.mu.Lock()
	s := p.snap
	cursor := p.cursor
	p.mu.Unlock()
	if !s.reachable || !s.running || s.model != "m1" {
		t.Fatalf("状态没刷上: %+v", s)
	}
	if len(s.notices) != 1 || s.notices[0].ID != 7 {
		t.Fatalf("提示筛选错: %+v", s.notices)
	}
	if cursor != 7 {
		t.Fatalf("cursor = %d, want 7", cursor)
	}
}

// 这一轮拿不到状态:标失联,但上一次的提示不该被清空(清空=假装没发生过)。
func TestMiniPollKeepsNoticesWhenStateFails(t *testing.T) {
	p := &miniPanel{}
	p.getState = func() ([]byte, bool) { return nil, false }
	p.notices = func(uint64) (noticePage, bool) { return noticePage{}, false }
	p.snap = miniSnap{reachable: true, notices: []noticeItem{{ID: 9, Level: "error", Title: "旧错误"}}}

	p.pollOnce()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snap.reachable {
		t.Fatal("拿不到状态却仍标可达")
	}
	if len(p.snap.notices) != 1 || p.snap.notices[0].Title != "旧错误" {
		t.Fatalf("提示被清空了: %+v", p.snap.notices)
	}
}

// 轮询启停:重复 start 不叠加,重复 stop 不 panic。
func TestMiniPollingIdempotent(t *testing.T) {
	p := &miniPanel{}
	p.getState = func() ([]byte, bool) { return nil, false }
	p.notices = func(uint64) (noticePage, bool) { return noticePage{}, false }

	p.startPolling()
	p.startPolling()
	p.stopPolling()
	p.stopPolling()

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		t.Fatal("stop 之后仍持有 stop chan")
	}
}
