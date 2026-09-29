// 技能改名 / 跨库移动端点单测(第八十四批)。
//
// 判据与回收站同口径:不看替身是否被调用,看**磁盘与索引是否真的变了** ——
// 尤其是"改名后角色的挂载清单有没有跟着改"(不改就会出现一条看不见的失效挂载)。
package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type relocateResp struct {
	OK            bool     `json:"ok"`
	Name          string   `json:"name"`
	Role          string   `json:"role"`
	FromName      string   `json:"from_name"`
	MountsUpdated []string `json:"mounts_updated"`
	Warning       string   `json:"warning"`
}

func relocate(t *testing.T, s *Server, path, body string) relocateResp {
	t.Helper()
	code, raw := do(t, s, http.MethodPost, path, body)
	if code != 200 {
		t.Fatalf("POST %s = %d %s", path, code, raw)
	}
	var v relocateResp
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("relocate 响应不是预期形状: %v\n%s", err, raw)
	}
	return v
}

func TestSkillRelocateEndpoint(t *testing.T) {
	s, home := rolesServer(t, true)

	// 备料:共享技能 report + 角色 finance 显式挂载它
	if code, body := do(t, s, http.MethodPost, "/api/skills", `{"name":"report","description":"周报","body":"b"}`); code != 200 {
		t.Fatalf("建共享技能失败: %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodPost, "/api/roles", `{"id":"finance","name":"财务"}`); code != 200 {
		t.Fatalf("建角色失败: %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodPatch, "/api/roles/finance", `{"skills_set":true,"skills":["report"]}`); code != 200 {
		t.Fatalf("挂载失败: %d %s", code, body)
	}

	// ① 只改名:挂载它的角色必须被同步(这是本端点存在的理由)
	r := relocate(t, s, "/api/skills/report/relocate", `{"to_name":"weekly-report"}`)
	if r.Name != "weekly-report" || r.FromName != "report" {
		t.Fatalf("改名响应不符: %+v", r)
	}
	if len(r.MountsUpdated) != 1 || r.MountsUpdated[0] != "finance" {
		t.Fatalf("挂载未被同步: %+v", r)
	}
	roleYAML, err := os.ReadFile(filepath.Join(home, "roles", "finance", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(roleYAML), "weekly-report") || strings.Contains(string(roleYAML), "- report") {
		t.Errorf("role.yaml 挂载未改写:\n%s", roleYAML)
	}
	// 目录名与 frontmatter 名一起改了(Write 的不变量:署名 == 目录名)
	if _, err := os.Stat(filepath.Join(home, "skills", "report")); !os.IsNotExist(err) {
		t.Error("旧技能目录应已不存在")
	}
	fm, err := os.ReadFile(filepath.Join(home, "skills", "weekly-report", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fm), "name: weekly-report") {
		t.Errorf("frontmatter name 未同步:\n%s", fm)
	}
	// 索引跟上:面板读到的库里是新名字
	code, body := do(t, s, http.MethodGet, "/api/roles", "")
	if code != 200 || !strings.Contains(body, `"weekly-report"`) {
		t.Fatalf("重扫后索引未更新: %d %s", code, body)
	}

	// ② 只换库(名字不变):共享库 → 角色私有;挂载按名字存,不需要改写
	r = relocate(t, s, "/api/skills/weekly-report/relocate", `{"to_role":"finance"}`)
	if r.Name != "weekly-report" || r.Role != "finance" || len(r.MountsUpdated) != 0 {
		t.Fatalf("换库响应不符: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(home, "roles", "finance", "skills", "weekly-report", "SKILL.md")); err != nil {
		t.Errorf("技能未落到角色私有库: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "weekly-report")); !os.IsNotExist(err) {
		t.Error("共享库副本应已搬走")
	}

	// ③ 改名 + 换库一起(搬回共享库):挂载再次被同步
	r = relocate(t, s, "/api/skills/weekly-report/relocate?role=finance", `{"to_name":"report","to_role":""}`)
	if r.Name != "report" || r.FromName != "weekly-report" || len(r.MountsUpdated) != 1 {
		t.Fatalf("改名+换库响应不符: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "report", "SKILL.md")); err != nil {
		t.Errorf("技能未搬回共享库: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "roles", "finance", "skills", "weekly-report")); !os.IsNotExist(err) {
		t.Error("角色私有库副本应已搬走")
	}

	// ④ 拒绝路径:无变化 / 目标同名已存在 / 源不存在 / 非法角色 id
	if code, body := do(t, s, http.MethodPost, "/api/skills/report/relocate", `{"to_name":"report"}`); code != 400 {
		t.Errorf("无变化应 400,got %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodPost, "/api/skills", `{"name":"dup","body":"b"}`); code != 200 {
		t.Fatalf("建 dup 失败: %d %s", code, body)
	}
	code, rbody := do(t, s, http.MethodPost, "/api/skills/report/relocate", `{"to_name":"dup"}`)
	if code != 400 || !strings.Contains(rbody, "同名") {
		t.Errorf("目标同名应 400 且说明原因,got %d %s", code, rbody)
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "report")); err != nil {
		t.Error("被拒时源技能不应被动")
	}
	if code, body := do(t, s, http.MethodPost, "/api/skills/ghost/relocate", `{"to_name":"x"}`); code != 400 {
		t.Errorf("源不存在应 400,got %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodPost, "/api/skills/dup/relocate?role=BAD%20ID", `{"to_name":"x"}`); code != 400 {
		t.Errorf("非法角色 id 应 400,got %d %s", code, body)
	}
	// 目标角色 id 非法(不该出现半截移动)
	if code, body := do(t, s, http.MethodPost, "/api/skills/dup/relocate", `{"to_role":"BAD ID"}`); code != 400 {
		t.Errorf("非法目标角色 id 应 400,got %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "dup", "SKILL.md")); err != nil {
		t.Errorf("被拒后 dup 应仍在共享库: %v", err)
	}

	// ⑤ 未装配角色链 → 503(与其它角色端点同口径)
	s2, _ := rolesServer(t, false)
	if code, _ := do(t, s2, http.MethodPost, "/api/skills/x/relocate", `{"to_name":"y"}`); code != http.StatusServiceUnavailable {
		t.Errorf("未装配应 503,got %d", code)
	}
}
