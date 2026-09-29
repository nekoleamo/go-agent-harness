// 子代理继承「角色全三件套」端到端(第九十批:验证并钉住,不是新功能)。
//
// 背景:第八十六批让子代理**自动**继承当前角色的 model/thinking(把事件发在 llm.Complete
// 单点,子代理也必经此处),但「身份槽 + 技能可见性」是否同样进子代理,此前没有用例钉住 ——
// 子代理不走 host-agent-loop,而是自己拼 `&sdk.LLMRequest{Messages: …}` 直调 ctx.llm.Complete,
// 所以它完全可能绕过系统提示(身份槽在 host-system-prompt)或另建一套技能索引(可见性在
// ctx.skills 的判定函数上)。DESIGN §14.1 里把「子代理继承角色身份/技能」一直登记为**未实施**。
//
// 本文件是**结构性验证**:把这两条从"应该成立"变成"有用例的事实"。装配真实
// host-llm/host-tools/host-system-prompt/host-skills/host-roles/host-fanout,经 `subagent`
// 工具真跑一轮子代理(走完整工具管线),断言子代理请求里:
//   - 系统提示含身份槽(角色名 + 身份句 + 工作规则):身份走 sp.Assemble;
//   - 技能可见性与主会话一致:同一份共享技能库,角色只挂 skill-a ⇒ 子代理读 skill-b 被拒;
//   - 模型为角色声明的那个:走的仍是 llm.Complete 单点注入。
//
// **证伪面**(否则用例可能只是"碰巧通过"):先跑一遍**基线**(不启用角色)同一条子代理任务 ——
// 那时必须没有身份槽、读 skill-b 成功、模型是会话档。同一条用例里两套期望互为对照。
package tests

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// roleProbe 探针适配器:记录子代理每次请求的系统提示/模型,并在子代理里读一个共享技能 ——
// 读得成还是读不成,完全由「当前角色的挂载清单」决定(两条路径都读同一个 name)。
type roleProbe struct {
	mu      sync.Mutex
	systems []string // 每次请求的系统消息(子代理装配后的)
	models  []string // 每次请求最终发给适配器的模型
	tools   []string // 子代理看到的工具回包(有 tool 消息时取第一条)
	names   []string // 本探针读的技能名(回读给断言)
}

func (a *roleProbe) Name() string { return "role-probe" }

func (a *roleProbe) reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.systems, a.models, a.tools, a.names = nil, nil, nil, nil
}

func (a *roleProbe) snap() (systems, models, tools []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.systems...), append([]string(nil), a.models...), append([]string(nil), a.tools...)
}

// probeSkill 探针要读的技能名:共享库里存在,但角色只挂 skill-a ⇒ 读得成/读不成由挂载清单决定。
const probeSkill = "skill-b"

func (a *roleProbe) Complete(_ context.Context, req *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	sys, toolMsg := "", ""
	for _, m := range req.Messages {
		switch {
		case m.Role == sdk.RoleSystem && sys == "":
			sys = m.Content
		case m.Role == sdk.RoleTool && toolMsg == "":
			toolMsg = m.Content
		}
	}
	a.mu.Lock()
	a.systems = append(a.systems, sys)
	a.models = append(a.models, req.Model)
	if toolMsg != "" {
		a.tools = append(a.tools, toolMsg)
	}
	a.mu.Unlock()
	if toolMsg != "" { // 工具已执行 → 收尾
		return emitText(onChunk, "已核对:"+toolMsg)
	}
	a.mu.Lock()
	a.names = append(a.names, probeSkill)
	a.mu.Unlock()
	// 首轮:让子代理读那个"库里存在、角色是否可见取决于挂载"的技能
	return emitCall(onChunk, "read_skill", `{"name":"`+probeSkill+`"}`)
}

// buildSubAgentRolesEnv 装配子代理 + 角色链最小 base(真实插件,非桩)。
func buildSubAgentRolesEnv(t *testing.T, home string) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", home)
	writeTestSkill(t, filepath.Join(home, "skills"), "skill-a")
	writeTestSkill(t, filepath.Join(home, "skills"), "skill-b")
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "host-skills"},
		{ID: "host-roles"},
		{ID: "host-fanout"},
		{ID: "tool-subagent"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c
}

