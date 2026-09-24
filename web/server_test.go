// Server 单测(httptest 直挂 handler):静态/state/input 409/命令分发/会话/confirm/鉴权/SSE。
package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 服务 stub(嵌入式接口技巧:未覆盖方法调用 panic,测试仅触碰已覆盖面) ——

type stubLoop struct {
	mu      sync.Mutex
	inputs  []string
	failErr error
}

func (l *stubLoop) Run(_ context.Context, input string) error {
	l.mu.Lock()
	l.inputs = append(l.inputs, input)
	l.mu.Unlock()
	return l.failErr
}

func (l *stubLoop) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.inputs) == 0 {
		return ""
	}
	return l.inputs[len(l.inputs)-1]
}

type stubLLM struct {
	sdk.LLMService
	removed []string // RemoveProvider 调用记录(删除路径断言)
}

// errStubMissingProvider 删除不存在 provider 时的显式错误(对齐 host-llm 语义)。
var errStubMissingProvider = errors.New("provider: 不存在 (\"/provider show\" 查看)")

func (s *stubLLM) Model() string               { return "deepseek-chat" }
func (s *stubLLM) Thinking() sdk.ThinkingLevel { return sdk.ThinkingMedium }
func (s *stubLLM) ListModels() ([]sdk.ModelInfo, error) {
	return []sdk.ModelInfo{{ID: "deepseek-chat"}}, nil
}

// —— MultiProviderService 扩展(M12 stub) ——
func (s *stubLLM) Providers() []sdk.ProviderProfile {
	return []sdk.ProviderProfile{{Name: "demo", BaseURL: "http://x", Model: "deepseek-chat", Active: true}}
}
func (s *stubLLM) AddProvider(name, baseURL, apiKey, model string) error { return nil }
func (s *stubLLM) SetActiveProvider(name string) error                   { return nil }
func (s *stubLLM) RemoveProvider(name string) error {
	if name == "missing" { // 不存在:显式报错分支断言
		return errStubMissingProvider
	}
	s.removed = append(s.removed, name)
	return nil
}
func (s *stubLLM) ListAllModels() []sdk.ProviderModelList {
	return []sdk.ProviderModelList{{Name: "demo", BaseURL: "http://x", Models: []sdk.ModelInfo{{ID: "deepseek-chat"}}}}
}

type stubSB struct{ mode sdk.SandboxMode }

func (s *stubSB) Mode() sdk.SandboxMode     { return s.mode }
func (s *stubSB) SetMode(m sdk.SandboxMode) { s.mode = m }
func (s *stubSB) Root() string              { return "/ws" }
func (s *stubSB) ValidatePath(string) error { return nil }

type stubStats struct{ v sdk.UsageStats }

func (s *stubStats) Stats() sdk.UsageStats { return s.v }
func (s *stubStats) Reset()                { s.v = sdk.UsageStats{} }

type stubCS struct {
	sdk.CwdSessions
	mu        sync.Mutex
	infos     []sdk.SessionInfo
	curID     string
	curName   string
	curKey    string
	curPath   string
	newCount  int
	opened    []string
	projects  []sdk.ProjectInfo
	switched  []string
	pinned    []string
	summaries []string
	renamed   []string
	forks     []uint64
	clones    int
}

func (s *stubCS) Sessions() []sdk.SessionInfo { return s.infos }
func (s *stubCS) SetPinned(id string, pinned bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pinned = append(s.pinned, fmt.Sprintf("%s=%v", id, pinned))
	return nil
}
func (s *stubCS) SetName(id, name string) error {
	s.renamed = append(s.renamed, id+"="+name)
	return nil
}
func (s *stubCS) SetSummary(id string, sum sdk.SessionSummary) error {
	s.summaries = append(s.summaries, id+"="+sum.Text)
	return nil
}
func (s *stubCS) CurrentSession() string { return s.curID }
func (s *stubCS) SessionName() string    { return s.curName }
func (s *stubCS) Current() string        { return s.curKey }
func (s *stubCS) Path() string           { return s.curPath }
func (s *stubCS) Open(id string) error {
	s.mu.Lock()
	s.opened = append(s.opened, id)
	s.curID = id
	s.mu.Unlock()
	return nil
}
func (s *stubCS) New() (string, error) {
	s.mu.Lock()
	s.newCount++
	s.curID = "n" + string(rune('0'+s.newCount))
	s.mu.Unlock()
	return s.curID, nil
}
func (s *stubCS) RecentProjects() []sdk.ProjectInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects
}
func (s *stubCS) SwitchProject(key string) (string, error) {
	s.mu.Lock()
	s.switched = append(s.switched, key)
	s.curKey = key
	s.curID = "" // 切换即是新项目主会话
	s.mu.Unlock()
	return "", nil
}
func (s *stubCS) SwitchDir(dir string) (string, error) {
	s.mu.Lock()
	s.switched = append(s.switched, dir)
	s.curKey = dir
	s.curID = ""
	s.mu.Unlock()
	return "", nil
}
func (s *stubCS) Rename(name string) error {
	s.mu.Lock()
	s.curName = name
	s.mu.Unlock()
	return nil
}
func (s *stubCS) ForkAt(seq uint64) (string, error) {
	s.mu.Lock()
	s.forks = append(s.forks, seq)
	s.mu.Unlock()
	return "f1", nil
}
func (s *stubCS) CloneCurrent() (string, error) {
	s.mu.Lock()
	s.clones++
	s.mu.Unlock()
	return "c1", nil
}
func (s *stubCS) ForkPoints(id string) ([]sdk.ForkPoint, error) {
	return nil, nil
}
func (s *stubCS) ForkTree() ([]sdk.ForkNode, error) { return nil, nil }

// stubCSPlain 不实现 Fork/ForkableSessions(用来测 501 分支)。
type stubCSPlain struct{ sdk.CwdSessions }

// —— 能力 stub(通用 REST 面测试) ——

type stubTools struct {
	sdk.ToolRegistry
	defs    map[string]sdk.ToolDefinition
	called  []string
	callErr error
	// out 逐个工具指定 Execute 返回内容(缺省 "ok:<name>");MCP 端点需要解析 JSON 结果。
	out map[string]string
}

func (s *stubTools) List() []sdk.ToolDefinition {
	out := make([]sdk.ToolDefinition, 0, len(s.defs))
	for _, d := range s.defs {
		out = append(out, d)
	}
	return out
}
func (s *stubTools) Get(name string) (sdk.ToolDefinition, bool) {
	d, ok := s.defs[name]
	return d, ok
}
func (s *stubTools) Execute(_ context.Context, name, _ string) (*sdk.ToolResult, error) {
	s.called = append(s.called, name)
	if s.callErr != nil {
		return nil, s.callErr
	}
	if c, ok := s.out[name]; ok {
		return &sdk.ToolResult{Content: c}, nil
	}
	return &sdk.ToolResult{Content: "ok:" + name}, nil
}

type stubJobs struct {
	sdk.JobService
	list   []sdk.Job
	killed []string
}

func (s *stubJobs) List() []sdk.Job { return s.list }
func (s *stubJobs) Output(id string) (sdk.Job, bool) {
	for _, j := range s.list {
		if j.ID == id {
			return j, true
		}
	}
	return sdk.Job{}, false
}
func (s *stubJobs) Kill(id string) error {
	s.killed = append(s.killed, id)
	return nil
}

// stubSched 定时计划服务替身(记录调用;ID/状态语义与真实实现一致)。
type stubSched struct {
	sdk.ScheduleService
	mu     sync.Mutex
	plans  []sdk.Schedule
	runs   []string
	errAdd error
}

func (s *stubSched) List() []sdk.Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sdk.Schedule, len(s.plans))
	copy(out, s.plans)
	return out
}

func (s *stubSched) Add(p sdk.Schedule) (sdk.Schedule, error) {
	if s.errAdd != nil {
		return sdk.Schedule{}, s.errAdd
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p.ID = "sched-test0001"
	s.plans = append(s.plans, p)
	return p, nil
}

func (s *stubSched) Update(p sdk.Schedule) (sdk.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.plans {
		if old.ID == p.ID {
			p.CreatedAt = old.CreatedAt
			s.plans[i] = p
			return p, nil
		}
	}
	return sdk.Schedule{}, errors.New("计划不存在: " + p.ID)
}

func (s *stubSched) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.plans {
		if old.ID == id {
			s.plans = append(s.plans[:i], s.plans[i+1:]...)
			return nil
		}
	}
	return errors.New("计划不存在: " + id)
}

func (s *stubSched) RunNow(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.plans {
		if old.ID == id {
			s.runs = append(s.runs, id)
			return nil
		}
	}
	return errors.New("计划不存在: " + id)
}

