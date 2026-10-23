// 工具层测试:配置解析、权限选项选择、参数校验,以及一次**真子进程**的 happy path。
package acpclient

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestParseConfigGood 正常配置:runner 名/命令/参数/超时都对上。
func TestParseConfigGood(t *testing.T) {
	rs, timeout, err := parseConfig(&sdk.Manifest{Data: map[string]any{
		"timeout_seconds": 30,
		"runners": map[string]any{
			"opencode": map[string]any{"command": "opencode", "args": []any{"acp", "--flag"}},
			"codex":    map[string]any{"command": "npx", "args": []string{"-y", "codex-acp"}},
			"bare":     map[string]any{"command": "agent"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if timeout.Seconds() != 30 {
		t.Fatalf("timeout = %v, want 30s", timeout)
	}
	if len(rs) != 3 {
		t.Fatalf("runners = %d, want 3", len(rs))
	}
	if got := strings.Join(rs["opencode"].args, " "); got != "acp --flag" {
		t.Fatalf("opencode.args = %q", got)
	}
	if got := strings.Join(rs["codex"].args, " "); got != "-y codex-acp" {
		t.Fatalf("codex.args = %q", got)
	}
	if len(rs["bare"].args) != 0 {
		t.Fatalf("不带 args 的 runner 应为空参数, got %v", rs["bare"].args)
	}
}

// TestParseConfigBad 配置写错必须**显式报错**(它静默失效的表现是"工具不存在",
// 而用户会去查 PATH —— 查一天也查不出来)。
func TestParseConfigBad(t *testing.T) {
	cases := []struct {
		name string
		data map[string]any
	}{
		{"runners 非映射", map[string]any{"runners": []any{"opencode"}}},
		{"runner 非对象", map[string]any{"runners": map[string]any{"a": "opencode"}}},
		{"缺 command", map[string]any{"runners": map[string]any{"a": map[string]any{"args": []any{"x"}}}}},
		{"args 含非字符串", map[string]any{"runners": map[string]any{"a": map[string]any{"command": "x", "args": []any{1}}}}},
		{"timeout 非整数", map[string]any{"timeout_seconds": "600"}},
		{"timeout 非正", map[string]any{"timeout_seconds": 0}},
	}
	for _, tc := range cases {
		if _, _, err := parseConfig(&sdk.Manifest{Data: tc.data}); err == nil {
			t.Errorf("%s: 应报错", tc.name)
		}
	}
}

// TestParseConfigEmpty 没配 runners ⇒ 空表(装配侧据此不注册工具)。
func TestParseConfigEmpty(t *testing.T) {
	rs, timeout, err := parseConfig(&sdk.Manifest{})
	if err != nil || len(rs) != 0 || timeout != defaultTimeout {
		t.Fatalf("空配置应为空表 + 默认超时, got %v %v %v", rs, timeout, err)
	}
}

// TestChooseOptionPicksNarrowest 允许取 once 优先,拒绝取 reject_once 优先。
//
// 为什么这条最要紧:"allow_always" 往往排在第一个,直接取第一个 = 用户点一次
// 就永久放行。审批最常见的坑就是这个。
func TestChooseOptionPicksNarrowest(t *testing.T) {
	opts := []permOption{
		{OptionID: "always", Name: "永久允许", Kind: "allow_always"},
		{OptionID: "once", Name: "仅本次", Kind: "allow_once"},
		{OptionID: "r-always", Name: "永久拒绝", Kind: "reject_always"},
		{OptionID: "r-once", Name: "仅本次拒绝", Kind: "reject_once"},
	}
	if id, ok := chooseOption(opts, true); !ok || id != "once" {
		t.Fatalf("允许应取 allow_once, got %q %v", id, ok)
	}
	if id, ok := chooseOption(opts, false); !ok || id != "r-once" {
		t.Fatalf("拒绝应取 reject_once, got %q %v", id, ok)
	}
	// 只有 always 可用时也照样能用(否则就变成"点了批准但发不出去")。
	if id, ok := chooseOption([]permOption{{OptionID: "always", Kind: "allow_always"}}, true); !ok || id != "always" {
		t.Fatalf("只有 allow_always 时应选它, got %q %v", id, ok)
	}
	// 没有 allow 类选项 ⇒ 不选(上层回 cancelled)。
	if _, ok := chooseOption([]permOption{{OptionID: "r", Kind: "reject_once"}}, true); ok {
		t.Fatal("没有 allow 选项时不能瞎选一个")
	}
}

// TestDescribePermissionSaysWhatAndHowMuch 弹窗必须让人能判断"该不该给":
// 说清是谁的什么操作,并列出可选项。
func TestDescribePermissionSaysWhatAndHowMuch(t *testing.T) {
	p := permParams{Options: []permOption{{Name: "仅本次"}, {Name: "永久允许"}}}
	p.ToolCall.Title = "写入 internal/x.go"
	p.ToolCall.Kind = "edit"
	got := describePermission(p)
	for _, want := range []string{"写入 internal/x.go", "edit", "仅本次", "永久允许", "拒绝"} {
		if !strings.Contains(got, want) {
			t.Errorf("权限弹窗应含 %q: %s", want, got)
		}
	}
}

// TestDefinitionDeclaresCWDAsWritePath cwd 是写意图路径参数:外部 agent 会在这个目录里
// 落盘,不声明就会被路径沙箱当成"没有路径参数"放过(一条绕开只读档的侧门)。
func TestDefinitionDeclaresCWDAsWritePath(t *testing.T) {
	d := (&Tool{runners: map[string]runner{"opencode": {command: "x"}}, timeout: defaultTimeout}).Definition()
	if d.Name != "acp_run" {
		t.Fatalf("工具名 = %q", d.Name)
	}
	if len(d.PathParams) != 1 || d.PathParams[0].Arg != "cwd" || d.PathParams[0].Access != sdk.PathWrite {
		t.Fatalf("cwd 必须声明为写路径参数: %+v", d.PathParams)
	}
	if !strings.Contains(d.Description, "opencode") {
		t.Error("工具描述应把已配置的 runner 列给模型(否则它只能猜名字)")
	}
}

// TestExecuteRejectsUnknownRunner 未配置的 runner:显式失败并列出可用的。
func TestExecuteRejectsUnknownRunner(t *testing.T) {
	tool := &Tool{runners: map[string]runner{"opencode": {command: "opencode"}}, timeout: defaultTimeout}
	_, err := tool.Execute(context.Background(), `{"runner":"cursor","message":"x"}`)
	if err == nil {
		t.Fatal("未配置的 runner 应报错")
	}
	if !strings.Contains(err.Error(), "opencode") {
		t.Fatalf("错误里应列出已配置的 runner: %v", err)
	}
}

// TestExecuteRejectsBadArgs 空 message / 不存在的 cwd 都在**起进程之前**拦住。
func TestExecuteRejectsBadArgs(t *testing.T) {
	tool := &Tool{runners: map[string]runner{"fake": {command: "definitely-not-a-real-binary-xyz"}}, timeout: defaultTimeout}
	if _, err := tool.Execute(context.Background(), `{"runner":"fake","message":"  "}`); err == nil {
		t.Fatal("空 message 应报错")
	}
	if _, err := tool.Execute(context.Background(), `{"runner":"fake","message":"x","cwd":"/no/such/dir/xyz"}`); err == nil {
		t.Fatal("不存在的 cwd 应报错")
	}
	if _, err := tool.Execute(context.Background(), `not json`); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

// TestExecuteEndToEnd 真子进程跑一次完整协议:initialize → session/new → session/prompt,
// 收一条 agent_message_chunk 增量,拿回 stopReason。
//
// 用 shell 脚本当假 agent(不依赖任何真实编码 agent 是否装在本机)。
// Windows 跳过:那里没有保证存在的 POSIX shell(CI 用的是 git-bash,不是系统 sh);
// 协议层的等价覆盖在 client_test.go(纯管道,跨平台)。
func TestExecuteEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 无保证存在的 POSIX shell;协议层覆盖见 client_test.go")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-acp.sh")
	body := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"initialize"'*) printf '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}\n' ;;
    *'"session/new"'*) printf '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"sX"}}\n' ;;
    *'"session/prompt"'*)
      printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sX","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"已完成"}}}}\n'
      printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sX","update":{"sessionUpdate":"tool_call","title":"写入 a.go"}}}\n'
      printf '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}\n' ;;
  esac
