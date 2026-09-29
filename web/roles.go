// web 角色面板端点(第七十九批 · 阶段 1b)。
//
// 与 /role 命令同源:都调 ctx.roles(sdk.RoleService),写盘细节在 internal/roles,
// 技能正文在 internal/skills。两条入口、一份校验收敛 —— 面板不是第二套实现。
//
// 未装配 ctx.roles(host-roles 未启用)→ 503,前端据此隐藏「角色」段(不摆空壳)。
package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/internal/skills"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// roleProblems 可选扩展:host-roles 的 Service 提供「上次刷新遇到的坏角色文件」。
// 不塞进 sdk.RoleService:只有 UI 需要它,核心消费方(提示组装)不需要。
type roleProblems interface{ Problems() []roles.Problem }

// roleService 懒解析角色服务(可能为 nil,不报错;调用方自行决定是否 503)。
// 为什么懒解析:ui-web-app 与 host-roles 无拓扑依赖,启动期一次性 Inject 会恒为 nil。
func (s *Server) roleService() sdk.RoleService {
	s.roleMu.Lock()
	cached, sk := s.roles, s.roleSkills
	s.roleMu.Unlock()
	if cached != nil {
		return cached
	}
	if s.ctx == nil {
		return nil
	}
	var rs sdk.RoleService
	if err := s.ctx.Inject("ctx.roles", &rs); err != nil || rs == nil {
		return nil
	}
	if sk == nil {
		_ = s.ctx.Inject("ctx.skills", &sk) // 技能库只读展示用;拿不到不影响角色 CRUD
	}
	s.roleMu.Lock()
	s.roles, s.roleSkills = rs, sk
	s.roleMu.Unlock()
	return rs
}

// roleSvc 取角色服务(缺则 503 并写响应;调用方见 false 直接 return)。
func (s *Server) roleSvc(w http.ResponseWriter) (sdk.RoleService, bool) {
	if rs := s.roleService(); rs != nil {
		return rs, true
	}
	http.Error(w, "角色服务未装配(缺 ctx.roles / host-roles 插件)", http.StatusServiceUnavailable)
	return nil, false
}

// skillsCached 读技能服务缓存(必须经这里读:直读字段会与懒解析的写入竞争 ——
// 面板并发请求在两个 goroutine 里跑时,-race 会指到 s.roleSkills)。
func (s *Server) skillsCached() sdk.SkillsService {
	s.roleMu.Lock()
	defer s.roleMu.Unlock()
	return s.roleSkills
}

// skillsSvc 取技能服务(懒解析;缺则 503)。
func (s *Server) skillsSvc(w http.ResponseWriter) (sdk.SkillsService, bool) {
	s.roleService() // 顺带把技能服务缓存上
	if sk := s.skillsCached(); sk != nil {
		return sk, true
	}
	http.Error(w, "技能服务未装配(缺 ctx.skills / host-skills 插件)", http.StatusServiceUnavailable)
	return nil, false
}

// rolesView GET /api/roles 的响应体。
type rolesView struct {
	Current        string           `json:"current"`
	MaxAgentsBytes int              `json:"max_agents_bytes"`
	Roles          []sdk.RoleSpec   `json:"roles"`
	Library        []sdk.SkillInfo  `json:"library,omitempty"`
	Problems       []map[string]any `json:"problems,omitempty"`
}

// handleRoles GET(列表 + 技能库)/ POST(新建)。
// 列表不带 AGENTS.md 正文(host-roles 的缓存里本来就有,但列表用途用不上,别让面板轮询拖大响应):
// 正文在 GET /api/roles/{id} 单独取。
func (s *Server) handleRoles(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.roleSvc(w)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		s.createRole(w, r, svc)
		return
	}
	v := rolesView{Current: svc.Current(), MaxAgentsBytes: svc.MaxAgentsBytes()}
	for _, spec := range svc.List() {
		spec.AGENTS = ""
		v.Roles = append(v.Roles, spec)
	}
	if v.Roles == nil {
		v.Roles = []sdk.RoleSpec{} // 空列表给空数组(nil 会序列化成 null,前端 .length 崩)
	}
	if sk := s.skillsCached(); sk != nil {
		v.Library = sk.List()
	}
	// 技能服务未装配时库列表留空(前端据 library 缺失只显示挂载文本,不崩)
	if ps, ok := svc.(roleProblems); ok {
		for _, p := range ps.Problems() {
			v.Problems = append(v.Problems, map[string]any{"id": p.ID, "error": p.Err})
		}
	}
	writeJSON(w, http.StatusOK, v)
}

