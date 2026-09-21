// A-1 条目 86(壳内通知端到端)的可自动化半边:**宿主真产物 → 壳解析**。
//
// 桌面壳(desktop/src-tauri)靠 GET /api/notices 的正文做「该不该弹系统通知」的判定。
// 此前 Rust 侧用的是**手抄**样例(REAL_SAMPLE),与宿主实际输出之间没有回归约束:
// 宿主改字段名/改形状,Rust 不会红。这里把真产物固化成**可复现 fixture**:
//   - 事件从**真总线**触发(job/done、schedule/run、agent/error)→ host-notices 自动生产 → 真 HTTP 端点取正文;
//   - ts 置零(时钟不可复现),其余字段原样 —— 壳的判定链不读 ts;
//   - `GAH_UPDATE_NOTICE_FIXTURE=1 go test ./tests/ -run TestNoticeFeedFixture` 重新生成。
package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

const noticeFixturePath = "../desktop/src-tauri/fixtures/notices-feed.json"

func TestNoticeFeedFixtureForDesktopShell(t *testing.T) {
	c, srv := buildNoticeEnv(t, true)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	// 真事件 → 自动生产(不经手工 Publish):覆盖 失败/跳过/回合作废 三种「需要人回来的时刻」
	bus := func(name string, payload any) { c.Emit(context.Background(), name, payload, sdk.Emit) }
	bus(sdk.EventJobDone, &sdk.JobDoneEvent{ID: "job-1", State: sdk.JobFailed})
	bus(sdk.EventScheduleRun, &sdk.ScheduleRunEvent{ID: "plan-1", State: sdk.ScheduleRunSkipped})
	bus(sdk.EventAgentError, context.DeadlineExceeded)

	resp, err := http.Get(hs.URL + "/api/notices?since=0")
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
	if len(page.Items) == 0 {
		t.Fatal("真事件应自动生产出提示(空 = 生产链断了)")
	}
	for i := range page.Items {
		page.Items[i].TS = time.Time{} // 归零:ts 不进壳判定(时钟不可复现)
	}
	norm, err := json.MarshalIndent(page, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	norm = append(norm, '\n')

	if os.Getenv("GAH_UPDATE_NOTICE_FIXTURE") == "1" {
		if err := os.MkdirAll(filepath.Dir(noticeFixturePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(noticeFixturePath, norm, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("已重写 fixture(%d 条)", len(page.Items))
		return
	}
	want, err := os.ReadFile(noticeFixturePath)
	if err != nil {
		t.Fatalf("fixture 缺失(%v);用 GAH_UPDATE_NOTICE_FIXTURE=1 生成", err)
	}
	if string(norm) != string(want) {
		t.Errorf("宿主真产物与桌面壳 fixture 不一致(宿主改了形状/字段,壳侧必须同步):\n实际:\n%s\n期望:\n%s", norm, want)
	}
	t.Logf("%s", norm)
}
