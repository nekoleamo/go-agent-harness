// ui-web-app 生命周期与鉴权接线测试(覆盖率补强):
// 用最小 sdk.Ctx 桩把插件启动链真正跑起来(注入 → 建服务 → 监听 → OnReady → Disposer),
// 断言三件真实不变量:①分支装配正确(单 profile Provide ctx.confirm/question;融合分支改注册呈现者);
// ②token 模式下日志给出带 fragment 的访问地址、且真服务上静态/接口都受鉴权门保护;
// ③Disposer 后监听确实关闭(不留孤儿服务)。
package uiweb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 最小 sdk.Ctx 桩 ——

type stubCtx struct {
	mu         sync.Mutex
	provided   map[string]any
	regs       []string // 融合注册的渠道名
	sessions   sdk.SessionLog
	noSessions bool // 模拟宿主未装配 ctx.sessions(Inject 显式报错)
	fusion     sdk.ConfirmFusion
	fusionQs   sdk.QuestionService
	unsubs     int
	log        *slog.Logger
	logbuf     *syncBuffer
}

func newStubCtx() *stubCtx {
	buf := &syncBuffer{}
	return &stubCtx{
		provided: map[string]any{},
		sessions: &stubLog{},
		logbuf:   buf,
		log:      slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

func (c *stubCtx) Provide(key string, svc any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, dup := c.provided[key]; dup {
		return fmt.Errorf("stubCtx: 重复注册 %s", key)
	}
	c.provided[key] = svc
	return nil
}

func (c *stubCtx) Inject(key string, out any) error {
	switch key {
	case "ctx.sessions":
		if c.noSessions {
			return errors.New("stubCtx: 未装配 ctx.sessions")
		}
		*(out.(*sdk.SessionLog)) = c.sessions
		return nil
	case "ctx.agentLoop", "ctx.llm", "ctx.sandbox":
		return nil // 零值即可:本测试只验装配与鉴权,不跑回合
	case "ctx.confirmFusion":
		if c.fusion == nil {
			return errors.New("stubCtx: 未装配 ctx.confirmFusion")
		}
		*(out.(*sdk.ConfirmFusion)) = c.fusion
		return nil
	case "ctx.question":
		if c.fusionQs == nil {
			return errors.New("stubCtx: 未装配 ctx.question")
		}
		*(out.(*sdk.QuestionService)) = c.fusionQs
		return nil
	}
	return fmt.Errorf("stubCtx: 未装配 %s", key)
}

func (c *stubCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer {
	c.mu.Lock()
	c.unsubs++
	c.mu.Unlock()
	return func() {}
}

func (c *stubCtx) Emit(context.Context, string, any, sdk.DispatchMode) (any, error) {
	return nil, nil
}
func (c *stubCtx) Logger() *slog.Logger { return c.log }

func (c *stubCtx) has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.provided[key]
	return ok
}

func (c *stubCtx) logged() string { return c.logbuf.String() }

// syncBuffer 并发安全日志缓冲(日志写入来自服务 goroutine)。
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// stubLog 最小会话日志桩(Hub 订阅只做转发,不读历史;仍给全量实现避免 nil 语义混淆)。
type stubLog struct{}

func (s *stubLog) Append(sdk.SessionEvent) error                 { return nil }
func (s *stubLog) DeriveMessages() []sdk.LLMMessage              { return nil }
func (s *stubLog) Replay() []sdk.SessionEvent                    { return nil }
func (s *stubLog) Flush() error                                  { return nil }
func (s *stubLog) SetPath(string)                                {}
func (s *stubLog) Load(string) error                             { return nil }
func (s *stubLog) SetHistory(int)                                {}
func (s *stubLog) RegisterCompressor(int, sdk.SessionCompressor) {}

// stubFusion 确认融合桩:只记注册渠道。
type stubFusion struct{ regs *[]string }

func (f *stubFusion) Register(channel string, _ sdk.ConfirmPresenter) sdk.Disposer {
	*f.regs = append(*f.regs, channel)
	return func() {}
}

// freeAddr 取一个本机空闲端口(先监听再释放;测试内单进程竞争可接受)。
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// waitHTTP 有界轮询直到拿到响应(服务在 goroutine 里监听,需等就绪)。
func waitHTTP(t *testing.T, method, url string) (*http.Response, error) {
	t.Helper()
	var lastErr error
	for i := 0; i < 200; i++ {
		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	return nil, fmt.Errorf("等待 %s 就绪超时: %w", url, lastErr)
}

// TestPluginStartLifecycleSingleProfile 单 profile 分支:Provide ctx.confirm/ctx.question,
// 日志给出带 fragment 的访问地址,token 门真实生效,Disposer 后监听关闭。
func TestPluginStartLifecycleSingleProfile(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("GAH_WEB_OPEN", "0") // 不真开浏览器
	addr := freeAddr(t)
	t.Setenv("GAH_WEB_ADDR", addr)

	c := newStubCtx()
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{"auth_token": "sekret"}})
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if !c.has("ctx.confirm") || !c.has("ctx.question") {
		t.Fatalf("单 profile 应 Provide ctx.confirm 与 ctx.question,实得 %v", c.provided)
	}
	// token 门:静态与接口都需凭据(缺凭据 → 静态给引导页,接口 401)
	resp, err := waitHTTP(t, http.MethodGet, "http://"+addr+"/api/state")
	if err != nil {
		t.Fatalf("服务未就绪: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无凭据访问 /api/state 应 401,实得 %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp, err = waitHTTP(t, http.MethodGet, "http://"+addr+"/")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "需要访问凭据") {
		t.Fatalf("无凭据访问 / 应给引导页,实得 %d %q", resp.StatusCode, trunc(body, 120))
	}
	if strings.Contains(body, "#token=sekret") {
		t.Fatal("引导页不得内嵌 token")
	}
	// OnReady 日志:token 模式给出 fragment 地址(用户复制即可进入 UI)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(c.logged(), "#token=sekret") {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(c.logged(), "#token=sekret") {
		t.Fatalf("token 模式日志应含带 fragment 的访问地址,实得 %q", c.logged())
	}
	dis()
	// 撤销后监听关闭(不留孤儿服务)
	if _, err := http.Get("http://" + addr + "/api/state"); err == nil {
		t.Fatal("Disposer 后仍可连接,监听未关闭")
	}
}

// TestPluginStartLifecycleFusionBranch 融合分支:已装配 ctx.confirmFusion →
// 注册为呈现者(渠道名 web)且**不**再 Provide ctx.confirm(避免双通道)。
func TestPluginStartLifecycleFusionBranch(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("GAH_WEB_OPEN", "0")
	t.Setenv("GAH_WEB_ADDR", freeAddr(t))

	c := newStubCtx()
	c.fusion = &stubFusion{regs: &c.regs}
	c.fusionQs = stubQuestion{}
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer dis()
	if c.has("ctx.confirm") {
		t.Fatal("融合分支不应 Provide ctx.confirm(应由 fusion 统一呈现)")
	}
	if len(c.regs) != 1 || c.regs[0] != "web" {
		t.Fatalf("应向 fusion 注册 web 渠道,实得 %v", c.regs)
	}
	// 非 token 模式:访问地址不带 fragment
	if strings.Contains(c.logged(), "#token=") {
		t.Fatalf("非 token 模式不应带 fragment: %q", c.logged())
	}
}

// TestPluginStartMissingSessions 未装配 ctx.sessions → 显式失败(不静默起半截服务)。
func TestPluginStartMissingSessions(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("GAH_WEB_OPEN", "0")
	c := newStubCtx()
	c.noSessions = true
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err == nil {
		t.Fatal("ctx.sessions 缺失应显式报错")
	}
	if c.has("ctx.confirm") {
		t.Fatal("启动失败时不应留下已注册服务")
	}
}

// TestUIPluginsHomeEnv 数据目录单根:GAH_HOME 优先,未设时回 TempDir(不落 cwd)。
func TestUIPluginsHomeEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GAH_HOME", dir)
	if got := uiPluginsHome(); got != dir {
		t.Fatalf("GAH_HOME 应作为数据根,实得 %q", got)
	}
	if err := os.Unsetenv("GAH_HOME"); err != nil {
		t.Fatal(err)
	}
	if got := uiPluginsHome(); got == "" || got == "." {
		t.Fatalf("未设 GAH_HOME 应回退 TempDir,实得 %q", got)
	}
}

// stubQuestion 满足 sdk.QuestionService(融合分支注入用)。
type stubQuestion struct{}

func (stubQuestion) Ask(context.Context, sdk.Question) (sdk.QuestionAnswer, error) {
	return sdk.QuestionAnswer{}, nil
}
func (stubQuestion) RegisterQuestioner(string, sdk.QuestionPresenter) sdk.Disposer {
	return func() {}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
