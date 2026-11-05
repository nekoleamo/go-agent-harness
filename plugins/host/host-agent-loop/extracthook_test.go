package hostagentloop

// 回合结束投递给记忆提取器的契约(P1)。
//
// 两条会"悄悄错"的事:
//   ① 回合结束时**没投**(或投了个空 digest)—— 自动生成整条链静默不工作,谁都不会发现;
//   ② 投递把回合拖住/让回合失败 —— 一次后台任务赔掉一次对话。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// recExtractor 记录收到的 digest(测试替身)。
type recExtractor struct {
	mu   sync.Mutex
	got  []sdk.TurnDigest
	err  error
	slow time.Duration
}

func (r *recExtractor) ObserveTurn(_ context.Context, d sdk.TurnDigest) error {
	if r.slow > 0 {
		time.Sleep(r.slow)
	}
	r.mu.Lock()
	r.got = append(r.got, d)
	r.mu.Unlock()
	return r.err
}

func (r *recExtractor) digests() []sdk.TurnDigest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sdk.TurnDigest(nil), r.got...)
}

func TestObserveTurnDeliversDigest(t *testing.T) {
	e := buildEnv(t, `[{"tool":{"name":"echo","args":"{}"}},{"text":"命令已执行","finish":"stop"}]`)
	rec := &recExtractor{}
	if err := e.c.Provide("ctx.memoryExtract", sdk.MemoryExtractor(rec)); err != nil {
		t.Fatal(err)
	}
	if err := e.loop.Run(context.Background(), "以后都先跑 gate.sh 再提交"); err != nil {
		t.Fatalf("回合失败: %v", err)
	}
	got := rec.digests()
	if len(got) != 1 {
		t.Fatalf("一个回合应投递一次,got %d", len(got))
	}
	d := got[0]
	if d.UserText != "以后都先跑 gate.sh 再提交" {
		t.Fatalf("digest 应带本回合用户消息,got %q", d.UserText)
	}
	// 没装 host-cwd-sessions ⇒ 主会话没有 id ⇒ 必须回退成 "main"(不能空:来源是回收抓手)。
	if d.SessionID != "main" {
		t.Fatalf("主会话的回退 id 应为 main,got %q", d.SessionID)
	}
	if d.ReplyText == "" || !strings.Contains(d.ReplyText, "命令已执行") {
		t.Fatalf("digest 应带助手收尾文本,got %q", d.ReplyText)
	}
}

// 提取器报错不得让回合失败(后台任务不许赔掉对话)。
func TestObserveTurnErrorDoesNotFailTurn(t *testing.T) {
	e := buildEnv(t, `[{"text":"好的","finish":"stop"}]`)
	rec := &recExtractor{err: errors.New("队列满")}
	if err := e.c.Provide("ctx.memoryExtract", sdk.MemoryExtractor(rec)); err != nil {
		t.Fatal(err)
	}
	if err := e.loop.Run(context.Background(), "你好"); err != nil {
		t.Fatalf("投递失败不该让回合报错: %v", err)
	}
}

// 未装配提取器:回合照跑(否则没装 host-memory 的构建全挂)。
func TestObserveTurnWithoutExtractor(t *testing.T) {
	e := buildEnv(t, `[{"text":"好的","finish":"stop"}]`)
	if err := e.loop.Run(context.Background(), "你好"); err != nil {
		t.Fatalf("未装配提取器时回合应正常: %v", err)
	}
}

func TestLastAssistantText(t *testing.T) {
	if got := lastAssistantText(nil); got != "" {
		t.Fatalf("nil 会话应为空,got %q", got)
	}
	e := buildEnv(t, `[{"text":"这是收尾","finish":"stop"}]`)
	if err := e.loop.Run(context.Background(), "你好"); err != nil {
		t.Fatal(err)
	}
	if got := lastAssistantText(e.sessions); !strings.Contains(got, "这是收尾") {
		t.Fatalf("应取最后一条助手文本,got %q", got)
	}
}

func TestClipDigest(t *testing.T) {
	long := strings.Repeat("字", digestReplyRunes+10)
	got := clipDigest(long, digestReplyRunes)
	if n := len([]rune(got)); n != digestReplyRunes+1 { // +1 = 省略号
		t.Fatalf("应截断到 %d(+…),got %d", digestReplyRunes, n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("截断后应有省略号(让人看得出被截过)")
	}
	if short := clipDigest("短", 10); short != "短" {
		t.Fatalf("未超长应原样返回,got %q", short)
	}
}