done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := &Tool{
		runners: map[string]runner{"fake": {command: script}},
		timeout: defaultTimeout,
	}
	// 进度通道(反向验证过):onNotify 里若用 context.Background() 上报,这里收不到
	// 任何 chunk —— 因为上报口(reporter)本身就在 ctx 里,Background 里没有它。
	var mu sync.Mutex
	var chunks []string
	ctx := sdk.WithToolProgressReporter(context.Background(), func(_ string, chunk string) error {
		mu.Lock()
		chunks = append(chunks, chunk)
		mu.Unlock()
		return nil
	})
	out, err := tool.Execute(ctx, `{"runner":"fake","message":"改个 bug","cwd":"`+dir+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	var res struct {
		Runner     string   `json:"runner"`
		SessionID  string   `json:"session_id"`
		StopReason string   `json:"stop_reason"`
		Output     string   `json:"output"`
		ToolCalls  []string `json:"tool_calls"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if res.SessionID != "sX" || res.StopReason != "end_turn" {
		t.Fatalf("协议没走完: %s", b)
	}
	if res.Output != "已完成" {
		t.Fatalf("正文增量应累计成 output, got %q", res.Output)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0] != "写入 a.go" {
		t.Fatalf("外部 agent 的工具调用应被记录: %v", res.ToolCalls)
	}
	mu.Lock()
	joined := strings.Join(chunks, "")
	mu.Unlock()
	if !strings.Contains(joined, "已完成") {
		t.Fatalf("外部 agent 的正文增量应进进度通道(界面看得到它在动), got %q", joined)
	}
	if !strings.Contains(joined, "写入 a.go") {
		t.Fatalf("外部 agent 的工具调用也该进进度通道, got %q", joined)
	}
}

// TestExecuteStartFailureIsStructured 命令不存在 ⇒ 结构化错误(不 panic、不挂住)。
func TestExecuteStartFailureIsStructured(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("路径语义不同,错误文案断言只在 POSIX 上有意义")
	}
	tool := &Tool{
		runners: map[string]runner{"ghost": {command: "definitely-not-a-real-binary-xyz"}},
		timeout: defaultTimeout,
	}
	_, err := tool.Execute(context.Background(), `{"runner":"ghost","message":"x"}`)
	if err == nil {
		t.Fatal("启动失败应返回错误")
	}
	if !strings.Contains(err.Error(), "启动") {
		t.Fatalf("错误应说清是启动失败(并提示查 PATH): %v", err)
	}
}