// probeSubAgent 经 subagent 工具真跑一轮子代理(走完整工具执行管线),回读探针快照。
func probeSubAgent(t *testing.T, c sdk.Ctx, probe *roleProbe, task string) (system, model, toolResult string) {
	t.Helper()
	probe.reset()
	res, err := runSubagent(t, c, `{"action":"delegate","task":"`+task+`"}`)
	if err != nil {
		t.Fatalf("subagent 调用失败: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("subagent 返回错误: %s", res.Error)
	}
	systems, models, tools := probe.snap()
	if len(systems) == 0 {
		t.Fatal("子代理一轮都没发请求(探针未介入)")
	}
	if len(tools) == 0 {
		t.Fatalf("子代理没拿到工具回包(read_skill 未被调用或未回流);系统提示:%.200s", systems[0])
	}
	return systems[0], models[0], tools[0]
}

// TestSubAgentInheritsRoleIdentitySkillsModel 一条用例钉住"角色三件套都进子代理"。
func TestSubAgentInheritsRoleIdentitySkillsModel(t *testing.T) {
	home := t.TempDir()
	c := buildSubAgentRolesEnv(t, home)

	var svc sdk.RoleService
	if err := c.Inject("ctx.roles", &svc); err != nil {
		t.Fatalf("ctx.roles 应由 host-roles 提供: %v", err)
	}
	if _, err := svc.Create(sdk.RoleSpec{
		ID: "finance", Name: "财务", Identity: "你是资深财务分析师。",
		Model: "role-model", Skills: []string{"skill-a"}, SkillsSet: true,
	}, "先确认口径,再给数字。\n"); err != nil {
		t.Fatal(err)
	}
	// 假适配器 + 会话档模型(session-model):让"角色模型 vs 会话模型"可判别
	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		t.Fatal(err)
	}
	probe := &roleProbe{}
	llm.RegisterAdapter(probe)
	llm.SetModel("session-model")

	// ① 基线(未启用角色):同一条件反查 —— 没有身份槽、技能全可见、模型是会话档
	sys, model, toolRes := probeSubAgent(t, c, probe, "基线核对")
	if strings.Contains(sys, "当前角色:") {
		t.Fatalf("基线不该有身份槽: %.300s", sys)
	}
	if strings.Contains(toolRes, "未挂载") {
		t.Fatalf("基线读者共享库全部技能,skill-b 不该被拒: %s", toolRes)
	}
	if model != "session-model" {
		t.Fatalf("基线模型应为会话档 session-model,got %q", model)
	}

	// ② 启用角色:同一条子代理任务,三件套都该跟着走
	if err := svc.Use("finance"); err != nil {
		t.Fatal(err)
	}
	sys, model, toolRes = probeSubAgent(t, c, probe, "角色核对")
	if !strings.Contains(sys, "当前角色:财务(finance)") || !strings.Contains(sys, "你是资深财务分析师。") ||
		!strings.Contains(sys, "先确认口径,再给数字。") {
		t.Fatalf("子代理系统提示缺身份槽(身份句/工作规则): %.600s", sys)
	}
	// 技能可见性:角色只挂 skill-a ⇒ 子代理读 skill-b 必须被拒(与主会话同一份判定)
	if !strings.Contains(toolRes, "未挂载") {
		t.Fatalf("子代理绕过了角色技能可见性:读未挂载技能应当被拒,got %s", toolRes)
	}
	// 已挂载技能仍可读(不是"一律拒"的假通过)
	res, err := toolsOf(t, c).Execute(context.Background(), "read_skill", `{"name":"skill-a"}`)
	if err != nil || res.Error != "" || !strings.Contains(res.Content, "skill-a") {
		t.Fatalf("已挂载技能应可读: err=%v res=%+v", err, res)
	}
	// 模型:角色声明的 role-model(经 llm.Complete 单点注入)
	if model != "role-model" {
		t.Fatalf("子代理模型应为角色声明的 role-model,got %q", model)
	}

	// ③ 切回基线(停用角色)后立刻回退:身份槽与过滤都随 Use 实时变化,不需要重装插件
	if err := svc.Use(""); err != nil {
		t.Fatal(err)
	}
	sys, model, toolRes = probeSubAgent(t, c, probe, "停用核对")
	if strings.Contains(sys, "当前角色:") {
		t.Fatalf("停用角色后子代理不该还有身份槽: %.300s", sys)
	}
	if strings.Contains(toolRes, "未挂载") {
		t.Fatalf("停用角色后 skill-b 应恢复可见: %s", toolRes)
	}
	if model != "session-model" {
		t.Fatalf("停用角色后模型应回到会话档,got %q", model)
	}
}