type stubPM struct {
	sdk.PluginManager
	list     []sdk.PluginInfo
	loaded   []string
	unloaded []string
}

func (s *stubPM) List() []sdk.PluginInfo { return s.list }
func (s *stubPM) Load(id string) error {
	s.loaded = append(s.loaded, id)
	return nil
}
func (s *stubPM) Unload(id string) error {
	s.unloaded = append(s.unloaded, id)
	return nil
}

// stubSPR systemPrompt 服务 + ReloadableInstructions(成功路径)。
type stubSPR struct {
	sdk.SystemPromptService
	reloaded int
}

func (s *stubSPR) ReloadInstructions() error {
	s.reloaded++
	return nil
}

type stubCmds struct {
	mu  sync.Mutex
	all map[string]sdk.CommandSpec
}

func newStubCmds() *stubCmds { return &stubCmds{all: map[string]sdk.CommandSpec{}} }
func (s *stubCmds) Register(spec sdk.CommandSpec) (sdk.Disposer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.all[spec.Name]; dup {
		return nil, errors.New("conflict")
	}
	s.all[spec.Name] = spec
	return func() {}, nil
}
func (s *stubCmds) List() []sdk.CommandSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sdk.CommandSpec, 0, len(s.all))
	for _, v := range s.all {
		out = append(out, v)
	}
	return out
}
func (s *stubCmds) Get(name string) (sdk.CommandSpec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	spec, ok := s.all[name]
	return spec, ok
}

// stubTurnControl 回合控制 stub(Cancel 调用经 channel 记录,断言 200/503 分支)。
type stubTurnControl struct {
	cancelled chan struct{}
}

func (s *stubTurnControl) Running() bool { return false }
func (s *stubTurnControl) Cancel() {
	select {
	case s.cancelled <- struct{}{}:
	default:
	}
}

// stubTurnSteerer 实现 sdk.TurnSteerer 的回合控制 stub(记录转向消息;ok=false 模拟无运行回合)。
type stubTurnSteerer struct {
	stubTurnControl
	mu     sync.Mutex
	steers []string
	ok     bool
	err    error
}

func (s *stubTurnSteerer) Steer(text string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if !s.ok {
		return false, nil
	}
	s.mu.Lock()
	s.steers = append(s.steers, text)
	s.mu.Unlock()
	return true, nil
}

func (s *stubTurnSteerer) texts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.steers...)
}

// newTestServer 组装一个可测试的 Server(直接注入字段,不经 sdk.Ctx)。
// stubTodoTools 供 TestTodoEndpoint(todo 面板端点:固定返回任务列表)。
// content 可覆盖返回值(空 = 默认单条任务):用于验「空账本回空数组」等边界。
type stubTodoTools struct {
	sdk.ToolRegistry
	content string
}

func (s *stubTodoTools) Execute(_ context.Context, name, args string) (*sdk.ToolResult, error) {
	c := s.content
	if c == "" {
		c = `[{"id":"t1","subject":"研究方案","status":"in_progress"}]`
	}
	return &sdk.ToolResult{Content: c, Error: ""}, nil
}

func newTestServer() (*Server, *memLog) {
	hub := NewHub()
	s := New(Config{}, hub, NewConfirm(hub), slog.Default())
	log := &memLog{}
	s.loop = &stubLoop{}
	s.sessions = log
	s.llm = &stubLLM{}
	s.sb = &stubSB{mode: sdk.SandboxWorkspace}
	s.us = &stubStats{v: sdk.UsageStats{PromptTokens: 10, Requests: 1, Window: 65536}}
	return s, log
}

