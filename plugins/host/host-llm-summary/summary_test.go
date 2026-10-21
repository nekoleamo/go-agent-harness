// host-llm-summary 单测:输入裁剪与脱敏 / JSON 解析与重试 / 结构化渲染 / 失败即报错
// (调用方据此保持抽取式摘要 = 确定性回退)。
package hostllmsummary

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeLLM 最小 LLMService 替身(只实现被用到的方法)。
type fakeLLM struct {
	sdk.LLMService
	content string
	err     error
	calls   int
	last    *sdk.LLMRequest
}

func (f *fakeLLM) Complete(_ context.Context, req *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	f.calls++
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant, Content: f.content}}, nil
}

func (f *fakeLLM) Model() string { return "fake-model" }

const goodJSON = `{"task":"改登录","changes":["auth.go"],"decisions":["用 session"],"open":["补测试"],"errors":[""]}`

func TestSummarizeProducesStructuredText(t *testing.T) {
	ll := &fakeLLM{content: goodJSON}
	s := &Service{ll: ll, o: defaultOptions()}
	out, err := s.Summarize(context.Background(), sdk.SummaryRequest{
		Prev: "旧摘要", Seg: "用户: 改一下登录", MaxChars: 500})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	for _, want := range []string{"任务: 改登录", "改动: auth.go", "决定: 用 session", "未完成: 补测试"} {
		if !strings.Contains(out, want) {
			t.Fatalf("摘要应含 %q,实际:\n%s", want, out)
		}
	}
	if strings.Contains(out, "错误:") {
		t.Fatalf("空项不应出现:\n%s", out)
	}
}

func TestSummarizeRetriesOnBadJSON(t *testing.T) {
	ll := &fakeLLM{content: "这不是 JSON"}
	s := &Service{ll: ll, o: defaultOptions()}
	if _, err := s.Summarize(context.Background(), sdk.SummaryRequest{Seg: "x"}); err == nil {
		t.Fatal("解析失败应报错(调用方据此保持抽取式)")
	}
	if ll.calls != defaultOptions().MaxRetries+1 {
		t.Fatalf("应重试 %d 次(共 %d 次调用),实际 %d",
			defaultOptions().MaxRetries, defaultOptions().MaxRetries+1, ll.calls)
	}
}

func TestSummarizeFailsWhenModelUnavailable(t *testing.T) {
	s := &Service{o: defaultOptions()} // 没有模型
	if _, err := s.Summarize(context.Background(), sdk.SummaryRequest{Seg: "x"}); err == nil {
		t.Fatal("模型不可用应报错(而不是静默返回空摘要)")
	}
}

func TestSummarizePropagatesModelError(t *testing.T) {
	ll := &fakeLLM{err: errors.New("端点 500")}
	s := &Service{ll: ll, o: defaultOptions()}
	if _, err := s.Summarize(context.Background(), sdk.SummaryRequest{Seg: "x"}); err == nil {
		t.Fatal("模型报错应如实返回")
	}
}

func TestSummarizeRedactsInput(t *testing.T) {
	ll := &fakeLLM{content: goodJSON}
	s := &Service{ll: ll, o: defaultOptions()}
	_, err := s.Summarize(context.Background(), sdk.SummaryRequest{
		Seg: "我的 api_key=sk-abcdef123456 请别泄露"})
	if err != nil {
		t.Fatal(err)
	}
	user := ll.last.Messages[len(ll.last.Messages)-1].Content
	if strings.Contains(user, "sk-abcdef123456") {
		t.Fatalf("密钥不应进模型上下文:\n%s", user)
	}
	if !strings.Contains(user, "[REDACTED]") {
		t.Fatalf("应有脱敏标记:\n%s", user)
	}
}

func TestSummarizeRespectsMaxChars(t *testing.T) {
	ll := &fakeLLM{content: goodJSON}
	s := &Service{ll: ll, o: defaultOptions()}
	out, err := s.Summarize(context.Background(), sdk.SummaryRequest{Seg: "x", MaxChars: 12})
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(out)); n > 12 {
		t.Fatalf("摘要应限长 12,实际 %d: %q", n, out)
	}
}

func TestBuildPromptTrimsToBudget(t *testing.T) {
	long := strings.Repeat("啊", 10000)
	p := buildPrompt(sdk.SummaryRequest{Prev: long, Seg: long}, 1000)
	if len(p) > 1200 {
		t.Fatalf("提示应受预算约束: %d 字节", len(p))
	}
	if !strings.Contains(p, "[前文省略]") {
		t.Fatalf("省略处应注明:\n%s", p[:200])
	}
}

func TestRenderSkipsEmptySections(t *testing.T) {
	got := render(out{Task: "T", Changes: []string{"", "  "}, Open: []string{"x"}})
	if strings.Contains(got, "改动") {
		t.Fatalf("空改动项不应出现:\n%s", got)
	}
	if !strings.Contains(got, "未完成: x") {
		t.Fatalf("应有未完成项:\n%s", got)
	}
}
