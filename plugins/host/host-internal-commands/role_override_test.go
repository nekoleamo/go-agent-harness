// /model、/thinking 与「角色已声明模型/思考档」的交互(第八十六批)。
//
// 为何要给这两条命令加提示:会话档被角色每回合覆盖时,命令看起来"执行成功了但没生效" ——
// 静默失效比报错更难查。这里钉的是:会话档**照旧被改**(不停用、不报错),只是如实说明覆盖关系。
package hostintcmd

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubRoles 只要 Current/Get 的角色服务桩(其余方法由内嵌接口兜底:本测试不走那些路径)。
type stubRoles struct {
	sdk.RoleService
	spec sdk.RoleSpec
}

func (s *stubRoles) Current() string { return s.spec.ID }
func (s *stubRoles) Get(id string) (sdk.RoleSpec, bool) {
	if id != s.spec.ID {
		return sdk.RoleSpec{}, false
	}
	return s.spec, true
}

// roleLLM = stubMultiLLM(/model 需要多 provider 能力)+ 可观测的 SetThinking。
type roleLLM struct {
	*stubMultiLLM
	thinking string
}

func (s *roleLLM) SetThinking(l sdk.ThinkingLevel) { s.thinking = l.String() }

func TestModelAndThinkingNoticeRoleOverride(t *testing.T) {
	home := providerHome(t)
	c, cmds := buildEnv(t)
	ms := &roleLLM{stubMultiLLM: &stubMultiLLM{}}
	if err := c.Provide("ctx.llm", sdk.LLMService(ms)); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.roles", sdk.RoleService(&stubRoles{
		spec: sdk.RoleSpec{ID: "finance", Name: "财务", Model: "claude-sonnet-4-5", Thinking: "off"},
	})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)

	// 思考档:会话值照旧改变并落偏好,输出额外说明被角色覆盖(不是错误)
	out, err := run(t, cmds, "thinking", "high")
	if err != nil {
		t.Fatalf("被角色覆盖不该是错误: %v", err)
	}
	if ms.thinking != "high" {
		t.Fatalf("会话思考档应照旧写入: %q", ms.thinking)
	}
	if !strings.Contains(out, "off") || !strings.Contains(out, "角色") {
		t.Fatalf("未说明角色覆盖: %q", out)
	}
	if p := prefs.Load(); p.Thinking != "high" {
		t.Fatalf("偏好应照旧持久化: %+v", p)
	}

	// 模型:同样只是加一句说明
	if _, err := run(t, cmds, "provider", "add", "https://api.siliconflow.cn/v1", "sk-aaaabbbb"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, cmds, "model", "deepseek-chat")
	if err != nil {
		t.Fatalf("被角色覆盖不该是错误: %v", err)
	}
	if ms.curModel != "deepseek-chat" {
		t.Fatalf("会话模型应照旧切换: %q", ms.curModel)
	}
	if !strings.Contains(out, "claude-sonnet-4-5") || !strings.Contains(out, "角色") {
		t.Fatalf("未说明角色覆盖: %q", out)
	}
	_ = home
}

// 未装配 ctx.roles / 未启用角色:输出与本功能上线前逐字一致(不加任何提示)。
func TestModelAndThinkingNoNoticeWithoutRole(t *testing.T) {
	home := providerHome(t)
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.llm", sdk.LLMService(&stubMultiLLM{})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "thinking", "low")
	if err != nil {
		t.Fatal(err)
	}
	if out != "思考等级 -> low" {
		t.Fatalf("无角色时输出应逐字不变: %q", out)
	}
	_ = home
}
