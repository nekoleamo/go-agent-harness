// 角色面板端点单测(第七十九批 1b)。
//
// 装配**真实** host-tools/host-system-prompt/host-skills/host-roles 到 Server.ctx,
// 走懒解析路径(与生产同一条):不写服务替身 —— 替身会与真实校验/落盘行为漂移,
// 而这里的判据正是"面板改的东西是否真落到了角色定义里"。
package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-roles"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-skills"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// rolesServer 起一个带真实角色链的 Server(未装配角色链的用法见 TestRolesEndpointUnavailable)。
func rolesServer(t *testing.T, withChain bool) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	s, _ := newTestServer()
	if !withChain {
		return s, home
	}
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	for _, p := range []sdk.Plugin{&hosttools.Plugin{}, &hostsystemprompt.Plugin{}, &hostskills.Plugin{}, &hostroles.Plugin{}} {
		if _, err := p.Start(c, &sdk.Manifest{}); err != nil {
			t.Fatalf("%s Start: %v", p.Name(), err)
		}
	}
	s.ctx = c
	// 工具注册表也挂到 Server(生产走 Server.Inject 同一条链):/api/tools 与
	// 角色工具排除的可见性断言都要它(第九十一批)。
	var tl sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tl); err == nil {
		s.tools = tl
	}
	return s, home
}

// do 发一次请求并返回状态码与响应体。
func do(t *testing.T, s *Server, method, path, body string) (int, string) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func TestRolesEndpointUnavailable(t *testing.T) {
	s, _ := rolesServer(t, false)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/roles", ""},
		{http.MethodPost, "/api/roles", `{"id":"x"}`},
		{http.MethodPost, "/api/skills", `{"name":"x"}`},
	} {
		code, body := do(t, s, tc.method, tc.path, tc.body)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: 未装配应 503,got %d %s", tc.method, tc.path, code, body)
		}
	}
	// 状态栏徽标:未装配时不谎报角色
	code, body := do(t, s, http.MethodGet, "/api/state", "")
	if code != 200 || strings.Contains(body, `"role"`) {
		t.Fatalf("未装配角色链时 state 不应带 role 字段: %d %s", code, body)
	}
}

