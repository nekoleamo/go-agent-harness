// NOND-N1 提示通道端到端(真实装配):host-notices 参与 base 装配 →
//
//	① 插件侧 ctx.notices.Publish 立即经 SSE 送达浏览器(notice 帧,载荷与 REST 回填同源);
//	② GET /api/notices?since= 能回填「离开期间错过」的提示(帧丢了也能补);
//	③ 自动生产:总线上的 job/done / schedule/run / agent/error 无需各端轮询即变成提示;
//	④ 未装配 host-notices 时端点显式 503(不静默回空 = 不把「没装」说成「没有」)。
package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
	"github.com/nekoleamo/go-agent-harness/web"
)

// buildNoticeEnv 装配带 host-notices 的最小 base + web 服务窗口。
// withNotices=false 时改用不含 host-notices 的树(验未装配路径的显式失败)。
func buildNoticeEnv(t *testing.T, withNotices bool) (sdk.Ctx, *web.Server) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	entries := []config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-commands"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock"},
		{ID: "tool-shell"},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "workspace-write", "sync": true}},
		{ID: "host-agent-loop"},
	}
	if withNotices {
		entries = append(entries, config.Entry{ID: "host-notices"})
	}
	tree.Apply(entries)
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })

	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	hub := web.NewHub()
	srv := web.New(web.Config{}, hub, web.NewConfirm(hub), logger)
	if err := srv.Inject(c); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Subscribe(c, sessions); err != nil {
		t.Fatal(err)
	}
	return c, srv
}

// noticeSvcOf 取 ctx.notices(装配缺失即失败,不静默跳过)。
func noticeSvcOf(t *testing.T, c sdk.Ctx) sdk.NoticeService {
	t.Helper()
	var ns sdk.NoticeService
	if err := c.Inject("ctx.notices", &ns); err != nil {
		t.Fatalf("ctx.notices 应由 host-notices 提供: %v", err)
	}
	return ns
}

// fetchNotices 读回填端点(真实 HTTP)。
func fetchNotices(t *testing.T, base string, since uint64) sdk.NoticePage {
	t.Helper()
	resp, err := http.Get(base + "/api/notices?since=" + itoa(since))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("回填应 200,得 %d", resp.StatusCode)
	}
	var page sdk.NoticePage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("响应非 NoticePage: %v", err)
	}
	return page
}

func itoa(u uint64) string {
	if u == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}

