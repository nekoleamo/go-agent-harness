// F 组 F3 会话概述单测:输入裁剪与脱敏 / JSON 解析与重试 / 缓存命中不调模型 /
// 单飞与节流 / 自动档阈值 / 模型不可用显式降级。
package hostsummary

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubLLM 记录调用次数并可编程返回。
type stubLLM struct {
	mu     sync.Mutex
	calls  int
	reply  string
	err    error
	lastRe *sdk.LLMRequest
}

func (l *stubLLM) RegisterAdapter(sdk.LLMAdapter) sdk.Disposer { return func() {} }
func (l *stubLLM) SetModel(string)                             {}
func (l *stubLLM) Model() string                               { return "stub-model" }
func (l *stubLLM) List() []string                              { return nil }
func (l *stubLLM) SetProvider(string, string) error            { return nil }
func (l *stubLLM) UnsetProvider(string) error                  { return nil }
func (l *stubLLM) ResetProvider() error                        { return nil }
func (l *stubLLM) ProviderInfo() (string, string, bool)        { return "", "", false }
func (l *stubLLM) ListModels() ([]sdk.ModelInfo, error)        { return nil, nil }
func (l *stubLLM) Thinking() sdk.ThinkingLevel                 { return sdk.ThinkingOff }
func (l *stubLLM) SetThinking(sdk.ThinkingLevel)               {}
func (l *stubLLM) Complete(_ context.Context, req *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.lastRe = req
	if l.err != nil {
		return nil, l.err
	}
	return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant, Content: l.reply}}, nil
}

// stubCS 会话服务桩(只实现概述依赖面)。
type stubCS struct {
	infos   []sdk.SessionInfo
	current string
	sums    []sdk.SessionSummary
	names   []string
	pinned  []string
}

func (c *stubCS) Current() string                      { return "proj" }
func (c *stubCS) Path() string                         { return c.pathOf(c.current) }
func (c *stubCS) List() []string                       { return nil }
func (c *stubCS) Sessions() []sdk.SessionInfo          { return c.infos }
func (c *stubCS) Open(string) error                    { return nil }
func (c *stubCS) CurrentSession() string               { return c.current }
func (c *stubCS) Rename(string) error                  { return nil }
func (c *stubCS) Delete(string) error                  { return nil }
func (c *stubCS) UnrecordProject(string) error         { return nil }
func (c *stubCS) SessionName() string                  { return "" }
func (c *stubCS) New() (string, error)                 { return "n1", nil }
func (c *stubCS) SwitchProject(string) (string, error) { return "", nil }
func (c *stubCS) RecentProjects() []sdk.ProjectInfo    { return nil }
func (c *stubCS) SwitchDir(string) (string, error)     { return "", nil }
func (c *stubCS) SetName(id, name string) error {
	c.names = append(c.names, id+"="+name)
	return nil
}
func (c *stubCS) SetSummary(id string, sum sdk.SessionSummary) error {
	c.sums = append(c.sums, sum)
	return nil
}
func (c *stubCS) SetPinned(id string, p bool) error {
	c.pinned = append(c.pinned, id)
	return nil
}
func (c *stubCS) pathOf(id string) string {
	for _, si := range c.infos {
		if si.ID == id {
			return si.Path
		}
	}
	return ""
}

func writeSession(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// sessionJSON 生成 n 轮(user+assistant)会话文本(含工具调用名)。
func sessionJSON(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		sb.WriteString(`{"Kind":"user/message","Payload":{"Content":"用户第` + strconv.Itoa(i) + `问"},"Seq":1}` + "\n")
		sb.WriteString(`{"Kind":"assistant/message","Payload":{"Content":"助手第` + strconv.Itoa(i) + `答","ToolCalls":[{"ID":"t","Name":"file_read","Arguments":"{}"}]},"Seq":2}` + "\n")
	}
	return sb.String()
}

func newSvc(t *testing.T, ll sdk.LLMService, cs sdk.CwdSessions, data map[string]any) *Service {
	t.Helper()
	o := defaultOptions()
	applyData(&o, data)
	return &Service{ll: ll, cs: cs, o: o, lastGen: map[string]time.Time{}, lastErr: map[string]string{}}
}

