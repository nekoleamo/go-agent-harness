// /diff 变更审查面单测(S-P1-1)。
// 覆盖:账本聚合(纯函数)、清单文本、单文件 patch、路径匹配(精确/后缀/同名歧义/未命中)、
// diff/open 意图事件载荷(端侧呈现依赖它)。
package hostintcmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// changeEvents 构造账本:两个文件三次改动(含新建/二进制/截断标记)。
func changeEvents() []sdk.SessionEvent {
	base := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	return []sdk.SessionEvent{
		{Kind: sdk.EventUserMessage, Seq: 1, TS: base, Payload: sdk.UserMessage{Content: "改文件"}},
		{Kind: sdk.EventFileChange, Seq: 2, TS: base.Add(time.Second), Payload: sdk.FileChangeEvent{
			Path: "/w/plugins/host/host-jobs/jobs.go", Rel: "plugins/host/host-jobs/jobs.go",
			Op: "edit", Tool: "file_edit", Added: 4, Removed: 1,
			Diff: "@@ -1,3 +1,6 @@\n a\n-b\n+B1\n+B2\n+B3\n+B4\n",
		}},
		{Kind: sdk.EventFileChange, Seq: 3, TS: base.Add(2 * time.Second), Payload: sdk.FileChangeEvent{
			Path: "/w/plugins/host/host-jobs/jobs.go", Rel: "plugins/host/host-jobs/jobs.go",
			Op: "edit", Tool: "file_edit", Added: 1, Removed: 0, Truncated: true,
			Diff: "@@ -9,2 +9,3 @@\n+x\n",
		}},
		{Kind: sdk.EventFileChange, Seq: 4, TS: base.Add(3 * time.Second), Payload: sdk.FileChangeEvent{
			Path: "/w/notes/new.md", Rel: "notes/new.md",
			Op: "write", Tool: "file_write", Added: 8, Removed: 0, Created: true,
			Diff: "@@ -1,0 +1,8 @@\n+n1\n",
		}},
		{Kind: sdk.EventFileChange, Seq: 5, TS: base.Add(4 * time.Second), Payload: sdk.FileChangeEvent{
			Path: "/w/blob.bin", Rel: "blob.bin", Op: "append", Tool: "file_append", Binary: true, Bytes: 99,
		}},
	}
}

func TestCollectFileChangesAggregates(t *testing.T) {
	entries := collectFileChanges(changeEvents())
	if len(entries) != 3 {
		t.Fatalf("应聚合成 3 个文件: %d", len(entries))
	}
	// 按 key 排序:blob.bin < notes/new.md < plugins/...
	if entries[0].key != "blob.bin" || entries[1].key != "notes/new.md" || entries[2].key != "plugins/host/host-jobs/jobs.go" {
		t.Fatalf("排序错误: %s %s %s", entries[0].key, entries[1].key, entries[2].key)
	}
	jobs := entries[2]
	if jobs.added != 5 || jobs.removed != 1 {
		t.Fatalf("同文件多次改动应累加: +%d -%d", jobs.added, jobs.removed)
	}
	if len(jobs.events) != 2 || jobs.events[0].seq != 2 || jobs.events[1].seq != 3 {
		t.Fatalf("逐次改动应带 seq 保持时间序: %+v", jobs.events)
	}
	if !jobs.trunc || len(jobs.ops) != 1 || jobs.ops[0] != "edit" {
		t.Fatalf("标记/操作去重: %+v", jobs)
	}
	if !entries[1].created || !entries[0].binary {
		t.Fatalf("新建/二进制标记: %+v %+v", entries[1], entries[0])
	}
}

func TestDiffListView(t *testing.T) {
	out := diffListView(collectFileChanges(changeEvents()))
	for _, want := range []string{
		"本会话改动 3 个文件:+13 −1 行",
		"notes/new.md", "· 新建",
		"blob.bin", "· 二进制(仅计次)",
		"plugins/host/host-jobs/jobs.go", "edit×2",
		"不依赖 git",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("清单缺少 %q:\n%s", want, out)
		}
	}
	// 对齐:每个文件一行(+/− 列按最长文件名对齐,便于竖读)
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[1], "  blob.bin") || !strings.Contains(lines[1], "append×1") {
		t.Errorf("第二行应为第一个文件(blob.bin,按路径排序): %q", lines[1])
	}
	if empty := diffListView(nil); !strings.Contains(empty, "还没有捕获到文件改动") {
		t.Errorf("空清单提示: %s", empty)
	}
}

func TestFilePatchView(t *testing.T) {
	entries := collectFileChanges(changeEvents())
	jobs := entries[2]
	patch, trunc := filePatchView(jobs)
	for _, want := range []string{
		"plugins/host/host-jobs/jobs.go",
		"本次会话 2 次改动:+5 −1 行",
		"#2 edit",
		"#3 edit",
		"@@ -1,3 +1,6 @@",
		"patch 已按预算截断",
	} {
		if !strings.Contains(patch, want) {
			t.Errorf("单文件 patch 缺少 %q:\n%s", want, patch)
		}
	}
	if !trunc {
		t.Error("任一次改动截断 → 整体应标记截断")
	}
	// 二进制与「过大未生成 patch」两条降级文案
	if p, _ := filePatchView(entries[0]); !strings.Contains(p, "二进制文件,未生成逐行 diff") {
		t.Errorf("二进制降级文案:\n%s", p)
	}
	oversize := &diffEntry{key: "huge.txt", events: []changeRecord{{seq: 9, ev: sdk.FileChangeEvent{
		Op: "write", Added: 90000, Removed: 0, Coarse: true,
	}}}}
	if p, _ := filePatchView(oversize); !strings.Contains(p, "文件过大,写盘时未生成逐行 diff") {
		t.Errorf("超限降级文案:\n%s", p)
	}
}

