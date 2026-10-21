// 委派审批的**会话继承**(D5 · 2026-10-21)。
//
// 问题:后台子代理(spawn / fork / worktree 后台)的 ctx 原本是 context.Background()
// ⇒ **会话归属被丢掉**。而裁决链按会话归属取值:
//   - 会话级审批档(policy-guard 的 sessionPref);
//   - 会话的角色收紧(host-roles 的 RoleForSession)。
//
// 丢了它,后台子代理就退回**全局**档位:父回合被会话级 strict 拦下的操作,
// 后台子代理却按全局 open 直接放行 ⇒ **经委派提权**(安全缺陷)。
//
// 修法:后台 ctx 用 context.WithoutCancel(父 ctx) —— 继承值(含会话归属),
// 但不受父 ctx 的取消/超时影响(后台子代理要在父回合结束后继续跑)。
package hostfanout

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// sessionProbeTools registry 替身:记录每次工具调用**看到的会话归属**。
type sessionProbeTools struct {
	mu       sync.Mutex
	sessions []string
}

func (r *sessionProbeTools) Register(sdk.Tool) sdk.Disposer { return func() {} }

func (r *sessionProbeTools) List() []sdk.ToolDefinition {
	return []sdk.ToolDefinition{{Name: "shell", Description: "执行 shell", InputSchema: map[string]any{"type": "object"}}}
}

func (r *sessionProbeTools) Get(name string) (sdk.ToolDefinition, bool) {
	for _, d := range r.List() {
		if d.Name == name {
			return d, true
		}
	}
	return sdk.ToolDefinition{}, false
}

func (r *sessionProbeTools) Execute(ctx context.Context, _, _ string) (*sdk.ToolResult, error) {
	r.mu.Lock()
	r.sessions = append(r.sessions, sdk.SessionFromContext(ctx))
	r.mu.Unlock()
	return &sdk.ToolResult{Content: `{"output":"ok"}`}, nil
}

func (r *sessionProbeTools) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.sessions...)
}

var _ sdk.ToolRegistry = (*sessionProbeTools)(nil)

// TestBackgroundSubAgentKeepsSessionContext 后台子代理的工具调用必须仍在**父回合的会话**里。
//
// **反向验证**:把 spawnWorker 的 ctx 改回 context.Background() ⇒ 本用例必须红。
func TestBackgroundSubAgentKeepsSessionContext(t *testing.T) {
	reg := &sessionProbeTools{}
	ad := &recordingLLM{tool: "shell", args: `{"command":"echo sub"}`, final: "完成"}
	svc, _, _ := buildContractEnvCtx(t, reg, ad)

	// 父回合的 ctx:带会话归属(与 host-agent-loop 注入的同款)
	parent := sdk.WithSessionContext(context.Background(), "sess-A")
	id, err := svc.SpawnAgent(parent, "后台任务")
	if err != nil {
		t.Fatal(err)
	}
	waitAgentDone(t, svc, id)
	seen := reg.seen()
	if len(seen) == 0 {
		t.Fatal("后台子代理应调过工具")
	}
	for _, s := range seen {
		if s != "sess-A" {
			t.Fatalf("后台子代理的工具调用应仍在父会话 sess-A 里(实得 %q):丢了会话归属 ⇒ 经委派提权", s)
		}
	}
}

// TestSyncSubAgentKeepsSessionContext 同步子代理(agent)同样要在父会话里(回归护栏)。
func TestSyncSubAgentKeepsSessionContext(t *testing.T) {
	reg := &sessionProbeTools{}
	ad := &recordingLLM{tool: "shell", args: `{"command":"echo sub"}`, final: "完成"}
	svc, _, _ := buildContractEnvCtx(t, reg, ad)

	parent := sdk.WithSessionContext(context.Background(), "sess-B")
	if _, err := svc.Agent(parent, "跑一轮"); err != nil {
		t.Fatal(err)
	}
	seen := reg.seen()
	if len(seen) == 0 {
		t.Fatal("子代理应调过工具")
	}
	if seen[0] != "sess-B" {
		t.Fatalf("同步子代理应留在父会话 sess-B(实得 %q)", seen[0])
	}
}

// TestBackgroundSubAgentOutlivesParentCancel 后台子代理**不受父 ctx 取消**影响
// (这是 Background/WithoutCancel 的那一半语义:父回合结束它还活着)。
func TestBackgroundSubAgentOutlivesParentCancel(t *testing.T) {
	reg := &sessionProbeTools{}
	ad := &recordingLLM{tool: "shell", args: `{"command":"echo sub"}`, final: "完成"}
	svc, _, _ := buildContractEnvCtx(t, reg, ad)

	parent, cancel := context.WithCancel(sdk.WithSessionContext(context.Background(), "sess-C"))
	id, err := svc.SpawnAgent(parent, "后台任务")
	if err != nil {
		t.Fatal(err)
	}
	cancel() // 父 ctx 立刻取消:后台子代理仍应跑完
	waitAgentDone(t, svc, id)
	h, _ := svc.AgentStatus(id)
	if h.State != sdk.AgentDone {
		t.Fatalf("父 ctx 取消不应带倒后台子代理: %+v", h)
	}
	if !strings.Contains(h.Result, "完成") {
		t.Fatalf("后台子代理应跑完: %+v", h)
	}
}

// waitAgentDone 轮询等待后台子代理结束。
func waitAgentDone(t *testing.T, svc sdk.FanoutService, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h, ok := svc.AgentStatus(id)
		if !ok {
			t.Fatalf("子代理会话 %s 应存在", id)
		}
		if h.State == sdk.AgentDone || h.State == sdk.AgentFailed {
			if h.State != sdk.AgentDone {
				t.Fatalf("子代理应完成: %+v", h)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %+v", h)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
