// updatepanel 单测:阶段→界面映射、按钮→副作用、进度文案边界。
//
// 全部在 ui.NewTester 里跑(内存渲帧、不建窗口),所以 `go test` 不需要图形会话;
// 副作用出口(doCheck/doInstall/doRestart/doClose)注入假实现,不碰网络与文件。
package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// testPanel 一个只有状态、没有 shell 的面板(副作用出口全部由用例接管)。
func testPanel(s panelSnap) *updatePanel {
	return &updatePanel{snap: s}
}

// 阶段 → 标题/按钮的映射。这张表是面板的全部「产品口径」,改它就是改行为。
func TestPanelPhaseRenders(t *testing.T) {
	cases := []struct {
		name    string
		snap    panelSnap
		title   string
		wantHas []string
	}{
		{"idle", panelSnap{phase: panelIdle}, "更新", []string{"检查更新"}},
		{"checking", panelSnap{phase: panelChecking}, "正在检查更新…", []string{"正在跟更新源核对版本…"}},
		{"current", panelSnap{phase: panelCurrent, message: "当前版本 0.10.2 已经是最新的。"}, "已是最新",
			[]string{"当前版本 0.10.2 已经是最新的。", "再次检查"}},
		{"available", panelSnap{phase: panelAvailable, version: "0.11.0", notes: "修了托盘", expect: "0.11.0"},
			"发现新版本 0.11.0", []string{"修了托盘", "下载并安装", "稍后"}},
		{"available-no-notes", panelSnap{phase: panelAvailable, version: "0.11.0"}, "发现新版本 0.11.0",
			[]string{"这个版本没带更新说明。", "下载并安装"}},
		{"downloading", panelSnap{phase: panelDownloading, version: "0.11.0", written: 2 << 20, total: 8 << 20},
			"正在下载 0.11.0", []string{"已下载 2.0 MB / 8.0 MB", "取消下载"}},
		{"downloading-unknown-total", panelSnap{phase: panelDownloading, version: "0.11.0", written: 3 << 20},
			"正在下载 0.11.0", []string{"已下载 3.0 MB", "取消下载"}},
		{"ready", panelSnap{phase: panelReady, version: "0.11.0"}, "更新已就绪",
			[]string{"新版本 0.11.0 已就绪,重启后生效。", "立即重启", "稍后"}},
		{"failed", panelSnap{phase: panelFailed, message: "连不上更新源(超时):xxx"}, "更新失败",
			[]string{"连不上更新源(超时):xxx", "重试", "关闭"}},
		// 被挡住 ≠ 失败:便携版重试没有意义,给的是「去下载」(发行页)。
		{"blocked", panelSnap{phase: panelBlocked, message: "便携版不自动更新:请下载新包覆盖本目录"}, "无法自动更新",
			[]string{"便携版不自动更新", "去下载", "关闭"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := testPanel(c.snap)
			tt := ui.NewTester(p.view, 420, 320)
			tt.Frame()
			if !tt.HasText(c.title) {
				t.Fatalf("标题缺 %q,现有 %q", c.title, tt.Texts())
			}
			for _, want := range c.wantHas {
				if !strings.Contains(strings.Join(tt.Texts(), "\n"), want) {
					t.Fatalf("缺文案 %q,现有 %q", want, tt.Texts())
				}
			}
		})
	}
}

