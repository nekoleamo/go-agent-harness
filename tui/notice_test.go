// NOND-N1 TUI 端提示回归:状态栏 notice 项(级别/颜色/空态)、State 消费语义
// (只认更新的 id / 用户提交即清)、/notice 详情浮层(未装配显式报错,装配后出正文)。
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubNotices ctx.notices 替身(只实现 List —— /notice 命令的回填口)。
type stubNotices struct{ page sdk.NoticePage }

func (s stubNotices) Publish(sdk.Notice) uint64  { return 0 }
func (s stubNotices) List(uint64) sdk.NoticePage { return s.page }

// TestStateApplyNoticeKeepsNewest 只认更新的 id:乱序/重放不得把更新的提示顶掉。
func TestStateApplyNoticeKeepsNewest(t *testing.T) {
	s := &State{}
	s.ApplyNotice(nil)
	if s.Notice != nil {
		t.Fatal("nil 提示不入状态")
	}
	s.ApplyNotice(&sdk.Notice{ID: 0, Title: "无 id"})
	if s.Notice != nil {
		t.Fatal("ID=0(未分配)不入状态")
	}
	s.ApplyNotice(&sdk.Notice{ID: 3, Level: sdk.NoticeWarn, Title: "新"})
	s.ApplyNotice(&sdk.Notice{ID: 2, Level: sdk.NoticeError, Title: "旧"})
	if s.Notice == nil || s.Notice.ID != 3 || s.Notice.Title != "新" {
		t.Fatalf("旧 id 不得覆盖新提示: %+v", s.Notice)
	}
	s.ApplyNotice(&sdk.Notice{ID: 4, Level: sdk.NoticeError, Title: "更新"})
	if s.Notice.ID != 4 || s.Notice.Title != "更新" {
		t.Fatalf("更新 id 应覆盖: %+v", s.Notice)
	}
	// 副本语义:调用方事后改动不得回写状态
	n := &sdk.Notice{ID: 5, Title: "五"}
	s.ApplyNotice(n)
	n.Title = "被改"
	if s.Notice.Title != "五" {
		t.Fatal("状态应持副本(不得被调用方后续改动污染)")
	}
}

// TestStateConsumeNotice 用户开口 = 人已回来:提示清场。
func TestStateConsumeNotice(t *testing.T) {
	s := &State{}
	s.ApplyNotice(&sdk.Notice{ID: 1, Title: "x"})
	s.ConsumeNotice()
	if s.Notice != nil {
		t.Fatal("提交后提示应清空")
	}
}

// TestStatuslineNoticeItem 状态栏 notice 项:空态不渲染、级别决定颜色与标记、标题裁宽。
func TestStatuslineNoticeItem(t *testing.T) {
	if got := statuslineItem(&State{}, "notice"); got != "" {
		t.Fatalf("无提示应渲染空串(默认基线逐字符不变): %q", got)
	}
	long := strings.Repeat("字", 60)
	s := &State{Notice: &sdk.Notice{ID: 1, Level: sdk.NoticeError, Title: long}}
	out := stripColor(statuslineItem(s, "notice"))
	if !strings.HasPrefix(out, "✗ ") || !strings.Contains(out, "(/notice)") {
		t.Fatalf("error 级应带 ✗ 与详情入口: %q", out)
	}
	if runes := []rune(out); len(runes) > 60 {
		t.Fatalf("标题应裁到 40 列(含标记/入口也不该占满): %q", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("超长标题裁剪应显式加省略号: %q", out)
	}
	s.Notice = &sdk.Notice{ID: 2, Level: sdk.NoticeWarn, Title: "计划失败"}
	if out := stripColor(statuslineItem(s, "notice")); !strings.HasPrefix(out, "⚠ 计划失败") {
		t.Fatalf("warn 级应带 ⚠: %q", out)
	}
	s.Notice = &sdk.Notice{ID: 3, Level: sdk.NoticeInfo, Title: "任务完成"}
	if out := stripColor(statuslineItem(s, "notice")); !strings.HasPrefix(out, "提示: 任务完成") {
		t.Fatalf("info 级用中性前缀: %q", out)
	}
	// 默认基线含 notice 项(否则插件/宿主发出的提示在 TUI 永远不可见)
	if len(defaultStatusline) == 0 || defaultStatusline[0] != "notice" {
		t.Fatalf("默认状态栏首项应为 notice: %v", defaultStatusline)
	}
	if statuslineTokenDesc["notice"] == "" {
		t.Fatal("/statusline 校验表应登记 notice(否则配置该词会被当成坏项丢弃)")
	}
}

// TestCommandNoticeNotAssembled 未装配 ctx.notices → 显式可操作错误(不静默空转)。
func TestCommandNoticeNotAssembled(t *testing.T) {
	a := commandTestApp()
	if _, err := a.cmdNotice(nil); err == nil || !strings.Contains(err.Error(), "host-notices") {
		t.Fatalf("未装配应报可操作错误: %v", err)
	}
}

// TestCommandNoticeRendersDetails 装配后:空缓冲给明说、有条目出新→旧详单与 gap 标注。
func TestCommandNoticeRendersDetails(t *testing.T) {
	a := commandTestApp()
	a.c = &stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry(), "ctx.notices": stubNotices{}}}

	out, err := a.cmdNotice(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "暂无提示") || a.model.state.Doc != nil {
		t.Fatalf("空缓冲应只回文案、不弹浮层: %q %v", out, a.model.state.Doc)
	}

	ts := time.Date(2026, 9, 18, 10, 30, 0, 0, time.Local)
	a.c = &stubCtx{svc: map[string]any{"ctx.commands": newMemRegistry(), "ctx.notices": stubNotices{
		page: sdk.NoticePage{
			MaxID: 2, Gap: true, Suppressed: 4,
			Items: []sdk.Notice{
				{ID: 1, Level: sdk.NoticeWarn, Title: "旧的", Body: "细节一", Source: "host-schedule", TS: ts},
				{ID: 2, Level: sdk.NoticeError, Title: "新的", Body: "第一行\n第二行", Source: "host-jobs", TS: ts},
			},
		},
	}}}
	if _, err := a.cmdNotice(nil); err != nil {
		t.Fatal(err)
	}
	p := a.model.state.Doc
	if p == nil {
		t.Fatal("/notice 有条目应打开浮层")
	}
	view := strings.Join(p.Lines, "\n")
	if !strings.Contains(view, "提示共 2 条") || !strings.Contains(view, "不完整") {
		t.Fatalf("抬头应报条数与 gap: %q", view)
	}
	if !strings.Contains(view, "4 条重复提示已被去重") {
		t.Fatalf("去重条数应显式可见(不静默): %q", view)
	}
	if !strings.Contains(view, "[error]") || !strings.Contains(view, "新的") {
		t.Fatalf("应含详单: %q", view)
	}
	if !strings.Contains(view, "    第二行") {
		t.Fatalf("正文多行应缩进展开: %q", view)
	}
	if iNew, iOld := strings.Index(view, "新的"), strings.Index(view, "旧的"); iNew > iOld {
		t.Fatalf("应最新在前: %q", view)
	}
	if !strings.Contains(view, "10:30:00") {
		t.Fatalf("应带时间戳: %q", view)
	}
}
