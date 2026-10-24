package hostfanout

// 委派档位快照(pin):子代理的每次工具调用都应看到**发起那一刻**的档位快照。
//
// 判据直接取「工具执行时 ctx 里的快照」 —— 那是裁决链真正读的位置(policy-guard 的
// decide / ValidatePathFor),不是内部变量。

import (
	"context"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// tierProbeTools 记录每次工具调用看到的档位快照。
type tierProbeTools struct {
	mu    sync.Mutex
	snaps []sdk.TierSnapshot
	oks   []bool
}

func (r *tierProbeTools) Register(sdk.Tool) sdk.Disposer { return func() {} }

func (r *tierProbeTools) List() []sdk.ToolDefinition {
	return []sdk.ToolDefinition{{Name: "shell", Description: "执行 shell", InputSchema: map[string]any{"type": "object"}}}
}

func (r *tierProbeTools) Get(name string) (sdk.ToolDefinition, bool) {
	for _, d := range r.List() {
		if d.Name == name {
			return d, true
		}
	}
	return sdk.ToolDefinition{}, false
}

func (r *tierProbeTools) Execute(ctx context.Context, _, _ string) (*sdk.ToolResult, error) {
	snap, ok := sdk.TierSnapshotFrom(ctx)
	r.mu.Lock()
	r.snaps = append(r.snaps, snap)
	r.oks = append(r.oks, ok)
	r.mu.Unlock()
	return &sdk.ToolResult{Content: `{"output":"ok"}`}, nil
}

func (r *tierProbeTools) seen() ([]sdk.TierSnapshot, []bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sdk.TierSnapshot(nil), r.snaps...), append([]bool(nil), r.oks...)
}

var _ sdk.ToolRegistry = (*tierProbeTools)(nil)

// stubAP 声明档 open、有效档 strict(模拟"角色/会话把档收紧了")。
type stubAP struct{}

func (stubAP) Mode() sdk.ApprovalMode          { return sdk.ApprovalOpen }
func (stubAP) SetMode(sdk.ApprovalMode)        {}
func (stubAP) EffectiveMode() sdk.ApprovalMode { return sdk.ApprovalStrict }

// TestSubAgentCarriesTierSnapshot 后台与同步两条委派路径都必须带上快照。
// **反向验证**:去掉 spawnWorker / runSubAgent 里的 withTierSnapshot ⇒ 本用例必须红。
func TestSubAgentCarriesTierSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(svc sdk.FanoutService, ctx context.Context) error
	}{
		{"后台 spawn", func(svc sdk.FanoutService, ctx context.Context) error {
			id, err := svc.SpawnAgent(ctx, "后台任务")
			if err != nil {
				return err
			}
			waitAgentDone(t, svc, id)
			return nil
		}},
		{"同步 agent", func(svc sdk.FanoutService, ctx context.Context) error {
			_, err := svc.Agent(ctx, "同步任务")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &tierProbeTools{}
			ad := &recordingLLM{tool: "shell", args: `{"command":"echo sub"}`, final: "完成"}
			svc, _, c := buildContractEnvCtx(t, reg, ad)
			if err := c.Provide("ctx.approval", sdk.ApprovalService(stubAP{})); err != nil {
				t.Fatal(err)
			}
			if err := tc.run(svc, context.Background()); err != nil {
				t.Fatal(err)
			}
			snaps, oks := reg.seen()
			if len(snaps) == 0 {
				t.Fatal("子代理应调过工具")
			}
			for i := range snaps {
				if !oks[i] {
					t.Fatalf("第 %d 次工具调用没有档位快照 —— 委派后按现算档裁决,发起时的 strict 承诺失效", i+1)
				}
				if snaps[i].Approval != sdk.ApprovalStrict {
					t.Fatalf("快照应取**有效**档 strict(不是声明档 open),得 %+v", snaps[i])
				}
			}
		})
	}
}

// TestNoServicesNoSnapshot 没有审批/沙箱服务时不注入空快照(旧宿主行为零变化)。
func TestNoServicesNoSnapshot(t *testing.T) {
	reg := &tierProbeTools{}
	ad := &recordingLLM{tool: "shell", args: `{"command":"echo sub"}`, final: "完成"}
	svc, _, _ := buildContractEnvCtx(t, reg, ad)
	if _, err := svc.Agent(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	snaps, oks := reg.seen()
	if len(snaps) == 0 {
		t.Fatal("子代理应调过工具")
	}
	for i := range oks {
		if oks[i] {
			t.Fatalf("没有任何档位服务时不应注入空快照(得 %+v)", snaps[i])
		}
	}
}
