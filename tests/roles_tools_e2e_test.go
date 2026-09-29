// 角色工具集(排除清单)端到端(第九十一批):一个角色能"收敛工具面"。
//
// 为什么必须有端到端用例:过滤缝装在 host-tools 的 List/Execute 上,而工具的消费面有五个
// (主循环工具表 / 子代理 / 工作流 / 外部插件回调 / 对外 MCP server)。单测只证明"这个方法
// 过滤了",证明不了"模型真的看不见、子代理真的调不动" —— 而后者才是这项能力的全部意义。
//
// 本文件钉住两条消费面 + 一条对照:
//   - 主会话:角色生效时 `tools.List()` 不含被排除的工具,`Execute` 给"未授权"而非"不存在";
//   - 子代理:系统提示里的「可用工具」清单不含它(走的是同一个 Registry 的 List),
//     且真叫一次会被拒(拒绝文案与"不存在"区分开);
//   - 停用角色后全部回退(证明是"现算判定",不是装过滤器时装死的)。
package tests

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestRoleToolsExcludeE2E 角色排除清单在主会话与子代理两条路径上都生效。
func TestRoleToolsExcludeE2E(t *testing.T) {
	home := t.TempDir()
	c := buildSubAgentRolesEnv(t, home) // 复用第九十批的装配(真实六插件 + 两个共享技能)

	var svc sdk.RoleService
	if err := c.Inject("ctx.roles", &svc); err != nil {
		t.Fatalf("ctx.roles 应由 host-roles 提供: %v", err)
	}
	if _, err := svc.Create(sdk.RoleSpec{
		ID: "finance", Name: "财务", Identity: "你是资深财务分析师。",
		ToolsExclude: []string{"read_skill"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		t.Fatal(err)
	}
	probe := &roleProbe{}
	llm.RegisterAdapter(probe)
	// 子代理自己拼请求(不带模型)⇒ 必须有一个会话档模型可回落(角色未声明 model 时)
	llm.SetModel("session-model")

	hasTool := func(name string) bool {
		for _, d := range toolsOf(t, c).List() {
			if d.Name == name {
				return true
			}
		}
		return false
	}

	// ① 基线:工具面全量(角色是"视角",不是默认闸门)
	if !hasTool("read_skill") {
		t.Fatalf("基线应可见全部工具: %v", toolNameList(t, c))
	}

	// ② 启用角色:主会话工具表少一个,其余不受影响
	if err := svc.Use("finance"); err != nil {
		t.Fatal(err)
	}
	if hasTool("read_skill") {
		t.Fatalf("角色生效后被排除的工具不该可见: %v", toolNameList(t, c))
	}
	if !hasTool("list_roles") {
		t.Fatalf("未排除的工具应保留: %v", toolNameList(t, c))
	}
	// 凭记忆调用:文案必须与"不存在"区分(否则用户会去查插件安装)
	res, err := toolsOf(t, c).Execute(context.Background(), "read_skill", `{"name":"skill-a"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Error, "排除") || strings.Contains(res.Error, "不存在") {
		t.Fatalf("被排除的工具应给未授权文案: %q", res.Error)
	}
	// 不是"一律拒"的假通过:同样条件下别的工具照常可用
	if ok, _ := toolsOf(t, c).Execute(context.Background(), "list_roles", "{}"); ok.Error != "" {
		t.Fatalf("未排除的工具应可执行: %q", ok.Error)
	}

	// ③ 子代理:同一份判定函数 ⇒ 系统提示的「可用工具」不含它、真调用被拒
	sys, _, toolRes := probeSubAgent(t, c, probe, "角色工具核对")
	sec := toolSection(sys)
	if sec == "" {
		t.Fatalf("子代理系统提示里找不到「可用工具」段(判据失效,不是功能问题): %.600s", sys)
	}
	if strings.Contains(sec, "read_skill") {
		t.Fatalf("子代理系统提示不该列出被角色排除的工具: %s", sec)
	}
	if !strings.Contains(sec, "list_roles") {
		t.Fatalf("子代理系统提示应列出其余工具: %s", sec)
	}
	if !strings.Contains(toolRes, "排除") {
		t.Fatalf("子代理调用被排除的工具应被拒: %s", toolRes)
	}

	// ④ 停用角色:立即回退(现算判定,不需要重装插件)
	if err := svc.Use(""); err != nil {
		t.Fatal(err)
	}
	if !hasTool("read_skill") {
		t.Fatalf("停用角色后应恢复全部工具: %v", toolNameList(t, c))
	}
	sys, _, toolRes = probeSubAgent(t, c, probe, "停用工具核对")
	if !strings.Contains(toolSection(sys), "read_skill") {
		t.Fatalf("停用后子代理系统提示应重新列出该工具: %.600s", toolSection(sys))
	}
	if strings.Contains(toolRes, "排除") {
		t.Fatalf("停用后子代理应能读该技能: %s", toolRes)
	}
}

// toolSection 从系统提示里取出「可用工具:」那一段(名字以「、」分隔)。
// **不能对整个提示做 Contains 判断**:host-skills 的索引片段正文里就写着"先行调用
// read_skill 读取全文再执行" —— 拿全文判 read_skill 会永远为真(实测踩到,断言假通过)。
func toolSection(sys string) string {
	const marker = "可用工具:"
	i := strings.Index(sys, marker)
	if i < 0 {
		return ""
	}
	rest := sys[i+len(marker):]
	if j := strings.Index(rest, "(完整定义"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// toolNameList 工具名列表(断言失败时打出来便于定位)。
func toolNameList(t *testing.T, c sdk.Ctx) []string {
	t.Helper()
	var out []string
	for _, d := range toolsOf(t, c).List() {
		out = append(out, d.Name)
	}
	return out
}