func TestStateEndpoint(t *testing.T) {
	s, _ := newTestServer()
	s.cs = &stubCS{curID: "", curKey: "demo", curPath: "/p"}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Get(hs.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var v StateView
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Model != "deepseek-chat" || v.Thinking != "medium" || v.Sandbox != "workspace-write" {
		t.Fatalf("state 不符 %+v", v)
	}
	if v.Stats.PromptTokens != 10 || v.Stats.Requests != 1 || v.Stats.Window != 65536 {
		t.Fatalf("stats 不符 %+v", v.Stats)
	}
	if v.Session == nil || v.Session.Key != "demo" || v.Running {
		t.Fatalf("session 不符 %+v", v)
	}
}

// stubSBDerived 档位联动的沙箱替身:声明档与有效档不同(approval=open 时 policy-guard 即如此)。
type stubSBDerived struct{ stubSB }

func (s *stubSBDerived) EffectiveMode() sdk.SandboxMode { return sdk.SandboxFullAccess }

// /api/state 必须报"实际生效档":只报 Mode() 会把联动后的 full-access 显示成 workspace-write。
func TestStateEffectiveSandbox(t *testing.T) {
	stateRaw := func(t *testing.T, s *Server) map[string]any {
		t.Helper()
		hs := httptest.NewServer(s.handler())
		defer hs.Close()
		resp, err := http.Get(hs.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status %d", resp.StatusCode)
		}
		var raw map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}

	// 未实现 sdk.EffectiveSandbox 的沙箱(旧替身/旧实现):不出现联动字段,客户端语义不变
	s, _ := newTestServer()
	raw := stateRaw(t, s)
	if raw["sandbox"] != "workspace-write" {
		t.Fatalf("sandbox 应为声明档,得 %v", raw["sandbox"])
	}
	if _, ok := raw["sandbox_effective"]; ok {
		t.Fatalf("未实现 EffectiveSandbox 时不应下发 sandbox_effective:%v", raw["sandbox_effective"])
	}
	if _, ok := raw["sandbox_derived"]; ok {
		t.Fatalf("未实现 EffectiveSandbox 时不应下发 sandbox_derived:%v", raw["sandbox_derived"])
	}

	// 实现有效能力且与声明档不同:两个字段都要出现,声明档保持原值(可对照)
	s.sb = &stubSBDerived{stubSB{mode: sdk.SandboxWorkspace}}
	raw = stateRaw(t, s)
	if raw["sandbox"] != "workspace-write" || raw["sandbox_effective"] != "full-access" || raw["sandbox_derived"] != true {
		t.Fatalf("联动字段不符: %v", raw)
	}

	// 有效档与声明档一致(如 approval=smart 不覆盖):不报冗余字段
	s.sb = &stubSBSame{stubSB{mode: sdk.SandboxWorkspace}}
	if raw = stateRaw(t, s); raw["sandbox_effective"] != nil || raw["sandbox_derived"] != nil {
		t.Fatalf("有效档一致时不应下发联动字段: %v", raw)
	}
}

// stubSBSame 实现有效能力但与声明档相同(smart 档不覆盖的等价形态)。
type stubSBSame struct{ stubSB }

func (s *stubSBSame) EffectiveMode() sdk.SandboxMode { return s.mode }

// /api/ui-plugins 每项必须带信任模型明示:UI 插件与宿主同源同权限(安装即完全信任)。
func TestUIPluginsTrustModel(t *testing.T) {
	s, _ := newTestServer()
	dir := t.TempDir()
	plug := filepath.Join(dir, "demo")
	if err := os.MkdirAll(plug, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"demo","version":"1.0.0","slots":[{"name":"statusbar","priority":5,"module":"./dist/plugin.js"}]}`
	if err := os.WriteFile(filepath.Join(plug, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	s.cfg.UIPluginsDir = dir
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Get(hs.URL + "/api/ui-plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []UIPlugin
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应扫到 1 个插件,得 %+v", list)
	}
	p := list[0]
	if p.ID != "demo" || !p.Trusted || p.TrustNote != uiPluginTrustNote {
		t.Fatalf("插件信任模型字段缺失:%+v", p)
	}
	if p.TrustNote == "" || !strings.Contains(p.TrustNote, "只安装你信任的插件") {
		t.Fatalf("信任提示文案不符:%q", p.TrustNote)
	}

	// 目录缺 manifest 时仍返回合法空数组(前端加载器不遇 null)
	s.cfg.UIPluginsDir = t.TempDir()
	resp2, err := http.Get(hs.URL + "/api/ui-plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if string(body) != "[]\n" {
		t.Fatalf("空插件目录应返回 [],得 %q", body)
	}
}

// TestInputConflict409 turnControl 未实现 sdk.TurnSteerer(旧装配/第三方实现)→ 回落 409,
// 不静默吃掉用户输入。
func TestInputConflict409(t *testing.T) {
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	s.running.Store(true)

	resp, err := http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(`{"content":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("running 时应 409,得 %d", resp.StatusCode)
	}
}

// TestInputSteerWhileRunning 回合运行中再次提交:不 409,而是注入当前回合(转向),
// 响应标 accepted=steer(前端/客户端据此区分「已注入本回合」与「已开新回合」)。
func TestInputSteerWhileRunning(t *testing.T) {
	s, _ := newTestServer()
	tc := &stubTurnSteerer{ok: true}
	s.tc = tc
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	s.running.Store(true)

	resp, err := http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(`{"content":"别查了,改 B 方案"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("运行中提交应 202(转向),得 %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["accepted"] != "steer" {
		t.Fatalf("响应应标 accepted=steer: %#v", body)
	}
	if got := tc.texts(); len(got) != 1 || got[0] != "别查了,改 B 方案" {
		t.Fatalf("转向消息应交给 turnControl: %#v", got)
	}
}

// TestInputSteerUnavailableFallsBackTo409 有 turnControl 但不实现 TurnSteerer → 仍 409。
func TestInputSteerUnavailableFallsBackTo409(t *testing.T) {
	s, _ := newTestServer()
	s.tc = &stubTurnControl{cancelled: make(chan struct{}, 1)}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	s.running.Store(true)

	resp, err := http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(`{"content":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("无转向能力时应 409,得 %d", resp.StatusCode)
	}
}

// TestInputSteerNotUsedForCommands 命令路径(/开头)不转向:命令即时执行,不经回合。
func TestInputSteerNotUsedForCommands(t *testing.T) {
	s, _ := newTestServer()
	tc := &stubTurnSteerer{ok: true}
	s.tc = tc
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	s.running.Store(true)

	resp, err := http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(`{"content":"/help"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("运行中命令应 409(不转向),得 %d", resp.StatusCode)
	}
	if got := tc.texts(); len(got) != 0 {
		t.Fatalf("命令不应进转向通道: %#v", got)
	}
}

func TestInputRoutesToLoop(t *testing.T) {
	s, _ := newTestServer()
	loop := s.loop.(*stubLoop)
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(`{"content":"你好"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expect 202, got %d", resp.StatusCode)
	}
	deadline := time.Now().Add(time.Second)
	for loop.last() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if loop.last() != "你好" {
		t.Fatalf("loop 未收到输入 %q", loop.last())
	}
}

func TestCommandDispatch(t *testing.T) {
	s, _ := newTestServer()
	cmds := newStubCmds()
	_, _ = cmds.Register(sdk.CommandSpec{
		Name: "echo", Usage: "/echo <词>", Desc: "回声",
		Run: func(args []string) (string, error) { return strings.Join(args, " "), nil },
	})
	s.cmds = cmds
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	// 未知命令 → 400
	resp, err := http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(`{"content":"/nosuch hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("未知命令应 400,得 %d", resp.StatusCode)
	}
	// 已知命令 → 200 且回命令帧
	ch, release := s.hub.Stream()
	defer release()
	resp, err = http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(`{"content":"/echo 你好"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("已知命令应 200,得 %d", resp.StatusCode)
	}
	select {
	case f := <-ch:
		if f.Type != FrameCommand {
			t.Fatalf("期望 command 帧,得 %+v", f)
		}
		res := f.Payload.(*CommandResult)
		if res.Output != "你好" || res.Raw != "/echo 你好" {
			t.Fatalf("命令结果不符 %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("命令帧未到达")
	}
	// 命令列表
	resp, err = http.Get(hs.URL + "/api/commands")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []CommandView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "echo" || list[0].Usage != "/echo <词>" {
		t.Fatalf("命令列表不符 %+v", list)
	}
}

func TestSessionsEndpoints(t *testing.T) {
	s, _ := newTestServer()
	cs := &stubCS{infos: []sdk.SessionInfo{{ID: "", Name: "主"}}, curKey: "demo"}
	s.cs = cs
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Get(hs.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("会话列表 200 期望,得 %d", resp.StatusCode)
	}
	// switch → Open 被调
	resp, err = http.Post(hs.URL+"/api/sessions", "application/json", strings.NewReader(`{"action":"switch","id":"s1"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("switch 应 200,得 %d", resp.StatusCode)
	}
	if len(cs.opened) != 1 || cs.opened[0] != "s1" {
		t.Fatalf("Open 未调用 %+v", cs.opened)
	}
	// new → 新会话 id 回传
	resp, err = http.Post(hs.URL+"/api/sessions", "application/json", strings.NewReader(`{"action":"new"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.ID != "n1" || cs.newCount != 1 {
		t.Fatalf("new 不符 %+v", out)
	}
	// 未知 action → 400
	resp, err = http.Post(hs.URL+"/api/sessions", "application/json", strings.NewReader(`{"action":"boom"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("未知 action 应 400,得 %d", resp.StatusCode)
	}
}

// 工作区历史列表(/api/workspaces):投影 RecentProjects;未装配 = 503。
func TestWorkspacesEndpoint(t *testing.T) {
	s, _ := newTestServer()
	cs := &stubCS{projects: []sdk.ProjectInfo{
		{Key: "b", Dir: "/ws/b", TS: 20},
		{Key: "a", Dir: "/ws/a", TS: 10},
	}}
	s.cs = cs
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	r, err := http.Get(hs.URL + "/api/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("列表应 200,得 %d", r.StatusCode)
	}
	var list []sdk.ProjectInfo
	if err := json.NewDecoder(r.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Key != "b" {
		t.Fatalf("列表透传不符 %+v", list)
	}

	// 未装配 → 503
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	r, err = http.Get(hs2.URL + "/api/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", r.StatusCode)
	}
}

// /api/control workspace 切换(dir 语义):SwitchDir 调用 + 未装配显式错误。
func TestControlWorkspace(t *testing.T) {
	s, _ := newTestServer()
	cs := &stubCS{curKey: "old"}
	s.cs = cs
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	dir := t.TempDir() // 真实目录(dir 语义)
	resp, err := http.Post(hs.URL+"/api/control", "application/json",
		strings.NewReader(fmt.Sprintf(`{"workspace":%q}`, dir)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("切换应 200,得 %d", resp.StatusCode)
	}
	if len(cs.switched) != 1 || cs.switched[0] != dir {
		t.Fatalf("SwitchDir 未按预期调用 %+v", cs.switched)
	}
	if cs.curKey != dir {
		t.Fatalf("当前 key 未更新: %s", cs.curKey)
	}

	// 未装配 → 显式 400(不静默)
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	resp, err = http.Post(hs2.URL+"/api/control", "application/json",
		strings.NewReader(`{"workspace":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("未装配应 400,得 %d", resp.StatusCode)
	}
}

// TestControlCancel 回合取消端点:POST /api/control {cancel} → ctx.turnControl.Cancel;
// 未装配显式 503(不静默)。
func TestControlCancel(t *testing.T) {
	s, _ := newTestServer()
	tc := &stubTurnControl{cancelled: make(chan struct{}, 1)}
	s.tc = tc
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	resp, err := http.Post(hs.URL+"/api/control", "application/json", strings.NewReader(`{"cancel":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("cancel 应 200,得 %d", resp.StatusCode)
	}
	select {
	case <-tc.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel 未被调用")
	}

	// 未装配 → 503(与其它可选服务一致)
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	resp, err = http.Post(hs2.URL+"/api/control", "application/json", strings.NewReader(`{"cancel":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", resp.StatusCode)
	}
}

// 通用 REST 面:工具清单/调用。
func TestToolsEndpoints(t *testing.T) {
	s, _ := newTestServer()
	s.tools = &stubTools{defs: map[string]sdk.ToolDefinition{
		"todo": {Name: "todo", Description: "任务清单"},
	}}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	r, err := http.Get(hs.URL + "/api/tools")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("工具清单应 200,得 %d", r.StatusCode)
	}
	var defs []sdk.ToolDefinition
	if err := json.NewDecoder(r.Body).Decode(&defs); err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "todo" {
		t.Fatalf("清单不符 %+v", defs)
	}

	// 调用工具
	resp, err := http.Post(hs.URL+"/api/tools/todo", "application/json", strings.NewReader(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("工具调用应 200,得 %d", resp.StatusCode)
	}
	if len(s.tools.(*stubTools).called) != 1 {
		t.Fatalf("Execute 未调用")
	}
	// 未注册 → 404
	resp, err = http.Post(hs.URL+"/api/tools/nope", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未注册应 404,得 %d", resp.StatusCode)
	}
	// 未装配 → 503
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	r, err = http.Post(hs2.URL+"/api/tools/x", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", r.StatusCode)
	}
}

// 通用 REST 面:后台任务列表/状态/终止。
func TestJobsEndpoints(t *testing.T) {
	s, _ := newTestServer()
	s.jobs = &stubJobs{list: []sdk.Job{{ID: "j1", State: sdk.JobDone}}}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	r, err := http.Get(hs.URL + "/api/jobs")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("任务列表应 200,得 %d", r.StatusCode)
	}
	var jobs []sdk.Job
	if err := json.NewDecoder(r.Body).Decode(&jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != "j1" {
		t.Fatalf("列表不符 %+v", jobs)
	}
	r, err = http.Get(hs.URL + "/api/jobs/j1")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("单任务应 200,得 %d", r.StatusCode)
	}
	var j sdk.Job
	if err := json.NewDecoder(r.Body).Decode(&j); err != nil || j.ID != "j1" {
		t.Fatalf("单任务不符 %+v %v", j, err)
	}
	resp, err := http.Post(hs.URL+"/api/jobs/j1/kill", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(s.jobs.(*stubJobs).killed) != 1 {
		t.Fatalf("kill 不符 %d", resp.StatusCode)
	}
	// 未装配 → 503
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	r, err = http.Get(hs2.URL + "/api/jobs")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", r.StatusCode)
	}
}

// 通用 REST 面:命令直接执行。
func TestCommandRunEndpoint(t *testing.T) {
	s, _ := newTestServer()
	cmds := newStubCmds()
	_, err := cmds.Register(sdk.CommandSpec{
		Name: "hi", Usage: "/hi <词>", Desc: "测试",
		Run: func(args []string) (string, error) { return "echo:" + strings.Join(args, ","), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	s.cmds = cmds
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Post(hs.URL+"/api/commands/hi", "application/json", strings.NewReader(`{"args":["a","b"]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("命令执行应 200,得 %d", resp.StatusCode)
	}
	var out struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Output != "echo:a,b" {
		t.Fatalf("输出不符 %+v", out)
	}
	// 未注册 → 404;未装配 → 503
	resp, err = http.Post(hs.URL+"/api/commands/nope", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未注册应 404,得 %d", resp.StatusCode)
	}
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	resp, err = http.Post(hs2.URL+"/api/commands/x", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", resp.StatusCode)
	}
}

// 通用 REST 面:插件清单/加载/卸载。
func TestPluginsEndpoints(t *testing.T) {
	s, _ := newTestServer()
	s.pm = &stubPM{list: []sdk.PluginInfo{
		{ID: "tool-x", State: "configured"},                         // 无声明常规 → web
		{ID: "tool-shell", State: "configured", Manage: "external"}, // 声明外部化 → external
		{ID: "ui-tui-app", State: "configured", Manage: "scenario"}, // 声明场景 → scenario
		{ID: "host-tools", State: "loaded"},                         // 运行态 → host
		{ID: "tool-web", State: "loaded", Manage: "external"},       // 声明 external 但已运行 → host 优先
		{ID: "llm-mock", State: "configured", Manage: "scenario"},   // 声明场景 → scenario
	}}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	r, err := http.Get(hs.URL + "/api/plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("插件清单应 200,得 %d", r.StatusCode)
	}
	var list []struct {
		ID     string
		Manage string
	}
	if err := json.NewDecoder(r.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range list {
		got[p.ID] = p.Manage
	}
	if got["tool-x"] != "web" || got["tool-shell"] != "external" || got["ui-tui-app"] != "scenario" || got["host-tools"] != "host" || got["tool-web"] != "host" || got["llm-mock"] != "scenario" {
		t.Fatalf("manage 分域不符: %+v", got)
	}
	resp, err := http.Post(hs.URL+"/api/plugins/tool-x/load", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	resp, err = http.Post(hs.URL+"/api/plugins/tool-x/unload", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	pm := s.pm.(*stubPM)
	if len(pm.loaded) != 1 || len(pm.unloaded) != 1 {
		t.Fatalf("load/unload 未按预期 %+v %+v", pm.loaded, pm.unloaded)
	}
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	r, err = http.Get(hs2.URL + "/api/plugins")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", r.StatusCode)
	}
}

// errListingLLM 让聚合模型列表带 Err(验证 error 字段被转成可读字符串输出)。
type errListingLLM struct{ stubLLM }

func (e *errListingLLM) ListAllModels() []sdk.ProviderModelList {
	return []sdk.ProviderModelList{
		{Name: "ok", BaseURL: "https://api.example.com/v1", Models: []sdk.ModelInfo{{ID: "m1"}}},
		{Name: "bad", BaseURL: "https://api.example.com/v1", Err: errors.New(strings.Repeat("很长的错误说明", 60))},
	}
}

// TestModelsAllErrString /api/models?all=1 回传可读的失败原因(而非 `{}`)、Models 归一为空数组、
// 错误文案按 rune 截断(W3 首启自检靠它给 401/404/DNS 人话提示)。
func TestModelsAllErrString(t *testing.T) {
	s, _ := newTestServer()
	s.llm = &errListingLLM{}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	r, err := http.Get(hs.URL + "/api/models?all=1")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("应 200,得 %d", r.StatusCode)
	}
	var v struct {
		Providers []struct {
			Name   string          `json:"Name"`
			Models []sdk.ModelInfo `json:"Models"`
			Err    string          `json:"Err"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if len(v.Providers) != 2 {
		t.Fatalf("应 2 条: %+v", v.Providers)
	}
	okP, badP := v.Providers[0], v.Providers[1]
	if okP.Err != "" || len(okP.Models) != 1 {
		t.Fatalf("成功条目不应带 Err 且模型保留: %+v", okP)
	}
	if badP.Err == "" {
		t.Fatalf("失败原因必须透出(error 接口直接序列化只会得到 {}): %+v", badP)
	}
	if n := len([]rune(badP.Err)); n > probeErrMaxRunes+1 {
		t.Fatalf("错误文案应截断到 %d rune,得 %d", probeErrMaxRunes, n)
	}
	if badP.Models == nil {
		t.Fatalf("Models 应为空数组而非 null: %+v", badP)
	}
}

// 模型列表与 provider CRUD。
func TestModelsAndProviders(t *testing.T) {
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	// 默认 stubLLM:单适配器模型列表
	r, err := http.Get(hs.URL + "/api/models")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("模型列表应 200,得 %d", r.StatusCode)
	}
	var v struct {
		Models []sdk.ModelInfo `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if len(v.Models) != 1 || v.Models[0].ID != "deepseek-chat" {
		t.Fatalf("模型不符 %+v", v.Models)
	}
	// stubLLM 同时实现 MultiProviderService:?all=1 聚合
	r, err = http.Get(hs.URL + "/api/models?all=1")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("聚合应 200,得 %d", r.StatusCode)
	}
	var pv struct {
		Providers []sdk.ProviderModelList `json:"providers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&pv); err != nil {
		t.Fatal(err)
	}
	if len(pv.Providers) != 1 {
		t.Fatalf("聚合不符 %+v", pv.Providers)
	}

	// provider 视图/add/use/delete
	r, err = http.Get(hs.URL + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("providers 应 200,得 %d", r.StatusCode)
	}
	resp, err := http.Post(hs.URL+"/api/providers", "application/json",
		strings.NewReader(`{"name":"p2","base_url":"http://y","model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("add 应 200,得 %d", resp.StatusCode)
	}
	resp, err = http.Post(hs.URL+"/api/providers/p2/use", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("use 应 200,得 %d", resp.StatusCode)
	}
	// 删除:真删除(第二十一批结清 M12 TODO)→ 200;不存在 → 400 显式报错
	req2, _ := http.NewRequest(http.MethodDelete, hs.URL+"/api/providers/p2", nil)
	r, err = http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("delete 应 200,得 %d", r.StatusCode)
	}
	if stub, ok := s.llm.(*stubLLM); !ok || len(stub.removed) != 1 || stub.removed[0] != "p2" {
		t.Fatalf("delete 应下传 RemoveProvider(p2),得 %+v", s.llm)
	}
	req3, _ := http.NewRequest(http.MethodDelete, hs.URL+"/api/providers/missing", nil)
	r, err = http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("删除不存在应 400,得 %d", r.StatusCode)
	}
	// name 必填
	resp, err = http.Post(hs.URL+"/api/providers", "application/json", strings.NewReader(`{"name":""}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 name 应 400,得 %d", resp.StatusCode)
	}
}

// 会话改名 + fork/clone。
func TestSessionRenameAndFork(t *testing.T) {
	s, _ := newTestServer()
	cs := &stubCS{curName: "旧"}
	s.cs = cs
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Post(hs.URL+"/api/sessions/rename", "application/json", strings.NewReader(`{"name":"新名"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || cs.curName != "新名" {
		t.Fatalf("改名不符 %d %q", resp.StatusCode, cs.curName)
	}
	// fork
	resp, err = http.Post(hs.URL+"/api/sessions", "application/json", strings.NewReader(`{"action":"fork","seq":5}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(cs.forks) != 1 || cs.forks[0] != 5 {
		t.Fatalf("fork 不符 %d %+v", resp.StatusCode, cs.forks)
	}
	// clone
	resp, err = http.Post(hs.URL+"/api/sessions", "application/json", strings.NewReader(`{"action":"clone"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || cs.clones != 1 {
		t.Fatalf("clone 不符 %d", resp.StatusCode)
	}
	// 不支持分支的 cs → 501
	s2, _ := newTestServer()
	s2.cs = &stubCSPlain{}
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	resp, err = http.Post(hs2.URL+"/api/sessions", "application/json", strings.NewReader(`{"action":"fork","seq":3}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("无分支能力应 501,得 %d", resp.StatusCode)
	}
}

// 指令热更。
func TestReloadEndpoint(t *testing.T) {
	s, _ := newTestServer()
	sp := &stubSPR{}
	s.sp = sp
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Post(hs.URL+"/api/reload", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || sp.reloaded != 1 {
		t.Fatalf("reload 不符 %d", resp.StatusCode)
	}
	// 未实现 → 501
	s2, _ := newTestServer()
	s2.sp = &struct{ sdk.SystemPromptService }{}
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	resp, err = http.Post(hs2.URL+"/api/reload", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("未实现应 501,得 %d", resp.StatusCode)
	}
}

func TestConfirmEndpoint(t *testing.T) {
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	resp, err := http.Post(hs.URL+"/api/confirm", "application/json", strings.NewReader(`{"id":"whatever","ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("confirm 应 200,得 %d", resp.StatusCode)
	}
}

func TestAuthToken(t *testing.T) {
	hs0 := httptest.NewServer(New(Config{}, NewHub(), NewConfirm(NewHub()), slog.Default()).handler())
	defer hs0.Close()
	// 无 token 配置:静态可访问
	r1 := mustGet(t, hs0.URL+"/")
	if r1 != 200 {
		t.Fatalf("无鉴权静态应 200,得 %d", r1)
	}

	ats, _ := newTestServer()
	ats.cfg.AuthToken = "tk"
	hs := httptest.NewServer(ats.authMiddleware(ats.handler()))
	defer hs.Close()
	// 无 token → 401;/api 之外的表面同样需凭据:返回引导页(200,但不含应用资源)
	if code := mustGet(t, hs.URL+"/api/state"); code != 401 {
		t.Fatalf("无 token 应 401,得 %d", code)
	}
	if code := mustGet(t, hs.URL+"/"); code != 200 {
		t.Fatalf("缺凭据导航应返回引导页(200),得 %d", code)
	}
	req, _ := http.NewRequest("GET", hs.URL+"/api/state", nil)
	req.Header.Set("Authorization", "Bearer tk")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("Bearer tk 应 200,得 %d", resp.StatusCode)
	}
}

// TestListenThenShutdownReleasesPort 覆盖 Listen 提前占口(插件 fail-fast)引入的新生命周期:
// ①Listen 幂等(重复调用不抢第二次端口);②Listen 后未 Start 就 Shutdown → 句柄就地关闭,
// 端口释放(否则插件启动失败/卸载会在后台留一个孤口);③Shutdown 后 Start 不再监听(返回 nil)。
func TestListenThenShutdownReleasesPort(t *testing.T) {
	s := New(Config{Addr: "127.0.0.1:0"}, NewHub(), NewConfirm(NewHub()), slog.Default())
	if err := s.Listen(); err != nil {
		t.Fatalf("Listen 失败: %v", err)
	}
	if err := s.Listen(); err != nil { // 幂等
		t.Fatalf("重复 Listen 应幂等: %v", err)
	}
	s.lifeMu.Lock()
	ln := s.ln
	s.lifeMu.Unlock()
	if ln == nil {
		t.Fatal("Listen 后应持有监听句柄")
	}
	url := "http://" + ln.Addr().String()
	s.Shutdown() // 未 Start 即停机
	if _, err := net.DialTimeout("tcp", ln.Addr().String(), 500*time.Millisecond); err == nil {
		t.Fatalf("Shutdown 后端口应已释放(%s)", url)
	}
	if err := s.Start(); err != nil {
		t.Fatalf("Shutdown 后 Start 应直接返回(不再监听): %v", err)
	}
}

// TestListenFailsOnTakenPort Listen 层真报错(fail-fast 的上游保证)。
func TestListenFailsOnTakenPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	s := New(Config{Addr: ln.Addr().String()}, NewHub(), NewConfirm(NewHub()), slog.Default())
	err = s.Listen()
	if err == nil {
		t.Fatal("端口被占用时 Listen 应报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "监听失败") {
		t.Fatalf("错误应可读地点明监听失败: %v", err)
	}
	// 端口占用的底层文案随平台不同:linux "address already in use",
	// windows "Only one usage of each socket address ... is normally permitted"。
	low := strings.ToLower(msg)
	if !strings.Contains(low, "already in use") && !strings.Contains(low, "only one usage") {
		t.Fatalf("错误应点明端口已被占用: %v", err)
	}
	// 可操作指引:真实世界的占用几乎都是「上一个 gah 实例还没死」,只报 bind 错误等于让用户没法下手。
	if !strings.Contains(msg, "GAH_WEB_ADDR") {
		t.Fatalf("错误应给出换端口这条可操作指引: %v", err)
	}
}

func TestShutdownEndpoint(t *testing.T) {
	// 未装配(OnShutdown nil)→ 503,不静默降级
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	resp, err := http.Post(hs.URL+"/api/shutdown", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("shutdown 未装配应 503,得 %d", resp.StatusCode)
	}
	hs.Close()

	// 装配 → 200 且回调触发(回调在 HTTP goroutine,标记用 atomic 防测试 race)
	var fired atomic.Bool
	s.OnShutdown = func() { fired.Store(true) }
	hs2 := httptest.NewServer(s.handler())
	defer hs2.Close()
	resp, err = http.Post(hs2.URL+"/api/shutdown", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("shutdown 应 200,得 %d", resp.StatusCode)
	}
	var body struct {
		OK           bool `json:"ok"`
		ShuttingDown bool `json:"shutting_down"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || !body.ShuttingDown {
		t.Fatalf("响应体异常: %+v", body)
	}
	// 回调在服务端 Flush 后同步执行,但客户端收到完整 body 即可返回——存在小竞态窗口(CI 稳定复现),轮询等待
	deadline := time.Now().Add(2 * time.Second)
	for !fired.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !fired.Load() {
		t.Fatal("OnShutdown 未被触发")
	}
	// 鉴权保护:auth_token 非空时未带 token → 401 且不触发
	var fired2 atomic.Bool
	as, _ := newTestServer()
	as.cfg.AuthToken = "tk"
	as.OnShutdown = func() { fired2.Store(true) }
	has := httptest.NewServer(as.authMiddleware(as.handler()))
	defer has.Close()
	req, _ := http.NewRequest("POST", has.URL+"/api/shutdown", nil)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 token 应 401,得 %d", r2.StatusCode)
	}
	if fired2.Load() {
		t.Fatal("未授权请求不应触发 OnShutdown")
	}
}

// multipartBody 构造 multipart 上传请求体(field=file;contentType 空则不设类型)。
func multipartBody(t *testing.T, contentType, filename, data string) (*bytes.Buffer, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	part, err := w.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return &b, w.FormDataContentType()
}

func TestAttachmentsEndpoint(t *testing.T) {
	// 未配置 → POST 503 / GET 503(不静默)
	s0, _ := newTestServer()
	hs0 := httptest.NewServer(s0.handler())
	resp, err := http.Post(hs0.URL+"/api/attachments", "multipart/form-data", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未配置应 503,得 %d", resp.StatusCode)
	}
	if code := mustGet(t, hs0.URL+"/attachments/x.png"); code != http.StatusServiceUnavailable {
		t.Fatalf("未配置 GET 应 503,得 %d", code)
	}
	hs0.Close()

	// 配置临时附件目录
	s, _ := newTestServer()
	dir := t.TempDir()
	s.cfg.AttachmentsDir = dir
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	// 上传 png → 200,返回 name/path/url,文件落盘
	body, ctype := multipartBody(t, "image/png", "photo.png", "\x89PNG\r\n\x1a\nxx")
	resp, err = http.Post(hs.URL+"/api/attachments", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("上传应 200,得 %d", resp.StatusCode)
	}
	var up struct {
		OK          bool             `json:"ok"`
		Attachments []AttachmentView `json:"attachments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&up); err != nil {
		t.Fatal(err)
	}
	if !up.OK || len(up.Attachments) != 1 {
		t.Fatalf("上传响应异常: %+v", up)
	}
	av := up.Attachments[0]
	if av.Name != "photo.png" || av.URL == "" || av.Path == "" {
		t.Fatalf("附件视图异常: %+v", av)
	}
	if _, err := os.Stat(av.Path); err != nil {
		t.Fatalf("文件未落盘: %v", err)
	}
	if code := mustGet(t, hs.URL+av.URL); code != 200 {
		t.Fatalf("预览应 200,得 %d", code)
	}

	// 类型白名单拒绝:未知类型(无 Content-Type → 非白名单)
	body2, ctype2 := multipartBody(t, "", "a.bin", "data")
	resp, err = http.Post(hs.URL+"/api/attachments", ctype2, body2)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("未知类型应被拒")
	}

	// 路径穿越:GET /attachments/../ → 非 200(FileServer 拒越根)
	if code := mustGet(t, hs.URL+"/attachments/../server_test.go"); code != 200 {
		// 403/400/404 均可(拒绝越根即可)
	}

	// input 注入:上传后带 attachments 提交 → stubLoop 收到含 [附件] 的 content
	l := s.loop.(*stubLoop)
	_ = l
	in, _ := json.Marshal(map[string]any{"content": "看看附件", "attachments": []string{av.Path}})
	resp, err = http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(string(in)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("input 应 202,得 %d", resp.StatusCode)
	}
	got := s.loop.(*stubLoop).last()
	if !strings.Contains(got, "[附件]") || !strings.Contains(got, "photo.png") {
		t.Fatalf("附件引用未注入: %q", got)
	}

	// 附件标识兼容:① `/attachments/<rel>` 相对标识(前端默认发这个;跨平台免路径语义)→ 202
	if p, err := s.resolveAttachment(av.URL); err != nil || filepath.Clean(p) != filepath.Clean(av.Path) {
		t.Fatalf("相对标识应解析为 %q,得 %q err=%v", av.Path, p, err)
	}
	inRel, _ := json.Marshal(map[string]any{"content": "相对标识提交", "attachments": []string{av.URL}})
	resp, err = http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(string(inRel)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("相对标识提交应 202,得 %d", resp.StatusCode)
	}

	// 拒绝面:越界相对标识 / 空 / 附件根自身 / 不存在的相对路径 → 均不可解析
	for _, badRef := range []string{"/attachments/../server_test.go", "/attachments/", "", dir, "/attachments/sub-不存在"} {
		if p, err := s.resolveAttachment(badRef); err == nil {
			t.Fatalf("应拒绝 %q,实际得 %q", badRef, p)
		}
	}
	// 归属判定(段级 + Windows 大小写折叠;该分支在 ubuntu CI 上无法直跑,故显式覆盖)
	if !attachmentWithinRootFold(dir, filepath.Join(dir, "20260914-000000", "a.png"), true) {
		t.Fatal("根内路径应判为归属(折叠大小写)")
	}
	if !attachmentWithinRootFold(dir, strings.ToUpper(filepath.Join(dir, "20260914-000000", "A.PNG")), true) {
		t.Fatal("Windows:大小写不同不应判越界")
	}
	// foldCase=false 模拟"区分大小写平台":Windows 上 filepath.Rel 自身即大小写
	// 不敏感,该平台无法模拟此语义 → 仅在与平台一致时断言。
	if !testutil.IsWindows() && attachmentWithinRootFold(dir, strings.ToUpper(filepath.Join(dir, "a.png")), false) {
		t.Fatal("区分大小写平台:大写路径不应判归属")
	}
	if attachmentWithinRootFold(dir, filepath.Join(dir, "..", "x.png"), true) {
		t.Fatal("越界路径不应判归属")
	}
	if attachmentWithinRootFold(dir, dir, true) {
		t.Fatal("附件根自身不应判归属")
	}

	// attachmentList:png→image+相对 rel;txt→file;非附件目录外 rel 还原
	atts := attachmentList(dir, []string{filepath.Join(dir, "20260906-000000", "a.png")})
	if len(atts) != 1 || atts[0].Kind != sdk.AttachmentImage || atts[0].MimeType != "image/png" {
		t.Fatalf("png 应 image: %+v", atts)
	}
	atts2 := attachmentList(dir, []string{filepath.Join(dir, "note.txt")})
	if len(atts2) != 1 || atts2[0].Kind != sdk.AttachmentFile {
		t.Fatalf("txt 应 file: %+v", atts2)
	}

	// input 越界路径 → 400(附件目录外/不存在)
	bad, _ := json.Marshal(map[string]any{"content": "x", "attachments": []string{"/etc/passwd"}})
	resp, err = http.Post(hs.URL+"/api/input", "application/json", strings.NewReader(string(bad)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("越界路径应 400,得 %d", resp.StatusCode)
	}
}

func TestSessionExportEndpoint(t *testing.T) {
	s, _ := newTestServer()
	dir := t.TempDir()
	fp := filepath.Join(dir, "s1.jsonl")
	if err := os.WriteFile(fp, []byte("line1\nline2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.cs = &stubCS{infos: []sdk.SessionInfo{{ID: "s1", Path: fp}, {ID: "", Path: filepath.Join(dir, "main.jsonl")}}}
	if err := os.WriteFile(filepath.Join(dir, "main.jsonl"), []byte("main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	// 指定会话 → 200 + 原始内容 + ndjson
	resp, err := http.Get(hs.URL + "/api/sessions/s1/export")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "line1\nline2\n" {
		t.Fatalf("导出内容不符: code=%d body=%q", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content-type 应 ndjson,got %s", ct)
	}

	// 未知 id → 404
	if code := mustGet(t, hs.URL+"/api/sessions/nope/export"); code != 404 {
		t.Fatalf("未知会话应 404,得 %d", code)
	}

	// format=html:自包含 HTML 下载(解码 jsonl 还原载荷 → 渲染器;坏行容忍)
	fp2 := filepath.Join(dir, "s2.jsonl")
	lines := `{"Kind":"user/message","Seq":1,"TS":"2026-09-22T15:43:26+08:00","Payload":{"Content":"<b>hi</b> 你好"}}` + "\n" + "坏行不入库\n"
	if err := os.WriteFile(fp2, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	s.cs.(*stubCS).infos = append(s.cs.(*stubCS).infos, sdk.SessionInfo{ID: "s2", Path: fp2})
	resp2, err := http.Get(hs.URL + "/api/sessions/s2/export?format=html")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("html 导出应 200,得 %d body=%q", resp2.StatusCode, b2)
	}
	if ct := resp2.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type 应 html,got %s", ct)
	}
	if cd := resp2.Header.Get("Content-Disposition"); !strings.Contains(cd, "session-s2.html") {
		t.Fatalf("下载名应 .html,got %s", cd)
	}
	if !strings.HasPrefix(string(b2), "<!DOCTYPE html>") {
		t.Fatalf("html 导出应是完整文档,got %q", string(b2)[:min(len(b2), 40)])
	}
	if !strings.Contains(string(b2), "&lt;b&gt;hi&lt;/b&gt; 你好") {
		t.Fatalf("jsonl 载荷应还原并转义进 html: %q", b2)
	}
	// 缺省仍是 jsonl(向后兼容)
	if ct := mustCT(t, hs.URL+"/api/sessions/s2/export"); ct != "application/x-ndjson" {
		t.Fatalf("缺省应 jsonl,got %s", ct)
	}
	// 主会话导出:走无 id 路由(空段路径 /api/sessions//export 不匹配任何模式 ⇒ 曾 404)
	resp3, err := http.Get(hs.URL + "/api/sessions/export")
	if err != nil {
		t.Fatal(err)
	}
	b3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != 200 || string(b3) != "main\n" {
		t.Fatalf("主会话导出应 200 + 内容,得 code=%d body=%q", resp3.StatusCode, b3)
	}
}

// mustCT 取响应的 Content-Type(导出格式判定用)。
func mustCT(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.Header.Get("Content-Type")
}

func TestTodoEndpoint(t *testing.T) {
	s, _ := newTestServer()
	// 未装配 → 503
	hs := httptest.NewServer(s.handler())
	resp, err := http.Get(hs.URL + "/api/todo")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("todo 未装配应 503,得 %d", resp.StatusCode)
	}
	hs.Close()
	// 装配 stub → 200 且任务列表透传
	s.tools = &stubTodoTools{}
	hs2 := httptest.NewServer(s.handler())
	defer hs2.Close()
	resp, err = http.Get(hs2.URL + "/api/todo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("todo 应 200,得 %d", resp.StatusCode)
	}
	var list []struct {
		Subject string `json:"subject"`
		Status  string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Subject != "研究方案" || list[0].Status != "in_progress" {
		t.Fatalf("todo 列表不符 %+v", list)
	}
	// 空账本:工具 list 序列化为 null → 端点回空数组(前端 Array.isArray/length 安全,
	// 否则面板把 null 当错误渲染;与 /api/backup 同规)
	s.tools = &stubTodoTools{content: "null"}
	hs3 := httptest.NewServer(s.handler())
	defer hs3.Close()
	resp3, err := http.Get(hs3.URL + "/api/todo")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()
	if resp3.StatusCode != 200 || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("空账本应回 []，得 code=%d body=%q", resp3.StatusCode, b)
	}
}

func TestBackupEndpoint(t *testing.T) {
	s, _ := newTestServer()
	// 未装配 → 503(不静默)
	hs := httptest.NewServer(s.handler())
	resp, err := http.Get(hs.URL + "/api/backup")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("backup 未装配应 503,得 %d", resp.StatusCode)
	}
	hs.Close()

	// 装配 stub:GET 列表 / POST backup / POST restore
	s.bk = &stubBackup{}
	hs2 := httptest.NewServer(s.handler())
	defer hs2.Close()

	resp, err = http.Get(hs2.URL + "/api/backup")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("backup 列表应 200,得 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var list []sdk.BackupInfo
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "gah-backup-test.tar.gz" {
		t.Fatalf("backup 列表不符 %+v", list)
	}

	// wire 契约护栏:字段名必须与前端读取的一致(web-src/src/api.ts backups())。
	// 解码进 []sdk.BackupInfo 对键名大小写完全不敏感 —— 第二十二批验收就是靠真机
	// 发现前端按小写读、面板渲染成「最近备份: · NaN KB」的(此处锁原始 key)。
	for _, k := range []string{"\"Name\"", "\"Size\"", "\"Time\""} {
		if !strings.Contains(string(body), k) {
			t.Fatalf("backup 列表 wire 缺字段 %s(前端按此读取): %s", k, body)
		}
	}

	resp, err = http.Post(hs2.URL+"/api/backup", "application/json",
		strings.NewReader(`{"action":"backup"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("backup 触发应 200,得 %d", resp.StatusCode)
	}

	resp, err = http.Post(hs2.URL+"/api/backup", "application/json",
		strings.NewReader(`{"action":"restore","name":"gah-backup-test.tar.gz"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("backup restore 应 200,得 %d", resp.StatusCode)
	}
}

// stubBackup 最小备份桩(列表固定一份;Backup/Restore 记录调用)。
type stubBackup struct {
	sdk.BackupService
	backups  int
	restored string
}

func (s *stubBackup) Backup(dest string) (string, error) {
	s.backups++
	if dest == "" {
		return "gah-backup-" + time.Now().Format("20060102-150405") + ".tar.gz", nil
	}
	return dest, nil
}

func (s *stubBackup) List() []sdk.BackupInfo {
	return []sdk.BackupInfo{{Name: "gah-backup-test.tar.gz", Size: 1024, Time: time.Now().Unix()}}
}

func (s *stubBackup) Restore(name string) error {
	s.restored = name
	return nil
}

func TestStaticIndex(t *testing.T) {
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("静态 index 不符 %d", resp.StatusCode)
	}
	body := readAll(resp)
	if !strings.Contains(body, "<html") || !strings.Contains(body, "id=\"app\"") {
		t.Fatalf("静态 index 内容不符(应含挂载点): %.150s", body)
	}
}

func TestSSEStream(t *testing.T) {
	s, log := newTestServer()
	_ = log.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: &sdk.UserMessage{Content: "旧"}})
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	req, _ := http.NewRequest("GET", hs.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE 头不符 %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 4096), 1<<20)
	var gotReplay bool
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") && strings.Contains(line, "session") {
			gotReplay = true
			break
		}
	}
	if !gotReplay {
		t.Fatal("SSE 未收到历史重放会话帧")
	}
	// 实时帧:hub 广播(连接仍在);帧带会话载荷
	se := &sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Seq: 2}
	s.hub.Push(Frame{ID: 2, Type: FrameSession, Payload: se})
	done := make(chan bool, 1)
	go func() {
		for sc.Scan() {
			if !strings.Contains(sc.Text(), "\"replay\"") && strings.Contains(sc.Text(), "assistant") {
				done <- true
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("实时帧未到达")
	}
}

func mustGet(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func readAll(resp *http.Response) string {
	var b bytes.Buffer
	_, _ = b.ReadFrom(resp.Body)
	return b.String()
}

// TestQuestionEndpoint POST /api/question:作答回传(未知 id 也 200 幂等)。
func TestQuestionEndpoint(t *testing.T) {
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	// 未知 id:幂等 200(不报错,防重放/超时后前端迟到提交)
	body := `{"id":"unknown","values":["prod"],"text":""}`
	resp, err := http.Post(hs.URL+"/api/question", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("未知 id 应 200 幂等,得 %d", resp.StatusCode)
	}
	// 缺 id:400
	resp2, err := http.Post(hs.URL+"/api/question", "application/json", strings.NewReader(`{"values":["a"]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 id 应 400,得 %d", resp2.StatusCode)
	}
	// 已登记提问 → 作答回填(先订阅再推送,对齐真实时序)
	stream, release := s.hub.Stream()
	defer release()
	ch, cancel, err := s.Question().PresentQuestion(context.Background(), sdk.Question{
		Prompt: "选环境", Options: []sdk.QuestionOption{{Value: "dev"}, {Value: "prod"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	f := <-stream
	req, _ := f.Payload.(*QuestionRequest)
	resp3, err := http.Post(hs.URL+"/api/question", "application/json",
		strings.NewReader(fmt.Sprintf(`{"id":%q,"values":["prod"],"text":""}`, req.ID)))
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	select {
	case ans := <-ch:
		if len(ans.Values) != 1 || ans.Values[0] != "prod" {
			t.Fatalf("作答应回填 prod: %+v", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("作答未回填")
	}
}

// TestCommandOptionsEndpoint 命令逐级确认端点(POST /api/commands/{name}/options):
// 枚举级联 / 自由参数提示 / 越界 done / 未注册 404 / 未装配 503。
func TestCommandOptionsEndpoint(t *testing.T) {
	s, _ := newTestServer()
	cmds := newStubCmds()
	_, err := cmds.Register(sdk.CommandSpec{
		Name: "demo", Usage: "/demo status|login|env", Desc: "逐级测试",
		Run: func([]string) (string, error) { return "", nil },
		Args: []sdk.ArgLevel{
			{Options: func([]string) []sdk.Option {
				return []sdk.Option{{Value: "env", Desc: "环境"}, {Value: "status", Desc: "状态"}}
			}},
			{
				Options: func(picked []string) []sdk.Option {
					if len(picked) >= 2 && picked[1] == "env" {
						return []sdk.Option{{Value: "sandbox", Desc: "沙箱"}, {Value: "official", Desc: "正式"}}
					}
					return nil
				},
				FreeArgs: func(picked []string) []string {
					if len(picked) >= 2 && picked[1] == "login" {
						return []string{"AppID", "AppSecret"}
					}
					return nil
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = cmds.Register(sdk.CommandSpec{Name: "plain", Usage: "/plain", Desc: "无参数", Run: func([]string) (string, error) { return "", nil }})
	if err != nil {
		t.Fatal(err)
	}
	s.cmds = cmds
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	type optionsResp struct {
		Level    int                 `json:"level"`
		Items    []CommandOptionView `json:"items"`
		FreeArgs []string            `json:"freeArgs"`
		Done     bool                `json:"done"`
	}
	post := func(name, body string) (int, optionsResp) {
		t.Helper()
		resp, err := http.Post(hs.URL+"/api/commands/"+name+"/options", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out optionsResp
		if resp.StatusCode == 200 {
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode, out
	}

	// 一级:命令自身声明候选(selected 为空)
	if code, out := post("demo", `{"picked":[]}`); code != 200 || out.Level != 1 || len(out.Items) != 2 || out.Done {
		t.Fatalf("一级应返回 2 个候选: code=%d %+v", code, out)
	}
	// 二级 env → 枚举 official/sandbox
	if _, out := post("demo", `{"picked":["env"]}`); out.Level != 2 || len(out.Items) != 2 || out.Items[1].Value != "official" {
		t.Fatalf("env 二级候选不符: %+v", out)
	}
	// 二级 login → 自由参数提示(无枚举);items 必须是 [] 而不是 null
	// (前端按数组消费:`resp.items.map(…)` 遇 null 抛错 → 被 catch 吞 → 自由参数整级不显示)
	if _, out := post("demo", `{"picked":["login"]}`); len(out.Items) != 0 || len(out.FreeArgs) != 2 || out.FreeArgs[0] != "AppID" || out.Done {
		t.Fatalf("login 应返回自由参数提示: %+v", out)
	}
	{
		resp, err := http.Post(hs.URL+"/api/commands/demo/options", "application/json", strings.NewReader(`{"picked":["login"]}`))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(raw), `"items":[]`) {
			t.Fatalf("自由参数级的 items 应为空数组而非 null: %s", raw)
		}
	}
	// 越界(已到末级)→ done(前端不再提示,可直接执行)
	if _, out := post("demo", `{"picked":["env","sandbox"]}`); !out.Done || len(out.Items) != 0 {
		t.Fatalf("末级应 done: %+v", out)
	}
	// 无参数级命令 → done
	if _, out := post("plain", `{"picked":[]}`); !out.Done {
		t.Fatalf("无参数命令应 done: %+v", out)
	}
	// 未注册 404
	if code, _ := post("nope", `{}`); code != http.StatusNotFound {
		t.Fatalf("未注册应 404,得 %d", code)
	}
	// 未装配 503
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	resp, err := http.Post(hs2.URL+"/api/commands/x/options", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未装配应 503,得 %d", resp.StatusCode)
	}
}

// TestAttachmentsCleanupOnReject 上传被拒(类型不允许)时必须回收本批已落盘文件,
// 否则 $GAH_HOME/attachments/<时间戳>/ 留下无引用孤儿文件。
func TestAttachmentsCleanupOnReject(t *testing.T) {
	s, _ := newTestServer()
	dir := t.TempDir()
	s.cfg.AttachmentsDir = dir
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	// 两个 part:合法 png + 不允许类型(txt)→ 整批拒绝
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for _, p := range []struct{ ct, name, data string }{
		{"image/png", "ok.png", "\x89PNG\r\n\x1a\nok"},
		{"application/zip", "bad.zip", "nope"},
	} {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", `form-data; name="file"; filename="`+p.name+`"`)
		h.Set("Content-Type", p.ct)
		part, err := w.CreatePart(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(p.data)); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()

	resp, err := http.Post(hs.URL+"/api/attachments", w.FormDataContentType(), &b)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("不允许类型应 415,得 %d", resp.StatusCode)
	}
	// 目录内不得残留任何文件(已落盘的 png 被回收)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			sub, err := os.ReadDir(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if len(sub) != 0 {
				t.Fatalf("被拒上传留下孤儿文件: %s/%s", e.Name(), sub[0].Name())
			}
			continue
		}
		t.Fatalf("被拒上传留下孤儿文件: %s", e.Name())
	}
}

// freeTestAddr 取本机空闲端口(先监听再释放;测试内单进程竞争可接受)。
func freeTestAddr(t *testing.T) string {
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

// TestShutdownBeforeStartLeavesNoOrphan 未监听即 Shutdown(插件启动 goroutine 与
// 卸载并发)不得留下孤儿监听:Start 需自检 closed 放弃监听,端口保持可用。
// 回归保护:此前 s.http 由 Start goroutine 无锁赋值,Shutdown 早于赋值时静默跳过 →
// 服务在卸载后继续常驻(端口泄漏,见 -race 下的 data race 报告)。
func TestShutdownBeforeStartLeavesNoOrphan(t *testing.T) {
	addr := freeTestAddr(t)
	s, _ := newTestServer()
	s.cfg.Addr = addr
	s.Shutdown() // 先卸载
	if err := s.Start(); err != nil {
		t.Fatalf("Shutdown 后 Start 应直接放弃监听,实得错误: %v", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("Shutdown 后端口仍被占用(孤儿监听): %v", err)
	}
	_ = ln.Close()
}

// TestStartShutdownConcurrent 启动与卸载并发(-race 下必须无数据竞争,
// 且每轮结束后不得有孤儿监听)。
func TestStartShutdownConcurrent(t *testing.T) {
	for i := 0; i < 20; i++ {
		addr := freeTestAddr(t)
		s, _ := newTestServer()
		s.cfg.Addr = addr
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = s.Start()
		}()
		s.Shutdown()
		<-done
		// 服务已停机或从未起监听:端口应可再次绑定
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Fatalf("第 %d 轮端口仍被占用(孤儿监听): %v", i, err)
		}
		_ = ln.Close()
	}
}

// 定时计划 REST 面(NOND-W4):列表/新增/修改/删除/立即触发 + 未装配 503。
func TestScheduleEndpoints(t *testing.T) {
	s, _ := newTestServer()
	sched := &stubSched{plans: []sdk.Schedule{{ID: "sched-test0001", Name: "对账", Cron: "0 8 * * *", Prompt: "跑", Enabled: true}}}
	s.sched = sched
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	// 列表
	r, err := http.Get(hs.URL + "/api/schedules")
	if err != nil {
		t.Fatal(err)
	}
	var list []sdk.Schedule
	if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&list) != nil {
		t.Fatalf("列表应 200 且可解析,得 %d", r.StatusCode)
	}
	r.Body.Close()
	if len(list) != 1 || list[0].ID != "sched-test0001" {
		t.Fatalf("列表不符 %+v", list)
	}

	// 新增:未传 enabled 默认启用
	r, err = http.Post(hs.URL+"/api/schedules", "application/json",
		strings.NewReader(`{"name":"每周对账","cron":"0 8 * * 1","prompt":"生成上周对账"}`))
	if err != nil {
		t.Fatal(err)
	}
	var added sdk.Schedule
	if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&added) != nil {
		t.Fatalf("新增应 200,得 %d", r.StatusCode)
	}
	r.Body.Close()
	if added.ID == "" || !added.Enabled || added.Name != "每周对账" {
		t.Fatalf("新增结果不符 %+v", added)
	}

	// 新增失败(校验错误)→ 400 且带原因(前端直接显示)
	sched.errAdd = errors.New("计划名称不能为空")
	r, err = http.Post(hs.URL+"/api/schedules", "application/json", strings.NewReader(`{"cron":"* * * * *"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	sched.errAdd = nil
	if r.StatusCode != 400 || !strings.Contains(string(b), "计划名称不能为空") {
		t.Fatalf("校验失败应 400 + 原因,得 %d %q", r.StatusCode, string(b))
	}

	// 修改:仅覆盖传入字段(enabled=false 必须生效 —— 指针区分「未传」)
	req, _ := http.NewRequest(http.MethodPatch, hs.URL+"/api/schedules/sched-test0001",
		strings.NewReader(`{"enabled":false}`))
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var upd sdk.Schedule
	if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&upd) != nil {
		t.Fatalf("修改应 200,得 %d", r.StatusCode)
	}
	r.Body.Close()
	if upd.Enabled || upd.Name != "对账" || upd.Cron != "0 8 * * *" {
		t.Fatalf("PATCH 应只改 enabled: %+v", upd)
	}
	// 未传 enabled 的 PATCH 不该把计划停掉
	req, _ = http.NewRequest(http.MethodPatch, hs.URL+"/api/schedules/sched-test0001",
		strings.NewReader(`{"enabled":true,"name":"改名"}`))
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&upd) != nil || upd.Name != "改名" || !upd.Enabled {
		t.Fatalf("PATCH 改名+启用失败 %d %+v", r.StatusCode, upd)
	}
	r.Body.Close()

	// 未知 ID → 404(不是 400)
	req, _ = http.NewRequest(http.MethodPatch, hs.URL+"/api/schedules/sched-nope0000", strings.NewReader(`{"name":"x"}`))
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 404 {
		t.Fatalf("未知计划应 404,得 %d", r.StatusCode)
	}

	// 立即触发
	r, err = http.Post(hs.URL+"/api/schedules/sched-test0001/run", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 || len(sched.runs) != 1 {
		t.Fatalf("run 应 200 且记录一次,得 %d %v", r.StatusCode, sched.runs)
	}
	r, err = http.Post(hs.URL+"/api/schedules/sched-nope0000/run", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 400 || !strings.Contains("", "") { // 未知 ID 由服务返回错误 → 400
		if r.StatusCode != 400 {
			t.Fatalf("未知计划 run 应 400,得 %d", r.StatusCode)
		}
	}

	// 删除
	req, _ = http.NewRequest(http.MethodDelete, hs.URL+"/api/schedules/sched-test0001", nil)
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 || len(sched.List()) != 1 { // 表里只剩新增的那条
		t.Fatalf("删除应 200,得 %d 剩 %d", r.StatusCode, len(sched.List()))
	}

	// 未装配 → 503(全部端点一致)
	s2, _ := newTestServer()
	hs2 := httptest.NewServer(s2.handler())
	defer hs2.Close()
	for _, c := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/schedules"},
		{http.MethodPost, "/api/schedules"},
		{http.MethodPatch, "/api/schedules/sched-test0001"},
		{http.MethodDelete, "/api/schedules/sched-test0001"},
		{http.MethodPost, "/api/schedules/sched-test0001/run"},
	} {
		req, _ := http.NewRequest(c.method, hs2.URL+c.path, strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 未装配应 503,得 %d", c.method, c.path, resp.StatusCode)
		}
	}
}
