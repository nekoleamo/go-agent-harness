// web 回收站端点(第八十三批):删除的角色/技能移进 .trash 后**可恢复**的入口。
//
// 为什么单开两条路由而不是塞进 /api/roles/{id}、/api/skills/{name} 下:
// 那两条是通配路径,技能名恰好叫 "trash"(合法名字)时会被路由吃掉。这里走独立前缀,
// 与写盘细节(internal/roles.Store / internal/skills.Library)同一份校验,面板不是第二套实现。
//
// 列表**不 503**:角色服务未装配时角色段为空数组,技能段照常(共享库 + 各角色私有库)。
// 恢复按 kind 分派;恢复后必须让缓存跟上(角色 Reload 连带技能重扫),重载失败沿
// 既有 200 + warning 口径如实回 —— 不谎报"已生效"。
package web

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/internal/skills"
)

// trashSkillEntry 一条技能回收站记录(库归属 + skills.TrashEntry 平铺)。
type trashSkillEntry struct {
	skills.TrashEntry
	Role string `json:"role"` // "" = 共享技能库
}

// trashView GET /api/trash 的响应体。
type trashView struct {
	Roles  []roles.TrashEntry `json:"roles"`
	Skills []trashSkillEntry  `json:"skills"`
}

// handleTrash GET /api/trash —— 角色回收站 + 技能回收站(共享库与每个角色私有库)。
func (s *Server) handleTrash(w http.ResponseWriter, r *http.Request) {
	store := roles.Store{}
	v := trashView{Roles: store.TrashList(), Skills: []trashSkillEntry{}}
	if v.Roles == nil {
		v.Roles = []roles.TrashEntry{} // nil 会序列化成 null,前端 .length 崩
	}
	// 共享库 + 每个角色目录的私有技能回收站;枚举目录用 IDs(坏角色的目录也不漏)。
	for _, e := range skills.Shared().TrashList() {
		v.Skills = append(v.Skills, trashSkillEntry{TrashEntry: e})
	}
	for _, id := range store.IDs() {
		for _, e := range skills.ForRole(id).TrashList() {
			v.Skills = append(v.Skills, trashSkillEntry{TrashEntry: e, Role: id})
		}
	}
	// 跨库按删除时间倒序(各库内部已倒序,这里合并——同一库内尾部时间戳定序)。
	sort.SliceStable(v.Skills, func(i, j int) bool { return v.Skills[i].DeletedAt > v.Skills[j].DeletedAt })
	writeJSON(w, http.StatusOK, v)
}

// handleTrashRestore POST /api/trash/restore {kind:"role"|"skill", name, role?}。
// name 是**回收站目录名**(<名>-<时间戳>),不是原 ID/技能名 —— 同一角色可能有多份历史快照。
func (s *Server) handleTrashRestore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	switch body.Kind {
	case "role":
		id, err := roles.Store{}.Restore(body.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := map[string]any{"ok": true, "kind": "role", "id": id}
		if warn := s.reloadRoleSkillCaches(); warn != "" {
			resp["warning"] = "已恢复,但" + warn
		}
		writeJSON(w, http.StatusOK, resp)
	case "skill":
		lib, err := skillsLib(body.Role)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		name, err := lib.Restore(body.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := map[string]any{"ok": true, "kind": "skill", "name": name, "role": body.Role}
		if warn := s.reloadRoleSkillCaches(); warn != "" {
			resp["warning"] = "已恢复,但" + warn
		}
		writeJSON(w, http.StatusOK, resp)
	default:
		http.Error(w, `kind 只支持 "role" 或 "skill"`, http.StatusBadRequest)
	}
}

// reloadRoleSkillCaches 让角色缓存与技能索引跟上写盘结果;返回非空 = 重载失败(由调用方拼成 warning)。
// 为什么走 RoleService.Reload:它内部 Refresh + Rescan,角色定义与私有技能归属一并刷新
// (只 Rescan 的话,角色列表里的 OwnSkills 会停在旧值);角色服务未装配时才退化为单扫地重扫。
func (s *Server) reloadRoleSkillCaches() string {
	if rs := s.roleService(); rs != nil {
		if err := rs.Reload(); err != nil {
			return "重载失败:" + err.Error()
		}
		return ""
	}
	if err := s.rescanSkills(); err != nil {
		return "技能重扫失败:" + err.Error()
	}
	return ""
}