func TestRolesCRUDAndState(t *testing.T) {
	s, home := rolesServer(t, true)

	// 空列表:契约给空数组(不是 null)
	code, body := do(t, s, http.MethodGet, "/api/roles", "")
	if code != 200 {
		t.Fatalf("GET /api/roles = %d %s", code, body)
	}
	var list struct {
		Current        string            `json:"current"`
		MaxAgentsBytes int               `json:"max_agents_bytes"`
		Roles          []json.RawMessage `json:"roles"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if list.Roles == nil || len(list.Roles) != 0 || list.Current != "" {
		t.Fatalf("空库契约不符: %s", body)
	}
	if list.MaxAgentsBytes != 32*1024 {
		t.Fatalf("max_agents_bytes 应下发上限: %d", list.MaxAgentsBytes)
	}

	// 新建:非法 ID → 400;合法 → 200 且落盘
	if code, _ := do(t, s, http.MethodPost, "/api/roles", `{"id":"Bad ID"}`); code != 400 {
		t.Fatalf("非法 ID 应 400,got %d", code)
	}
	code, body = do(t, s, http.MethodPost, "/api/roles",
		`{"id":"finance","name":"财务","identity":"你是资深财务分析师。","description":"记账与报表","exclude_global":true}`)
	if code != 200 {
		t.Fatalf("新建 = %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(home, "roles", "finance", "role.yaml")); err != nil {
		t.Fatalf("role.yaml 未落盘: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "roles", "finance", "AGENTS.md")); err != nil {
		t.Fatalf("缺省应给 AGENTS.md 模板: %v", err)
	}
	// 重复 ID → 400(显式失败,不覆盖)
	if code, _ := do(t, s, http.MethodPost, "/api/roles", `{"id":"finance"}`); code != 400 {
		t.Fatalf("重复 ID 应 400,got %d", code)
	}

	// 详情(带正文)+ 404
	code, body = do(t, s, http.MethodGet, "/api/roles/finance", "")
	if code != 200 || !strings.Contains(body, "你是资深财务分析师。") || !strings.Contains(body, `"exclude_global":true`) {
		t.Fatalf("详情不符: %d %s", code, body)
	}
	if code, _ := do(t, s, http.MethodGet, "/api/roles/nope", ""); code != 404 {
		t.Fatalf("不存在应 404,got %d", code)
	}

	// 切换:回执带 current;state 随之下发角色(状态栏徽标数据源)
	code, body = do(t, s, http.MethodPost, "/api/roles/finance/use", "{}")
	if code != 200 || !strings.Contains(body, `"current":"finance"`) {
		t.Fatalf("切换失败: %d %s", code, body)
	}
	code, body = do(t, s, http.MethodGet, "/api/state", "")
	if code != 200 || !strings.Contains(body, `"role":"finance"`) || !strings.Contains(body, `"role_name":"财务"`) {
		t.Fatalf("state 未带当前角色: %d %s", code, body)
	}
	// 切到不存在的角色 → 400,且当前角色不变
	if code, _ := do(t, s, http.MethodPost, "/api/roles/ghost/use", "{}"); code != 400 {
		t.Fatalf("切到不存在角色应 400,got %d", code)
	}
	if _, body := do(t, s, http.MethodGet, "/api/state", ""); !strings.Contains(body, `"role":"finance"`) {
		t.Fatalf("失败的切换不应改动当前角色: %s", body)
	}

	// PATCH:部分更新(只改传了的字段,未传的保持)
	code, body = do(t, s, http.MethodPatch, "/api/roles/finance", `{"identity":"改过的身份句"}`)
	if code != 200 {
		t.Fatalf("PATCH = %d %s", code, body)
	}
	raw, err := os.ReadFile(filepath.Join(home, "roles", "finance", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "改过的身份句") || !strings.Contains(string(raw), "exclude_global: true") {
		t.Fatalf("PATCH 应只改目标字段(未传字段保持):\n%s", raw)
	}
	if strings.Contains(string(raw), "effective_skills") || strings.Contains(string(raw), "agents_bytes") {
		t.Fatalf("派生字段不应写回定义:\n%s", raw)
	}

	// PATCH 技能挂载(替换语义)+ 非法技能名拒绝
	// 先放一个技能进共享库:挂载校验是"必须引用已加载的技能"(写错一个名字不该静默少挂)
	if code, body := do(t, s, http.MethodPost, "/api/skills", `{"name":"skill-a","description":"技能A","body":"正文"}`); code != 200 {
		t.Fatalf("准备技能失败: %d %s", code, body)
	}
	code, body = do(t, s, http.MethodPatch, "/api/roles/finance", `{"skills_set":true,"skills":["skill-a"]}`)
	if code != 200 || !strings.Contains(body, `"skills":["skill-a"]`) {
		t.Fatalf("挂载技能失败: %d %s", code, body)
	}
	if code, _ := do(t, s, http.MethodPatch, "/api/roles/finance", `{"skills":["不存在技能"]}`); code != 400 {
		t.Fatalf("挂载不存在的技能应 400,got %d", code)
	}

	// 写 AGENTS.md:成功 + 超限拒绝
	code, body = do(t, s, http.MethodPut, "/api/roles/finance/agents", `{"agents":"先确认口径,再给数字。"}`)
	if code != 200 {
		t.Fatalf("写 AGENTS.md = %d %s", code, body)
	}
	if got, _ := os.ReadFile(filepath.Join(home, "roles", "finance", "AGENTS.md")); !strings.Contains(string(got), "先确认口径") {
		t.Fatalf("AGENTS.md 未写入: %s", got)
	}
	huge := `{"agents":"` + strings.Repeat("x", 32*1024+1) + `"}`
	if code, _ := do(t, s, http.MethodPut, "/api/roles/finance/agents", huge); code != 400 {
		t.Fatalf("超限应 400,got %d", code)
	}

	// 重命名(改 ID + 显示名);目标是当前角色 → 偏好跟随
	code, body = do(t, s, http.MethodPost, "/api/roles/finance/rename", `{"id":"finance-cn","name":"财务(中文)"}`)
	if code != 200 || !strings.Contains(body, `"id":"finance-cn"`) {
		t.Fatalf("重命名失败: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(home, "roles", "finance")); !os.IsNotExist(err) {
		t.Fatal("旧目录应已改名")
	}
	if _, body := do(t, s, http.MethodGet, "/api/state", ""); !strings.Contains(body, `"role":"finance-cn"`) {
		t.Fatalf("当前角色应跟随改名: %s", body)
	}
	// 目标已存在 → 400
	if code, _ := do(t, s, http.MethodPost, "/api/roles", `{"id":"other"}`); code != 200 {
		t.Fatalf("建 other 失败")
	}
	if code, _ := do(t, s, http.MethodPost, "/api/roles/finance-cn/rename", `{"id":"other"}`); code != 400 {
		t.Fatalf("改到已存在 ID 应 400,got %d", code)
	}

	// 删除:当前角色拒绝;非当前角色 → 进回收站
	if code, body := do(t, s, http.MethodDelete, "/api/roles/finance-cn", ""); code != 400 || !strings.Contains(body, "正在使用中") {
		t.Fatalf("删当前角色应 400 且说明原因: %d %s", code, body)
	}
	code, body = do(t, s, http.MethodDelete, "/api/roles/other", "")
	if code != 200 {
		t.Fatalf("删非当前角色 = %d %s", code, body)
	}
	if entries, err := os.ReadDir(filepath.Join(home, "roles", ".trash")); err != nil || len(entries) != 1 {
		t.Fatalf("删除应进回收站: %v %v", entries, err)
	}
}

func TestSkillsEndpoints(t *testing.T) {
	s, home := rolesServer(t, true)

	// 新建共享技能(表单式 → 服务端拼 frontmatter)
	code, body := do(t, s, http.MethodPost, "/api/skills",
		`{"name":"review","description":"代码审查","triggers":["CR"],"body":"# 步骤\n1. 读 diff"}`)
	if code != 200 {
		t.Fatalf("新建技能 = %d %s", code, body)
	}
	p := filepath.Join(home, "skills", "review", "SKILL.md")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("技能未落盘: %v", err)
	}
	if !strings.Contains(string(raw), "name: review") || !strings.Contains(string(raw), "trigger:") {
		t.Fatalf("frontmatter 不符:\n%s", raw)
	}
	// 重名 + 不覆盖 → 400;覆盖 → 200
	if code, _ := do(t, s, http.MethodPost, "/api/skills", `{"name":"review","body":"x"}`); code != 400 {
		t.Fatalf("重名应 400,got %d", code)
	}
	// 原文式覆盖:frontmatter 名与目录名不一致 → 400(否则扫描名与目录名两个口径打架)
	if code, _ := do(t, s, http.MethodPost, "/api/skills", `{"name":"review","content":"---\nname: other\n---\nb","overwrite":true}`); code != 400 {
		t.Fatalf("frontmatter 名不一致应 400,got %d", code)
	}
	// 表单式覆盖(名以请求为准)→ 200
	if code, _ := do(t, s, http.MethodPost, "/api/skills", `{"name":"review","body":"改过","overwrite":true}`); code != 200 {
		t.Fatalf("表单式覆盖应 200,got %d", code)
	}
	// 原文式写入(编辑既有技能):名字一致才允许
	code, body = do(t, s, http.MethodPost, "/api/skills",
		`{"name":"review","content":"---\nname: review\ndescription: 改过\n---\n新正文","overwrite":true}`)
	if code != 200 {
		t.Fatalf("原文式覆盖 = %d %s", code, body)
	}
	// GET 原文(供编辑器)
	code, body = do(t, s, http.MethodGet, "/api/skills/review", "")
	if code != 200 || !strings.Contains(body, "新正文") {
		t.Fatalf("读原文 = %d %s", code, body)
	}
	// 角色私有技能:落角色目录,不进共享库(角色必须先存在 —— 否则会凭空造出“坏角色”目录)
	if code, body := do(t, s, http.MethodPost, "/api/roles", `{"id":"finance"}`); code != 200 {
		t.Fatalf("建角色 = %d %s", code, body)
	}
	code, body = do(t, s, http.MethodPost, "/api/skills",
		`{"role":"finance","name":"tax","description":"税务","body":"口径"}`)
	if code != 200 {
		t.Fatalf("角色私有技能 = %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(home, "roles", "finance", "skills", "tax", "SKILL.md")); err != nil {
		t.Fatalf("角色私有技能未落角色目录: %v", err)
	}
	// 不存在的角色 → 显式拒(否则会凭空造出缺 role.yaml 的“坏角色”目录,该 ID 之后无法新建角色)
	if code, _ := do(t, s, http.MethodPost, "/api/skills", `{"role":"ghost","name":"x","body":"b"}`); code != 400 {
		t.Fatalf("未知角色的私有技能应 400,got %d", code)
	}
	if _, err := os.Stat(filepath.Join(home, "roles", "ghost")); err == nil {
		t.Fatal("不应为未知角色造出目录")
	}
	// 非法名/非法角色 ID 拒绝
	if code, _ := do(t, s, http.MethodPost, "/api/skills", `{"name":"../escape"}`); code != 400 {
		t.Fatalf("路径穿越名应 400,got %d", code)
	}
	if code, _ := do(t, s, http.MethodPost, "/api/skills", `{"role":"../x","name":"y"}`); code != 400 {
		t.Fatalf("非法角色 ID 应 400,got %d", code)
	}
	// 删除 → 回收站 + 库列表里消失;重复删除 → 400
	code, body = do(t, s, http.MethodDelete, "/api/skills/review", "")
	if code != 200 {
		t.Fatalf("删除技能 = %d %s", code, body)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("技能应已移出库")
	}
	if _, err := os.Stat(filepath.Join(home, "skills", ".trash")); err != nil {
		t.Fatalf("技能应进回收站: %v", err)
	}
	if code, _ := do(t, s, http.MethodDelete, "/api/skills/review", ""); code != 400 {
		t.Fatalf("重复删除应 400,got %d", code)
	}
	// 回收站里的技能不再被索引(重扫后库列表不含它)
	code, body = do(t, s, http.MethodGet, "/api/roles", "")
	if code != 200 || strings.Contains(body, `"name":"review"`) {
		t.Fatalf("回收站技能不应仍在索引里: %s", body)
	}
	if !strings.Contains(body, `"name":"tax"`) || !strings.Contains(body, `"role":"finance"`) {
		t.Fatalf("库列表应含角色私有技能并标归属: %s", body)
	}
}

// TestRoleToolsExcludeEndpoints 角色工具排除清单的面板端点(第九十一批):
// PATCH 落盘 + 详情回显 + 坏形状 400 + **模型可见面确实变了**(过滤不是只写在 role.yaml 里)。
func TestRoleToolsExcludeEndpoints(t *testing.T) {
	s, home := rolesServer(t, true)
	if code, body := do(t, s, http.MethodPost, "/api/roles", `{"id":"finance","name":"财务"}`); code != 200 {
		t.Fatalf("建角色失败: %d %s", code, body)
	}
	// 坏形状(空项/空白)→ 400(不静默丢弃用户写的条目)
	if code, _ := do(t, s, http.MethodPatch, "/api/roles/finance", `{"tools_exclude":[""]}`); code != 400 {
		t.Fatalf("空工具名应 400,got %d", code)
	}
	if code, _ := do(t, s, http.MethodPatch, "/api/roles/finance", `{"tools_exclude":["a b"]}`); code != 400 {
		t.Fatalf("含空白工具名应 400,got %d", code)
	}
	// 正常:落盘 + 回显
	code, body := do(t, s, http.MethodPatch, "/api/roles/finance", `{"tools_exclude":["read_skill"," list_roles "]}`)
	if code != 200 || !strings.Contains(body, `"tools_exclude":["read_skill","list_roles"]`) {
		t.Fatalf("PATCH tools_exclude 失败: %d %s", code, body)
	}
	raw, err := os.ReadFile(filepath.Join(home, "roles", "finance", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "tools_exclude") || !strings.Contains(string(raw), "- read_skill") {
		t.Fatalf("排除清单未落盘:\n%s", raw)
	}
	// 详情回显(面板据此渲染"已排除 N 项")
	if code, body := do(t, s, http.MethodGet, "/api/roles/finance", ""); code != 200 || !strings.Contains(body, `"tools_exclude":["read_skill","list_roles"]`) {
		t.Fatalf("详情未回显排除清单: %d %s", code, body)
	}
	// 角色生效后**模型可见面**确实少了(端到端:面板改的东西真的作用于工具表)
	if code, _ := do(t, s, http.MethodPost, "/api/roles/finance/use", "{}"); code != 200 {
		t.Fatalf("切换失败: %d", code)
	}
	code, body = do(t, s, http.MethodGet, "/api/tools", "")
	if code != 200 {
		t.Fatalf("GET /api/tools = %d %s", code, body)
	}
	if strings.Contains(body, `"read_skill"`) {
		t.Fatalf("被排除的工具不该出现在(模型可见的)/api/tools 里: %s", body)
	}
	if !strings.Contains(body, `"list_roles"`) && !strings.Contains(body, `"read_role"`) {
		t.Logf("提示:本次装配未注册角色只读工具,跳过可见性断言:%s", body)
	}
	// 管理面(?all=1)必须**仍列出**被排除的工具 —— 否则面板分不清"被角色排除"与"没装插件"
	code, body = do(t, s, http.MethodGet, "/api/tools?all=1", "")
	if code != 200 {
		t.Fatalf("GET /api/tools?all=1 = %d %s", code, body)
	}
	if !strings.Contains(body, `"read_skill"`) {
		t.Fatalf("管理面应看到全部已注册工具: %s", body)
	}
	// 清空(面板「全部恢复」)
	code, body = do(t, s, http.MethodPatch, "/api/roles/finance", `{"tools_exclude":[]}`)
	if code != 200 || strings.Contains(body, `"tools_exclude":["`) {
		t.Fatalf("清空排除清单失败: %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodGet, "/api/tools", ""); code != 200 || !strings.Contains(body, `"read_skill"`) {
		t.Fatalf("清空后工具应回来: %d %s", code, body)
	}
}

// TestRoleTierEndpoints 角色收紧档的 Web 面(第九十二批):
// 「试图放宽」= 400(不是静默忽略、不是静默存成最严);合法值落盘 + 回显;
// 新建即可带(与 PATCH 同一套校验)。
func TestRoleTierEndpoints(t *testing.T) {
	s, home := rolesServer(t, true)
	if code, body := do(t, s, http.MethodPost, "/api/roles", `{"id":"audit","name":"审计"}`); code != 200 {
		t.Fatalf("建角色失败: %d %s", code, body)
	}
	// 放宽(open / full-access)一律 400:角色无权放宽全局档
	for _, body := range []string{`{"approval":"open"}`, `{"sandbox":"full-access"}`} {
		code, resp := do(t, s, http.MethodPatch, "/api/roles/audit", body)
		if code != 400 {
			t.Fatalf("放宽档应 400,body=%s got %d %s", body, code, resp)
		}
		if !strings.Contains(resp, "收紧") {
			t.Errorf("400 的正文要说清\"只能收紧\": %s", resp)
		}
	}
	// 非法值同样 400(不静默当"未声明")
	for _, body := range []string{`{"approval":"extreme"}`, `{"sandbox":"ro"}`} {
		if code, _ := do(t, s, http.MethodPatch, "/api/roles/audit", body); code != 400 {
			t.Fatalf("非法档应 400,body=%s got %d", body, code)
		}
	}
	// 合法收紧:落盘 + 回显(带空白也归一)
	code, resp := do(t, s, http.MethodPatch, "/api/roles/audit", `{"approval":" STRICT ","sandbox":"read-only"}`)
	if code != 200 || !strings.Contains(resp, `"approval":"strict"`) || !strings.Contains(resp, `"sandbox":"read-only"`) {
		t.Fatalf("PATCH 收紧档失败: %d %s", code, resp)
	}
	raw, err := os.ReadFile(filepath.Join(home, "roles", "audit", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "approval: strict") || !strings.Contains(string(raw), "sandbox: read-only") {
		t.Fatalf("收紧档未落盘:\n%s", raw)
	}
	if code, body := do(t, s, http.MethodGet, "/api/roles/audit", ""); code != 200 || !strings.Contains(body, `"approval":"strict"`) {
		t.Fatalf("详情未回显收紧档: %d %s", code, body)
	}
	// 清掉 = 跟随全局(空串合法)
	code, resp = do(t, s, http.MethodPatch, "/api/roles/audit", `{"approval":"","sandbox":""}`)
	if code != 200 || strings.Contains(resp, `"approval":"`) || strings.Contains(resp, `"sandbox":"`) {
		t.Fatalf("清空收紧档失败: %d %s", code, resp)
	}
	// 新建时即可带(同一套校验;非法值当场拒)
	if code, _ := do(t, s, http.MethodPost, "/api/roles", `{"id":"bad","approval":"open"}`); code != 400 {
		t.Fatalf("新建带放宽档应 400,got %d", code)
	}
	code, resp = do(t, s, http.MethodPost, "/api/roles", `{"id":"guard","approval":"strict","sandbox":"read-only"}`)
	if code != 200 || !strings.Contains(resp, `"approval":"strict"`) {
		t.Fatalf("新建带收紧档失败: %d %s", code, resp)
	}
}