// 「下载并安装」必须把**用户看到的那个版本**带进安装(对账用),不是当前 seen。
func TestPanelInstallCarriesSeenVersion(t *testing.T) {
	p := testPanel(panelSnap{phase: panelAvailable, version: "0.11.0", expect: "0.11.0"})
	var got []string
	p.doInstall = func(expect string) { got = append(got, expect) }

	tt := ui.NewTester(p.view, 420, 320)
	if err := tt.Click("下载并安装"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "0.11.0" {
		t.Fatalf("install 调用 = %v, want [0.11.0]", got)
	}
}

// 就绪态:立即重启走重启出口;稍后只收面板。
func TestPanelReadyActions(t *testing.T) {
	p := testPanel(panelSnap{phase: panelReady, version: "0.11.0"})
	restarts, closes := 0, 0
	p.doRestart = func() { restarts++ }
	p.doClose = func() { closes++ }

	tt := ui.NewTester(p.view, 420, 320)
	if err := tt.Click("立即重启"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("稍后"); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 || closes != 1 {
		t.Fatalf("restarts=%d closes=%d, want 1/1", restarts, closes)
	}
}

// 被挡住时「去下载」走打开发行页,而不是再查一次(便携版重试无意义)。
func TestPanelBlockedDownload(t *testing.T) {
	p := testPanel(panelSnap{phase: panelBlocked, message: "便携版不自动更新"})
	downloads, checks := 0, 0
	p.doDownload = func() { downloads++ }
	p.doCheck = func() { checks++ }

	tt := ui.NewTester(p.view, 420, 320)
	if err := tt.Click("去下载"); err != nil {
		t.Fatal(err)
	}
	if downloads != 1 || checks != 0 {
		t.Fatalf("downloads=%d checks=%d, want 1/0", downloads, checks)
	}
}

// 下载态:取消必须调到给自己留下的那个 cancel(而不是新起一次下载)。
func TestPanelCancelDownload(t *testing.T) {
	p := testPanel(panelSnap{phase: panelDownloading, version: "0.11.0"})
	canceled := 0
	p.cancel = func() { canceled++ }

	tt := ui.NewTester(p.view, 420, 320)
	if err := tt.Click("取消下载"); err != nil {
		t.Fatal(err)
	}
	if canceled != 1 {
		t.Fatalf("cancel 调用 = %d, want 1", canceled)
	}
}

// 失败/被挡态的重试与关闭。
func TestPanelFailedActions(t *testing.T) {
	p := testPanel(panelSnap{phase: panelFailed, message: "下载返回 HTTP 404"})
	checks, closes := 0, 0
	p.doCheck = func() { checks++ }
	p.doClose = func() { closes++ }

	tt := ui.NewTester(p.view, 420, 320)
	if err := tt.Click("重试"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("关闭"); err != nil {
		t.Fatal(err)
	}
	if checks != 1 || closes != 1 {
		t.Fatalf("checks=%d closes=%d, want 1/1", checks, closes)
	}
}

// 没有更新器时不许装作在查:直接落到 failed 并说清原因。
func TestPanelCheckWithoutUpdaterFails(t *testing.T) {
	p := testPanel(panelSnap{phase: panelIdle})
	p.check() // sh == nil
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snap.phase != panelFailed {
		t.Fatalf("phase = %q, want %q", p.snap.phase, panelFailed)
	}
	if !strings.Contains(p.snap.message, "更新器未装配") {
		t.Fatalf("message = %q", p.snap.message)
	}
}

// 连点守卫:进行中时第二次 tryBegin 必须失败(备份那几秒按钮还在)。
func TestPanelBusyGuard(t *testing.T) {
	p := &updatePanel{}
	if !p.tryBegin() {
		t.Fatal("首次 tryBegin 失败")
	}
	if p.tryBegin() {
		t.Fatal("进行中又抢到了第二个名额(连点会两次备份 + 两份安装器)")
	}
	p.end()
	if !p.tryBegin() {
		t.Fatal("end 之后抢不回来")
	}
}

// 进度比例:总量未知 = -1(不确定长度),超界钳到 [0,1]。
func TestPanelProgressRatio(t *testing.T) {
	cases := []struct {
		written, total int64
		want           float64
	}{
		{0, 0, -1},
		{5, 0, -1},
		{0, 100, 0},
		{50, 100, 0.5},
		{150, 100, 1},
	}
	for _, c := range cases {
		if got := progressRatio(panelSnap{written: c.written, total: c.total}); got != c.want {
			t.Errorf("progressRatio(%d,%d) = %v, want %v", c.written, c.total, got, c.want)
		}
	}
}

// 进度只在下载态被采纳(检查/就绪态收到迟到的进度不能改界面)。
func TestPanelProgressIgnoredOutsideDownloading(t *testing.T) {
	p := testPanel(panelSnap{phase: panelAvailable, version: "0.11.0"})
	p.progress(9<<20, 10<<20)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snap.written != 0 || p.snap.total != 0 {
		t.Fatalf("written/total = %d/%d, want 0/0", p.snap.written, p.snap.total)
	}
}

// 长说明按字符截断(中文不切半个字)。
func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("更新说明", 10); got != "更新说明" {
		t.Fatalf("got %q", got)
	}
	got := truncateRunes("一二三四五", 3)
	if got != "一二三…" {
		t.Fatalf("got %q, want 一二三…", got)
	}
}
