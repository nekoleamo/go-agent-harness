// NOND-N1 Web 端提示回归:notice 帧(指针/值载荷都在一条链上)、/api/notices 回填端点
// (503 未装配 / 400 坏 since / 200 带 items+max_id+gap)、以及「帧与回填同源」不变量。
package web

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// memNotices ctx.notices 替身(内存实现;List 语义与 host-notices 一致:只回 > since)。
type memNotices struct {
	items []sdk.Notice
	max   uint64
	gap   bool
}

func (m *memNotices) Publish(n sdk.Notice) uint64 { m.max++; return m.max }
func (m *memNotices) List(since uint64) sdk.NoticePage {
	page := sdk.NoticePage{MaxID: m.max, Gap: m.gap, Items: []sdk.Notice{}}
	for _, n := range m.items {
		if n.ID > since {
			page.Items = append(page.Items, n)
		}
	}
	return page
}

func noticesTestServer(ns sdk.NoticeService) (*Server, *httptest.Server) {
	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), slog.Default())
	s.notices = ns
	return s, httptest.NewServer(s.handler())
}

// TestHubNoticeFrame notice 帧:载荷为 *sdk.Notice / sdk.Notice 两种形态都转发。
func TestHubNoticeFrame(t *testing.T) {
	ctx := newTestCtx()
	hub := NewHub()
	unsub, err := hub.Subscribe(ctx, &memLog{})
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	ch, release := hub.Stream()
	defer release()

	ts := time.Date(2026, 9, 18, 10, 30, 0, 0, time.UTC)
	n := sdk.Notice{ID: 7, Level: sdk.NoticeWarn, Title: "计划「对账」本轮跳过", Body: "有回合在跑", Source: "host-schedule", TS: ts}
	ctx.fire(sdk.EventNotice, &n)
	f := <-ch
	if f.Type != FrameNotice {
		t.Fatalf("期望提示帧,得 %+v", f)
	}
	got, ok := f.Payload.(*sdk.Notice)
	if !ok || got.ID != 7 || got.Level != sdk.NoticeWarn || got.Source != "host-schedule" {
		t.Fatalf("提示帧载荷不符: %+v", f.Payload)
	}
	// 值载荷同样转发(事件派发两种姿势都合法;漏一种 = 一半发布点静默丢失)
	ctx.fire(sdk.EventNotice, n)
	if f2 := <-ch; f2.Type != FrameNotice {
		t.Fatalf("值载荷未转发: %+v", f2)
	}
}

// TestNoticeFramePayloadKeys 帧内 JSON 键名 = 前端类型字段(跨端契约;改名即断)。
func TestNoticeFramePayloadKeys(t *testing.T) {
	raw, err := json.Marshal(&sdk.Notice{ID: 1, Level: sdk.NoticeError, Title: "t", Body: "b", Source: "s",
		TS: time.Unix(0, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"id":1`, `"level":"error"`, `"title":"t"`, `"body":"b"`, `"source":"s"`, `"ts":`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("JSON 缺 %s: %s", key, raw)
		}
	}
}

// TestNoticesUnavailableReturns503 未装配 → 503 显式错误(不静默回空列表:空列表会被前端
// 读成「没有提示」,而事实是「这条通道没装」)。
func TestNoticesUnavailableReturns503(t *testing.T) {
	_, hs := noticesTestServer(nil)
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/notices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("期望 503,得 %d", resp.StatusCode)
	}
}

// TestNoticesBadSinceReturns400 since 非法 → 400(不吞成 0:游标错会重复弹已读提示)。
func TestNoticesBadSinceReturns400(t *testing.T) {
	_, hs := noticesTestServer(&memNotices{})
	defer hs.Close()
	for _, q := range []string{"since=abc", "since=-1", "since=1.5"} {
		resp, err := http.Get(hs.URL + "/api/notices?" + q)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s 期望 400,得 %d", q, resp.StatusCode)
		}
	}
}

// TestNoticesListSince 回填语义:since 过滤 + max_id/gap 如实透传;空缓冲回空数组(非 null)。
func TestNoticesListSince(t *testing.T) {
	ns := &memNotices{gap: true, max: 3, items: []sdk.Notice{
		{ID: 1, Level: sdk.NoticeInfo, Title: "一"},
		{ID: 3, Level: sdk.NoticeError, Title: "三"},
	}}
	_, hs := noticesTestServer(ns)
	defer hs.Close()

	read := func(url string) sdk.NoticePage {
		t.Helper()
		resp, err := http.Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s 期望 200,得 %d", url, resp.StatusCode)
		}
		b, _ := io.ReadAll(resp.Body)
		var page sdk.NoticePage
		if err := json.Unmarshal(b, &page); err != nil {
			t.Fatalf("响应非 NoticePage: %s", b)
		}
		if !strings.Contains(string(b), `"items"`) {
			t.Fatalf("items 字段应恒在: %s", b)
		}
		return page
	}

	all := read(hs.URL + "/api/notices?since=0")
	if len(all.Items) != 2 || all.MaxID != 3 || !all.Gap {
		t.Fatalf("since=0 应回全量并透传 gap: %+v", all)
	}
	tail := read(hs.URL + "/api/notices?since=1")
	if len(tail.Items) != 1 || tail.Items[0].ID != 3 {
		t.Fatalf("since=1 应只回 3: %+v", tail)
	}
	empty := read(hs.URL + "/api/notices?since=3")
	if empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("无新提示应回空数组(不是 null): %+v", empty)
	}
	// 无参 = since 0(与显式 0 等价)
	noArg := read(hs.URL + "/api/notices")
	if len(noArg.Items) != 2 {
		t.Fatalf("无参应等价 since=0: %+v", noArg)
	}
}
