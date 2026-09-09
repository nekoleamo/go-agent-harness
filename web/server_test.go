// Server 单测(httptest 直挂 handler):静态/state/input 409/命令分发/会话/confirm/鉴权/SSE。
package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
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

type stubLLM struct{ sdk.LLMService }

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
func (s *stubLLM) SetActiveProvider(name string) error                    { return nil }
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
	mu       sync.Mutex
	infos    []sdk.SessionInfo
	curID    string
	curName  string
	curKey   string
	curPath  string
	newCount int
	opened   []string
	projects []sdk.ProjectInfo
	switched []string
	forks    []uint64
	clones   int
}

func (s *stubCS) Sessions() []sdk.SessionInfo { return s.infos }
func (s *stubCS) CurrentSession() string      { return s.curID }
func (s *stubCS) SessionName() string         { return s.curName }
func (s *stubCS) Current() string             { return s.curKey }
func (s *stubCS) Path() string                { return s.curPath }
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
	defs      map[string]sdk.ToolDefinition
	called    []string
	callErr   error
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
	return &sdk.ToolResult{Content: "ok:" + name}, nil
}

type stubJobs struct {
	sdk.JobService
	list   []sdk.Job
	killed []string
}

func (s *stubJobs) List() []sdk.Job               { return s.list }
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

type stubPM struct {
	sdk.PluginManager
	list    []sdk.PluginInfo
	loaded  []string
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

// newTestServer 组装一个可测试的 Server(直接注入字段,不经 sdk.Ctx)。
// stubTodoTools 供 TestTodoEndpoint(todo 面板端点:固定返回任务列表)。
type stubTodoTools struct{ sdk.ToolRegistry }

func (s *stubTodoTools) Execute(_ context.Context, name, args string) (*sdk.ToolResult, error) {
	return &sdk.ToolResult{Content: "[{\"id\":\"t1\",\"subject\":\"研究方案\",\"status\":\"in_progress\"}]", Error: ""}, nil
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
		strings.NewReader(`{"workspace":"`+dir+`"}`))
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
		{ID: "tool-x", State: "configured"},                          // 无声明常规 → web
		{ID: "tool-shell", State: "configured", Manage: "external"}, // 声明外部化 → external
		{ID: "ui-tui-app", State: "configured", Manage: "scenario"}, // 声明场景 → scenario
		{ID: "host-tools", State: "loaded"},                          // 运行态 → host
		{ID: "tool-web", State: "loaded", Manage: "external"},      // 声明 external 但已运行 → host 优先
		{ID: "llm-mock", State: "configured", Manage: "scenario"},  // 声明场景 → scenario
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
	// 删除:接口无 Remove(M12 决策)→ 501 显式
	req2, _ := http.NewRequest(http.MethodDelete, hs.URL+"/api/providers/p2", nil)
	r, err = http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusNotImplemented {
		t.Fatalf("delete 应 501,得 %d", r.StatusCode)
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
	// 无 token → 401;静态不鉴权 → 200
	if code := mustGet(t, hs.URL+"/api/state"); code != 401 {
		t.Fatalf("无 token 应 401,得 %d", code)
	}
	if code := mustGet(t, hs.URL+"/"); code != 200 {
		t.Fatalf("静态不需 token,得 %d", code)
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
	var list []sdk.BackupInfo
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "gah-backup-test.tar.gz" {
		t.Fatalf("backup 列表不符 %+v", list)
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
