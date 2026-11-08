package hostmemory

// 失败触发(重复失败 → pitfall 候选)的宿主侧契约。
//
// 钉三件"会静默错"的事:
//   ① 同一错误重复到阈值才提**候选**(不直写;pitfall 不进 AutoWriteKinds);
//   ② 阈值翻倍:重复 100 次也只提有限条,不会把候选池灌满;
//   ③ 开关与阈值是配置:off 立刻不订阅;阈值 1 显式拒绝(那等于每次失败都提一条)。

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func emitToolFail(t *testing.T, c sdk.Ctx, tool, errText string) {
	t.Helper()
	tctx := sdk.WithToolName(context.Background(), tool)
	tctx = sdk.WithSessionContext(tctx, "sess-fail")
	if _, err := c.Emit(tctx, "tools/post-execute", &sdk.ToolResult{Error: errText}, sdk.Waterfall); err != nil {
		t.Fatalf("emit 失败: %v", err)
	}
}

func candidatesText(t *testing.T, svc *Service) string {
	t.Helper()
	return strings.Join(svc.ListCandidates(), "\n")
}

func TestFailureTriggerProposesCandidate(t *testing.T) {
	c, svc := startServiceWith(t, map[string]any{"fail_trigger": "on", "fail_threshold": 2})
	emitToolFail(t, c, "bash", "docker compose: no such service api")
	if got := candidatesText(t, svc); strings.Contains(got, "重复失败") {
		t.Fatalf("第一次失败不该提候选: %s", got)
	}
	emitToolFail(t, c, "bash", "docker compose: no such service api")
	got := candidatesText(t, svc)
	if !strings.Contains(got, "重复失败") || !strings.Contains(got, "bash") {
		t.Fatalf("第二次失败应提一条候选: %s", got)
	}
	// 候选**不进记忆**(人工转正才生效)。
	if strings.Contains(strings.Join(svc.List(), "\n"), "重复失败") {
		t.Fatal("候选不该直接进记忆(pitfall 永远走候选)")
	}
	// 阈值翻倍:第 3 次不提,第 4 次才提第二条(2 → 4 → 8)。
	emitToolFail(t, c, "bash", "docker compose: no such service api")
	if n := candidateLines(t, svc); n != 1 {
		t.Fatalf("第 3 次不该再提(下一次阈值是 4), got %d", n)
	}
	emitToolFail(t, c, "bash", "docker compose: no such service api")
	if n := candidateLines(t, svc); n != 2 {
		t.Fatalf("第 4 次应提第二条(阈值翻到 4), got %d", n)
	}
	emitToolFail(t, c, "bash", "docker compose: no such service api")
	emitToolFail(t, c, "bash", "docker compose: no such service api")
	emitToolFail(t, c, "bash", "docker compose: no such service api")
	if n := candidateLines(t, svc); n != 2 {
		t.Fatalf("第 5~7 次不该再提(下一次阈值是 8), got %d", n)
	}
}

// candidateLines 候选池条数(与 ListCandidates 同一份口径)。
func candidateLines(t *testing.T, svc *Service) int {
	t.Helper()
	raw := strings.TrimSpace(candidatesText(t, svc))
	if raw == "" {
		return 0
	}
	return len(strings.Split(raw, "\n"))
}

func TestFailureTriggerOffAndBadConfig(t *testing.T) {
	// off ⇒ 不订阅,失败不产生任何候选
	c, svc := startServiceWith(t, map[string]any{"fail_trigger": "off"})
	for i := 0; i < 4; i++ {
		emitToolFail(t, c, "bash", "boom")
	}
	if got := candidatesText(t, svc); got != "" {
		t.Fatalf("关闭后不该提候选: %s", got)
	}
}

func TestFailureTriggerRejectsBadConfig(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	if _, err := newFailureWatch(nil, nil, &sdk.Manifest{Data: map[string]any{"fail_trigger": "sometimes"}}); err == nil {
		t.Fatal("未知模式必须显式失败(不静默降级)")
	}
	if _, err := newFailureWatch(nil, nil, &sdk.Manifest{Data: map[string]any{"fail_threshold": 1}}); err == nil {
		t.Fatal("阈值 1 必须拒绝(否则每次失败都提候选,池子会被灌满)")
	}
	fw, err := newFailureWatch(nil, nil, &sdk.Manifest{Data: map[string]any{"fail_trigger": "off"}})
	if err != nil || fw != nil {
		t.Fatalf("off 应返回 nil 观察者且不报错: %v %v", fw, err)
	}
}

// 不同错误不该合并:换了错误文本 = 另一件事,各自从 1 开始数。
func TestFailureTriggerSeparatesDifferentErrors(t *testing.T) {
	c, svc := startServiceWith(t, map[string]any{"fail_threshold": 2})
	emitToolFail(t, c, "bash", "permission denied for file alpha")
	emitToolFail(t, c, "bash", "no space left on device beta")
	if got := candidatesText(t, svc); strings.Contains(got, "重复失败") {
		t.Fatalf("两个不同的错误各出现一次,不该触发: %s", got)
	}
}

// 账本会节流落盘(每 failFlushEvery 次):攒够了就必须在盘上,不能只活在内存里。
func TestFailureLedgerFlushesAfterThreshold(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, svc := startServiceWith(t, map[string]any{})
	_ = svc
	for i := 0; i < failFlushEvery; i++ {
		emitToolFail(t, c, "bash", "flaky network "+alpha(i))
	}
	st, err := memory.LoadFailures()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Items) == 0 {
		t.Fatalf("%d 次失败后账本应已落盘", failFlushEvery)
	}
}

// alpha 互不相同的短串(数字会被签名归一化掉,不能用来区分)。
func alpha(n int) string { return string(rune('a' + n%26)) }
