// 子代理继承当前角色的模型/思考档(第八十六批,方案 D 的**必然结果**)。
//
// 为什么必须显式钉住:子代理不经过 agent-loop,而是自己拼 `&sdk.LLMRequest{Messages: …}`
// 直调 ctx.llm.Complete。于是"事件发在 Complete 里"这一选择让子代理**自动**拿到了角色模型 ——
// 这是语义自洽(同一角色的不同分身当然同模型),但它是个会被人悄悄改掉的行为,
// 所以这里用一条用例把"经 Complete ⇒ 拿到注入值"钉死;真要改口径就得先改这条测试。
package hostfanout

import (
	"context"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestSubAgentInheritsRoleModel(t *testing.T) {
	reg := &fanoutRecordingTools{res: sdk.ToolResult{Content: `{"output":"ok"}`}}
	ad := &recordingLLM{tool: "shell", args: `{"command":"echo sub"}`, final: "完成"}
	svc, _, c := buildContractEnvCtx(t, reg, ad)

	// 角色插件在本环境未装配,这里直接扮演它:在请求发出前填上角色声明的模型与思考档。
	dw := c.Subscribe(sdk.EventLLMPreRequest, func(_ context.Context, ev *sdk.Event) error {
		req := ev.Payload.(*sdk.LLMRequest)
		req.Model = "role-model"
		req.Thinking = sdk.ThinkingLow
		req.ThinkingSet = true
		return nil
	})
	defer dw()

	if _, err := svc.Agent(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	ad.mu.Lock()
	models := append([]string(nil), ad.models...)
	ad.mu.Unlock()
	if len(models) == 0 {
		t.Fatal("子代理一轮都没发请求")
	}
	for i, m := range models {
		if m != "role-model" {
			t.Fatalf("第 %d 次子代理请求未继承角色模型: %q(全部:%v)", i+1, m, models)
		}
	}
}