// readNoticeFrame 从 SSE 流里等一条 notice 帧(载荷解码为 sdk.Notice);超时即失败。
func readNoticeFrame(t *testing.T, r *bufio.Scanner, timeout time.Duration) sdk.Notice {
	t.Helper()
	deadline := time.Now().Add(timeout)
	payload := ""
	isNotice := false
	for time.Now().Before(deadline) {
		if !r.Scan() {
			t.Fatalf("SSE 流结束,未见 notice 帧(已读到 %q)", payload)
		}
		line := r.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			isNotice = strings.TrimSpace(strings.TrimPrefix(line, "event: ")) == web.FrameNotice
		case isNotice && strings.HasPrefix(line, "data: "):
			// 线上是整帧({id,type,ts,payload})——载荷在 payload 字段里(前端 transport 同款解析)。
			var f struct {
				Type    string          `json:"type"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f); err != nil {
				t.Fatalf("notice 帧无法解码: %v", err)
			}
			if f.Type != web.FrameNotice {
				t.Fatalf("帧内 type 与外层 event 不一致: %q", f.Type)
			}
			var n sdk.Notice
			if err := json.Unmarshal(f.Payload, &n); err != nil {
				t.Fatalf("notice 载荷无法解码: %v(%s)", err, f.Payload)
			}
			return n
		}
	}
	t.Fatalf("超时(%s)未见 notice 帧", timeout)
	return sdk.Notice{}
}

// TestNoticeEndToEndPublishFrameAndBackfill 发布 → SSE 帧 + REST 回填同源(同 id 同载荷)。
func TestNoticeEndToEndPublishFrameAndBackfill(t *testing.T) {
	c, srv := buildNoticeEnv(t, true)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	evResp, err := http.Get(hs.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	defer evResp.Body.Close()
	sc := bufio.NewScanner(evResp.Body)
	sc.Buffer(make([]byte, 4096), 1<<20)

	ns := noticeSvcOf(t, c)
	id := ns.Publish(sdk.Notice{Level: sdk.NoticeError, Title: "计划「对账」执行失败",
		Body: "模型超时\n细节在会话记录", Source: "host-schedule"})
	if id == 0 {
		t.Fatal("Publish 应返回分配的 id")
	}

	n := readNoticeFrame(t, sc, 10*time.Second)
	if n.ID != id || n.Level != sdk.NoticeError || !strings.Contains(n.Title, "对账") {
		t.Fatalf("帧载荷与发布内容不符: %+v(id=%d)", n, id)
	}
	if n.TS.IsZero() {
		t.Fatal("帧载荷应带时间戳(实现补齐)")
	}

	// REST 回填同源:同一 id / 同一标题(帧丢了也能补上,提示不进会话记录是刻意的)
	page := fetchNotices(t, hs.URL, 0)
	if len(page.Items) != 1 || page.Items[0].ID != id || page.Items[0].Title != n.Title || page.MaxID != id {
		t.Fatalf("回填与帧不同源: %+v vs %+v", page, n)
	}
	if len(fetchNotices(t, hs.URL, id).Items) != 0 {
		t.Fatal("since=已读 id 应回空(客户端按游标续传,不重复弹)")
	}
}

// TestNoticeAutoProduction 自动生产:job/done、schedule/run(failed/skipped)、agent/error
// 由 host-notices 集中转成提示 —— 无人值守场景不必各端自建轮询器。
func TestNoticeAutoProduction(t *testing.T) {
	c, srv := buildNoticeEnv(t, true)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	ns := noticeSvcOf(t, c)

	bus := func(name string, payload any) {
		t.Helper()
		c.Emit(context.Background(), name, payload, sdk.Emit)
	}
	bus(sdk.EventJobDone, &sdk.JobDoneEvent{ID: "job-1", State: sdk.JobFailed})
	bus(sdk.EventScheduleRun, &sdk.ScheduleRunEvent{ID: "plan-1", State: sdk.ScheduleRunSkipped})
	bus(sdk.EventScheduleRun, &sdk.ScheduleRunEvent{ID: "plan-1", State: sdk.ScheduleRunOK}) // ok 不提示
	bus(sdk.EventAgentError, context.DeadlineExceeded)

	page := fetchNotices(t, hs.URL, 0)
	if len(page.Items) != 3 {
		t.Fatalf("应生产 3 条(任务失败/计划跳过/回合报错),ok 不发: %+v", page.Items)
	}
	bySource := map[string]sdk.Notice{}
	for _, n := range page.Items {
		bySource[n.Source] = n
		if n.TS.IsZero() || n.Title == "" {
			t.Fatalf("自动生产的提示也必须补齐标题/时间: %+v", n)
		}
	}
	if n := bySource["host-jobs"]; n.Level != sdk.NoticeError || !strings.Contains(n.Body, "job-1") {
		t.Fatalf("任务失败应给 error 且带任务 id: %+v", n)
	}
	if n := bySource["host-schedule"]; n.Level != sdk.NoticeWarn || !strings.Contains(n.Title, "跳过") {
		t.Fatalf("计划跳过应给 warn: %+v", n)
	}
	if n := bySource["host-agent-loop"]; n.Level != sdk.NoticeError || !strings.Contains(n.Body, "deadline") {
		t.Fatalf("回合报错应给 error 且带原因: %+v", n)
	}
	// 环形缓冲容量内的回填是完整的(未发生丢弃 → 不标 gap)
	if page.Gap {
		t.Fatal("未发生丢弃时不得标 gap(不谎报不完整)")
	}
	if ns.List(0).MaxID != 3 {
		t.Fatalf("MaxID 应等于已发布条数: %d", ns.List(0).MaxID)
	}
}

// TestNoticesEndpointUnavailableWithoutPlugin 未装配 host-notices → 503(显式失败,
// 不把「通道没装」静默说成「没有提示」)。
func TestNoticesEndpointUnavailableWithoutPlugin(t *testing.T) {
	_, srv := buildNoticeEnv(t, false)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/notices?since=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", resp.StatusCode)
	}
}