// handleRoleOne GET(详情,含 AGENTS.md 正文)/ PATCH(改定义)/ DELETE(删除)。
func (s *Server) handleRoleOne(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.roleSvc(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "缺角色 ID", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		spec, found := svc.Get(id)
		if !found {
			http.Error(w, "角色不存在: "+id, http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, spec)
	case http.MethodPatch:
		var body struct {
			Name          *string   `json:"name"`
			Description   *string   `json:"description"`
			Identity      *string   `json:"identity"`
			ExcludeGlobal *bool     `json:"exclude_global"`
			SkillsSet     *bool     `json:"skills_set"`
			Skills        *[]string `json:"skills"`
			SkillsInherit *bool     `json:"skills_inherit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "坏请求体", http.StatusBadRequest)
			return
		}
		cur, found := svc.Get(id)
		if !found {
			http.Error(w, "角色不存在: "+id, http.StatusNotFound)
			return
		}
		// 部分更新:只改传了的字段(未传 = 保持现值,避免面板半张表单把其它字段清空)
		if body.Name != nil {
			cur.Name = *body.Name
		}
		if body.Description != nil {
			cur.Description = *body.Description
		}
		if body.Identity != nil {
			cur.Identity = *body.Identity
		}
		if body.ExcludeGlobal != nil {
			cur.ExcludeGlobal = *body.ExcludeGlobal
		}
		if body.SkillsInherit != nil {
			cur.SkillsInherit = *body.SkillsInherit
		}
		if body.SkillsSet != nil {
			cur.SkillsSet = *body.SkillsSet
		}
		if body.Skills != nil {
			cur.Skills = append([]string(nil), (*body.Skills)...)
			cur.SkillsSet = true
		}
		// 清掉派生字段:它们不是 role.yaml 的一部分(写回会污染定义)
		cur.OwnSkills, cur.EffectiveSkills, cur.Seed, cur.AGENTSBytes = nil, nil, false, 0
		cur.AGENTS = ""
		updated, err := svc.Update(id, cur)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		updated.AGENTS = ""
		writeJSON(w, http.StatusOK, updated)
	case http.MethodDelete:
		if err := svc.Delete(id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id})
	default:
		http.Error(w, "方法不支持", http.StatusMethodNotAllowed)
	}
}

// handleRoleAgents PUT /api/roles/{id}/agents {agents} —— 写角色工作规则(超上限显式失败)。
func (s *Server) handleRoleAgents(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.roleSvc(w)
	if !ok {
		return
	}
	var body struct {
		Agents string `json:"agents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if err := svc.SetAgents(r.PathValue("id"), body.Agents); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "bytes": len(body.Agents)})
}

// handleRoleRename POST /api/roles/{id}/rename {id?, name?} —— 改 ID(目录改名)/ 显示名。
func (s *Server) handleRoleRename(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.roleSvc(w)
	if !ok {
		return
	}
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	spec, err := svc.Rename(r.PathValue("id"), body.ID, body.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec.AGENTS = ""
	writeJSON(w, http.StatusOK, spec)
}

// handleRoleUse POST /api/roles/{id}/use {id} —— 切换(空/"none" = 停用)。
// 语义:下一轮系统提示生效,**不换会话**(与 /workspace 的区别见 /role 命令回执)。
func (s *Server) handleRoleUse(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.roleSvc(w)
	if !ok {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // 空 body 合法(用路径里的 id)
	id := body.ID
	if id == "" {
		id = r.PathValue("id")
	}
	if id == "-" { // 前端「停用」用一个不可能撞上真实 ID 的占位符
		id = ""
	}
	if err := svc.Use(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "current": svc.Current()})
}

// createRole POST /api/roles {id, name?, description?, identity?, skills?[], skills_inherit?,
// exclude_global?, agents?} —— 新建(已存在显式失败;agents 缺省给模板)。
func (s *Server) createRole(w http.ResponseWriter, r *http.Request, svc sdk.RoleService) {
	var body struct {
		ID            string   `json:"id"`
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		Identity      string   `json:"identity"`
		ExcludeGlobal bool     `json:"exclude_global"`
		Skills        []string `json:"skills"`
		SkillsSet     bool     `json:"skills_set"`
		SkillsInherit bool     `json:"skills_inherit"`
		Agents        string   `json:"agents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if err := roles.ValidateID(body.ID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	spec := sdk.RoleSpec{
		ID: body.ID, Name: body.Name, Description: body.Description, Identity: body.Identity,
		ExcludeGlobal: body.ExcludeGlobal, Skills: body.Skills, SkillsSet: body.SkillsSet,
		SkillsInherit: body.SkillsInherit,
	}
	created, err := svc.Create(spec, body.Agents)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	created.AGENTS = ""
	writeJSON(w, http.StatusOK, created)
}

// —— 技能(角色面板的「技能挂载/新建技能」) ——

// skillsLib 按 role 参数选根目录(空 = 共享库)。
func skillsLib(role string) (skills.Library, error) {
	if role == "" {
		return skills.Shared(), nil
	}
	if err := roles.ValidateID(role); err != nil {
		return skills.Library{}, err
	}
	return skills.ForRole(role), nil
}

// rescanSkills 重扫技能目录(写入/删除后让索引与提示同步;失败只报错不吞)。
func (s *Server) rescanSkills() error {
	if sk := s.skillsCached(); sk != nil {
		return sk.Rescan()
	}
	return nil
}

// handleSkills POST /api/skills —— 新建/覆盖一个 SKILL.md(共享库或角色私有)。
//
// 两种载荷:① 表单式 {name, description, triggers[], body} → 服务端拼 frontmatter;
// ② 原文式 {content} → 原样写入(面板编辑既有技能时用,不丢用户自定义 frontmatter 键)。
func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.skillsSvc(w); !ok {
		return
	}
	var body struct {
		Role        string   `json:"role"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Triggers    []string `json:"triggers"`
		Body        string   `json:"body"`
		Content     string   `json:"content"`
		Overwrite   bool     `json:"overwrite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	lib, err := skillsLib(body.Role)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	content := body.Content
	if strings.TrimSpace(content) == "" {
		content = skills.Content(body.Name, body.Description, body.Triggers, body.Body)
	}
	if err := lib.Write(body.Name, content, body.Overwrite); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.rescanSkills(); err != nil {
		// 写成功了但索引没刷新:如实说明(否则用户会看到"新建了但不生效"的悬案)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": body.Name, "path": lib.Path(body.Name),
			"warning": "技能已写入,但重扫失败:" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": body.Name, "path": lib.Path(body.Name)})
}

// handleSkillRelocate POST /api/skills/{name}/relocate?role=<源库> {to_name, to_role}
// —— 技能改名 / 跨库移动(共享库 ↔ 角色私有;一次可两件都做,to_name 空 = 不改名)。
// 为什么不复用 POST /api/skills(覆盖写):这里改的是**身份**(目录名/归属)而不是正文,
// 而且必须顺带把引用它的角色挂载清单一起改 —— 覆盖写做不到这件事:
// 挂载是按**名字**存的,只改目录名会让每个挂载它的角色悄悹多出一条"已失效挂载"(技能从该角色消失)。
func (s *Server) handleSkillRelocate(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.skillsSvc(w); !ok {
		return
	}
	name := r.PathValue("name")
	fromRole := r.URL.Query().Get("role")
	var body struct {
		ToName string `json:"to_name"`
		ToRole string `json:"to_role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	src, err := skillsLib(fromRole)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dst, err := skillsLib(body.ToRole)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	got, err := src.Relocate(name, dst, strings.TrimSpace(body.ToName))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := map[string]any{"ok": true, "name": got, "role": body.ToRole, "from_name": name, "from_role": fromRole}
	var warns []string
	if got != name { // 只有改名才需追着改引用;纯移动按名字挂载依然成立
		touched, err := roles.Store{}.RewriteMount(name, got)
		if err != nil {
			warns = append(warns, "技能已改名,但角色挂载改写失败(可能有角色残留失效挂载):"+err.Error())
		}
		if len(touched) > 0 {
			resp["mounts_updated"] = touched
		}
	}
	if warn := s.reloadRoleSkillCaches(); warn != "" {
		warns = append(warns, "技能已归位,但"+warn)
	}
	if len(warns) > 0 {
		resp["warning"] = strings.Join(warns, "; ")
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSkillOne GET /api/skills/{name}?role= (原文,供编辑)/ DELETE(移入回收站)。
func (s *Server) handleSkillOne(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.skillsSvc(w); !ok {
		return
	}
	name := r.PathValue("name")
	lib, err := skillsLib(r.URL.Query().Get("role"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		raw, err := lib.Read(name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"name": name, "content": raw, "path": lib.Path(name)})
	case http.MethodDelete:
		if err := lib.Remove(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.rescanSkills(); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true,
				"warning": "技能已删除,但重扫失败:" + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": name})
	default:
		http.Error(w, "方法不支持", http.StatusMethodNotAllowed)
	}
}