// emitCapture 订阅 diff/open 并收集载荷。
func emitCapture(t *testing.T, c *ctx.Ctx) *[]sdk.DiffOpenEvent {
	t.Helper()
	got := &[]sdk.DiffOpenEvent{}
	c.Subscribe(sdk.EventDiffOpen, func(_ context.Context, ev *sdk.Event) error {
		switch p := ev.Payload.(type) {
		case sdk.DiffOpenEvent:
			*got = append(*got, p)
		case *sdk.DiffOpenEvent:
			*got = append(*got, *p)
		}
		return nil
	})
	return got
}

func TestCmdDiffListEmitsOpenIntent(t *testing.T) {
	c, cmds := buildEnv(t)
	sl := newStubLog()
	sl.events = changeEvents()
	if err := c.Provide("ctx.sessions", sdk.SessionLog(sl)); err != nil {
		t.Fatal(err)
	}
	got := emitCapture(t, c)
	startCmds(t, c)

	out, err := run(t, cmds, "diff")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "本会话改动 3 个文件") {
		t.Errorf("清单输出:\n%s", out)
	}
	if len(*got) != 1 || (*got)[0].Path != "" {
		t.Fatalf("清单模式应发一条无 Path 的 diff/open(Web 打开审查视图): %+v", *got)
	}
}

func TestCmdDiffSingleFile(t *testing.T) {
	c, cmds := buildEnv(t)
	sl := newStubLog()
	sl.events = changeEvents()
	if err := c.Provide("ctx.sessions", sdk.SessionLog(sl)); err != nil {
		t.Fatal(err)
	}
	got := emitCapture(t, c)
	startCmds(t, c)

	out, err := run(t, cmds, "diff", "plugins/host/host-jobs/jobs.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已打开 plugins/host/host-jobs/jobs.go 的改动(2 次,+5 −1") {
		t.Errorf("返回摘要:\n%s", out)
	}
	if len(*got) != 1 {
		t.Fatalf("应发一条 diff/open: %+v", *got)
	}
	ev := (*got)[0]
	if ev.Path != "/w/plugins/host/host-jobs/jobs.go" || ev.Changes != 2 || ev.Added != 5 || ev.Removed != 1 {
		t.Fatalf("载荷字段: %+v", ev)
	}
	if !strings.Contains(ev.Diff, "@@ -1,3 +1,6 @@") || !ev.Truncated {
		t.Fatalf("载荷应含拼好的 patch 与截断标记: %+v", ev)
	}
}

// TestCmdDiffPathMatching 路径匹配:后缀(带分隔符)与文件名两种宽松形式。
func TestCmdDiffPathMatching(t *testing.T) {
	c, cmds := buildEnv(t)
	sl := newStubLog()
	sl.events = changeEvents()
	if err := c.Provide("ctx.sessions", sdk.SessionLog(sl)); err != nil {
		t.Fatal(err)
	}
	got := emitCapture(t, c)
	startCmds(t, c)

	// 后缀(带 / 分隔符):不把 "jobs.go" 之外的相似路径吸进来
	if _, err := run(t, cmds, "diff", "host-jobs/jobs.go"); err != nil {
		t.Fatalf("后缀匹配应命中: %v", err)
	}
	// 文件名
	if _, err := run(t, cmds, "diff", "new.md"); err != nil {
		t.Fatalf("文件名匹配应命中: %v", err)
	}
	if len(*got) != 2 {
		t.Fatalf("两次调用应各发一条: %+v", *got)
	}
}

func TestCmdDiffAmbiguousAndMissing(t *testing.T) {
	c, cmds := buildEnv(t)
	sl := newStubLog()
	sl.events = append(changeEvents(), sdk.SessionEvent{Kind: sdk.EventFileChange, Seq: 6, Payload: sdk.FileChangeEvent{
		Path: "/w/other/jobs.go", Rel: "other/jobs.go", Op: "edit", Added: 1,
	}})
	if err := c.Provide("ctx.sessions", sdk.SessionLog(sl)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)

	// 同名文件 → 显式歧义(列出候选,不静默挑第一个)
	_, err := run(t, cmds, "diff", "jobs.go")
	if err == nil || !strings.Contains(err.Error(), "命中 2 个文件") {
		t.Fatalf("歧义应显式报错: %v", err)
	}
	if !strings.Contains(err.Error(), "other/jobs.go") {
		t.Errorf("歧义应列出候选: %v", err)
	}
	// 未捕获过的路径
	if _, err := run(t, cmds, "diff", "nope.go"); err == nil || !strings.Contains(err.Error(), "没有捕获到") {
		t.Fatalf("未命中应显式报错: %v", err)
	}
}

func TestCmdDiffNoSessions(t *testing.T) {
	c, cmds := buildEnv(t)
	startCmds(t, c)
	if _, err := run(t, cmds, "diff"); err == nil {
		t.Fatal("ctx.sessions 未装配应显式报错(不静默)")
	}
}
