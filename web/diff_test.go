// S-P1-1:`/diff` → diff/open 事件 → SSE FrameDiff(Web 侧切变更视图)。
package web

import (
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// diff/open 事件 → FrameDiff 帧(命令意图广播;单文件时载荷带 patch 与路径)。
func TestDiffOpenBroadcastsFrame(t *testing.T) {
	hub := NewHub()
	c := newTestCtx()
	log := &memLog{}
	dis, err := hub.Subscribe(c, log)
	if err != nil {
		t.Fatal(err)
	}
	defer dis()
	ch, release := hub.Stream()
	defer release()

	// 清单意图(无 Path/Diff)
	c.fire(sdk.EventDiffOpen, sdk.DiffOpenEvent{})
	select {
	case f := <-ch:
		if f.Type != FrameDiff {
			t.Fatalf("帧类型应为 %q,得 %q", FrameDiff, f.Type)
		}
		if ev, ok := f.Payload.(sdk.DiffOpenEvent); !ok || ev.Path != "" {
			t.Fatalf("清单载荷异常: %#v", f.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 FrameDiff 帧(清单意图)")
	}

	// 单文件意图(带 patch)
	c.fire(sdk.EventDiffOpen, sdk.DiffOpenEvent{Path: "/w/a.go", Title: "a.go", Diff: "@@ -1 +1 @@\n+x\n", Added: 1, Changes: 1})
	select {
	case f := <-ch:
		ev, ok := f.Payload.(sdk.DiffOpenEvent)
		if !ok || ev.Path != "/w/a.go" || ev.Added != 1 || ev.Diff == "" {
			t.Fatalf("单文件载荷异常: %#v", f.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未收到 FrameDiff 帧(单文件)")
	}
}
