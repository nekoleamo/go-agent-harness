// schedule.go 单测:域模型零逻辑(结构体 + 两个 ctx 存取函数),只钉存取语义与 nil 安全。
package sdk

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUnattendedRoundTrip(t *testing.T) {
	if UnattendedOf(context.Background()) {
		t.Fatal("未标记的 ctx 必须返回 false(交互回合默认)")
	}
	// nil ctx 必须安全返回 false(用变量而非字面 nil:staticcheck SA1012 只放行变量形式)
	var nilCtx context.Context
	if UnattendedOf(nilCtx) {
		t.Fatal("nil ctx 必须安全返回 false")
	}
	ctx := WithUnattended(context.Background())
	if !UnattendedOf(ctx) {
		t.Fatal("标记后必须返回 true")
	}
	// 派生 ctx 继承标记(agent-loop 内部会派生可取消 ctx,标记必须跟着走)
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !UnattendedOf(dctx) {
		t.Fatal("派生 ctx 必须继承无人值守标记")
	}
	// 反向:标记不得污染父 ctx
	if UnattendedOf(context.Background()) {
		t.Fatal("标记不得泄漏到其它 ctx")
	}
}

// TestScheduleJSONShape 钉住 Web 契约:Prompt/Enabled 等前端依赖字段必须在 JSON 里。
func TestScheduleJSONShape(t *testing.T) {
	b, err := json.Marshal(Schedule{ID: "s1", Name: "对账", Cron: "0 8 * * *", Prompt: "跑对账", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"id":"s1"`, `"cron":"0 8 * * *"`, `"prompt":"跑对账"`, `"enabled":true`} {
		if !strings.Contains(s, want) {
			t.Fatalf("JSON 缺 %s: %s", want, s)
		}
	}
	// time.Time 不受 omitempty 影响(结构体):未排期时该字段为**零值**而非缺省。
	// 前端据此判「无下次触发」,故这里钉住该行为(改类型成 *time.Time 时必须同步前端)。
	if !strings.Contains(s, `"next_run":"0001-01-01T00:00:00Z"`) {
		t.Fatalf("零值 NextRun 形态变了,前端按零值判断需同步: %s", s)
	}
}

// TestScheduleRunStateValues 钉住状态字符串(Web/TUI/落盘共用,改名即破坏老数据)。
func TestScheduleRunStateValues(t *testing.T) {
	for want, got := range map[string]ScheduleRunState{"ok": ScheduleRunOK, "failed": ScheduleRunFailed, "skipped": ScheduleRunSkipped} {
		if string(got) != want {
			t.Fatalf("状态常量漂移: %q != %q", got, want)
		}
	}
}