func TestBuildInputTrimsAndRedacts(t *testing.T) {
	p := writeSession(t, sessionJSON(10)+`{"Kind":"user/message","Payload":{"Content":"密钥 sk-abcdef123456 与 AppSecret XX"},"Seq":9}`+"\n")
	in, users, err := buildInput(p, defaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if users != 11 {
		t.Fatalf("用户轮数应 11,得 %d", users)
	}
	if !strings.Contains(in, "中间省略") {
		t.Fatalf("应显式提示省略条数:\n%s", in)
	}
	if !strings.Contains(in, "用户第1问") || !strings.Contains(in, "助手第10答") {
		t.Fatalf("应保留首尾轮次:\n%s", in)
	}
	for _, leak := range []string{"sk-abcdef123456", "XX"} {
		if strings.Contains(in, leak) {
			t.Fatalf("密钥未脱敏(%q):\n%s", leak, in)
		}
	}
	if !strings.Contains(in, "[REDACTED]") {
		t.Fatalf("应出现脱敏标记:\n%s", in)
	}
	if !strings.Contains(in, "工具调用:file_read") {
		t.Fatalf("应保留工具名:\n%s", in)
	}
}

func TestBuildInputBudget(t *testing.T) {
	o := defaultOptions()
	o.InputBudget = 200
	p := writeSession(t, sessionJSON(5))
	in, _, err := buildInput(p, o)
	if err != nil {
		t.Fatal(err)
	}
	if len(in) > o.InputBudget+64 {
		t.Fatalf("输入应受字节预算封顶,得 %d", len(in))
	}
	if !strings.Contains(in, "按字节预算截断") {
		t.Fatal("截断应显式提示")
	}
	// 空会话
	empty := writeSession(t, "")
	if _, _, err := buildInput(empty, o); err == nil {
		t.Fatal("空会话应报错")
	}
	// 不存在
	if _, _, err := buildInput(filepath.Join(t.TempDir(), "nope.jsonl"), o); err == nil {
		t.Fatal("不存在应报错")
	}
}

func TestParseSummary(t *testing.T) {
	cases := []struct {
		in      string
		wantSum string
		wantErr bool
	}{
		{`{"title":"T","summary":"一句话","topics":["a","b"]}`, "一句话", false},
		{"```json\n{\"summary\":\"围栏内\",\"title\":\"x\"}\n```", "围栏内", false},
		{"前面解释 {\"summary\":\"夹在中间\"} 后面", "夹在中间", false},
		{`{"summary":""}`, "", true},
		{`not json`, "", true},
		{`{"title":"只有标题"}`, "", true},
	}
	for _, c := range cases {
		out, err := parseSummary(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("应报错: %q", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("不应报错(%q): %v", c.in, err)
		}
		if out.Summary != c.wantSum {
			t.Fatalf("summary = %q, want %q", out.Summary, c.wantSum)
		}
	}
	// 超长截断(标题 20 / 概述 60 / 主题 ≤3 各 ≤8)
	long := parseMust(t, `{"title":"`+strings.Repeat("标", 30)+`","summary":"`+strings.Repeat("述", 80)+`","topics":["1","2","3","4"]}`)
	if len([]rune(long.Title)) != 20 || len([]rune(long.Summary)) != 60 || len(long.Topics) != 3 {
		t.Fatalf("截断未生效: %+v", long)
	}
}

func parseMust(t *testing.T, in string) genOut {
	t.Helper()
	out, err := parseSummary(in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSummaryGeneratesAndCaches(t *testing.T) {
	p := writeSession(t, sessionJSON(4))
	cs := &stubCS{current: "", infos: []sdk.SessionInfo{{ID: "", Path: p, Frames: 8, SummaryState: "missing"}}}
	ll := &stubLLM{reply: `{"title":"会话标题","summary":"修了鉴权","topics":["鉴权"]}`}
	s := newSvc(t, ll, cs, nil)

	sum, err := s.Summary(context.Background(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Text != "修了鉴权" || sum.Model != "stub-model" || sum.CoveredFrames != 8 {
		t.Fatalf("概述异常: %+v", sum)
	}
	if len(cs.sums) != 1 {
		t.Fatalf("应回写缓存一次: %+v", cs.sums)
	}
	if len(cs.names) != 1 || cs.names[0] != "=会话标题" {
		t.Fatalf("未命名会话应用标题回填: %+v", cs.names)
	}
	// 缓存命中(ready)→ 不再调模型
	cs.infos[0].Summary, cs.infos[0].SummaryState = "修了鉴权", "ready"
	before := ll.calls
	if _, err := s.Summary(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	if ll.calls != before {
		t.Fatalf("缓存命中不应调模型: %d → %d", before, ll.calls)
	}
	// force → 重新生成
	if _, err := s.Summary(context.Background(), "", true); err != nil {
		t.Fatal(err)
	}
	if ll.calls != before+1 {
		t.Fatalf("force 应重新生成: %d", ll.calls)
	}
	// 已命名会话不回填标题
	cs.names = nil
	cs.infos[0].Name = "旧名"
	if _, err := s.Summary(context.Background(), "", true); err != nil {
		t.Fatal(err)
	}
	if len(cs.names) != 0 {
		t.Fatalf("已命名会话不应回填: %+v", cs.names)
	}
}

func TestSummaryDegradations(t *testing.T) {
	p := writeSession(t, sessionJSON(4))
	cs := &stubCS{infos: []sdk.SessionInfo{{ID: "", Path: p, Frames: 8}}}
	// 模型未装配 → 显式 unavailable
	s := newSvc(t, nil, cs, nil)
	if _, err := s.Summary(context.Background(), "", false); err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("模型缺失应显式降级: %v", err)
	}
	// 会话不存在
	s2 := newSvc(t, &stubLLM{}, cs, nil)
	if _, err := s2.Summary(context.Background(), "nope", false); err == nil {
		t.Fatal("会话不存在应报错")
	}
	// 轮次不足
	short := writeSession(t, `{"Kind":"user/message","Payload":{"Content":"只有一问"},"Seq":1}`+"\n")
	cs2 := &stubCS{infos: []sdk.SessionInfo{{ID: "", Path: short, Frames: 1}}}
	s3 := newSvc(t, &stubLLM{}, cs2, nil)
	if _, err := s3.Summary(context.Background(), "", false); err == nil || !strings.Contains(err.Error(), "轮次不足") {
		t.Fatalf("短会话应显式跳过: %v", err)
	}
	// 模型报错
	s4 := newSvc(t, &stubLLM{err: errors.New("boom")}, cs, nil)
	if _, err := s4.Summary(context.Background(), "", false); err == nil {
		t.Fatal("模型报错应向上返回")
	}
	// 输出不可解析 → 重试 1 次后报错(调用 2 次)
	ll := &stubLLM{reply: "不是 JSON"}
	s5 := newSvc(t, ll, cs, map[string]any{"max_retries": 1})
	if _, err := s5.Summary(context.Background(), "", false); err == nil {
		t.Fatal("不可解析应报错")
	}
	if ll.calls != 2 {
		t.Fatalf("应重试 1 次(共 2 次调用),得 %d", ll.calls)
	}
}

func TestSummarySingleFlight(t *testing.T) {
	p := writeSession(t, sessionJSON(4))
	cs := &stubCS{infos: []sdk.SessionInfo{{ID: "", Path: p, Frames: 8}}}
	s := newSvc(t, &stubLLM{reply: `{"summary":"x"}`}, cs, nil)
	s.mu.Lock()
	s.inflight = true
	s.mu.Unlock()
	if _, err := s.Summary(context.Background(), "", false); err == nil || !strings.Contains(err.Error(), "单飞") {
		t.Fatalf("并发应显式拒绝: %v", err)
	}
}

func TestAutoAfterTurnThrottle(t *testing.T) {
	p := writeSession(t, sessionJSON(4))
	cs := &stubCS{current: "", infos: []sdk.SessionInfo{{ID: "", Path: p, Frames: 8, SummaryState: "missing"}}}
	ll := &stubLLM{reply: `{"summary":"自动生成"}`}
	s := newSvc(t, ll, cs, nil)
	s.autoAfterTurn()
	if ll.calls != 1 {
		t.Fatalf("自动档应生成一次,得 %d", ll.calls)
	}
	// 节流:紧接着再触发 → 不再生成
	s.autoAfterTurn()
	if ll.calls != 1 {
		t.Fatalf("节流未生效: %d", ll.calls)
	}
	// 关闭自动档 → 不生成
	s.o.AutoSummary = false
	s.autoAfterTurn()
	if ll.calls != 1 {
		t.Fatalf("关闭后不应生成: %d", ll.calls)
	}
	// 短会话(轮次不足)→ 不生成
	short := writeSession(t, `{"Kind":"user/message","Payload":{"Content":"一问"},"Seq":1}`+"\n")
	cs2 := &stubCS{infos: []sdk.SessionInfo{{ID: "", Path: short, Frames: 1}}}
	ll2 := &stubLLM{reply: `{"summary":"x"}`}
	s2 := newSvc(t, ll2, cs2, nil)
	s2.autoAfterTurn()
	if ll2.calls != 0 {
		t.Fatalf("短会话不应进入自动生成: %d", ll2.calls)
	}
	// 落后帧数未达阈值 → 跳过
	cs3 := &stubCS{infos: []sdk.SessionInfo{{ID: "", Path: p, Frames: 10, Summary: "已有", SummaryCoveredFrames: 9}}}
	ll3 := &stubLLM{reply: `{"summary":"y"}`}
	s3 := newSvc(t, ll3, cs3, nil)
	s3.autoAfterTurn()
	if ll3.calls != 0 {
		t.Fatalf("缓存新鲜不应重生成: %d", ll3.calls)
	}
}

func TestApplyDataOptions(t *testing.T) {
	o := defaultOptions()
	applyData(&o, map[string]any{"auto_summary": false, "min_turns": 5, "input_budget_bytes": 4096, "min_interval_seconds": 60})
	if o.AutoSummary || o.MinTurns != 5 || o.InputBudget != 4096 || o.MinInterval != 60 {
		t.Fatalf("data 覆盖失败: %+v", o)
	}
	if o.StaleFrames != 8 {
		t.Fatalf("未覆盖维度应保持默认: %+v", o)
	}
}
