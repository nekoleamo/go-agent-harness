// 回收站端点单测(第八十三批:删除的角色/技能曾"可恢复"但没入口)。
//
// 装配真实角色链(与 roles_test.go 同一套),走懒解析 + 真落盘路径 —— 判据是
// "恢复后角色/技能是否真的回到磁盘与索引里",不是替身是否被调用。
package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type trashListView struct {
	Roles []struct {
		Name      string `json:"name"`
		ID        string `json:"id"`
		DeletedAt string `json:"deleted_at"`
	} `json:"roles"`
	Skills []struct {
		Name      string `json:"name"`
		Skill     string `json:"skill"`
		Role      string `json:"role"`
		DeletedAt string `json:"deleted_at"`
	} `json:"skills"`
}

func getTrash(t *testing.T, s *Server) trashListView {
	t.Helper()
	code, body := do(t, s, http.MethodGet, "/api/trash", "")
	if code != 200 {
		t.Fatalf("GET /api/trash = %d %s", code, body)
	}
	var v trashListView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("回收站响应不是预期形状: %v\n%s", err, body)
	}
	return v
}

func TestTrashEndpoints(t *testing.T) {
	s, home := rolesServer(t, true)

	// 空回收站:契约给空数组(不是 null,否则前端 .length 崩)
	code, body := do(t, s, http.MethodGet, "/api/trash", "")
	if code != 200 {
		t.Fatalf("GET /api/trash = %d %s", code, body)
	}
	if !strings.Contains(body, `"roles":[]`) || !strings.Contains(body, `"skills":[]`) {
		t.Fatalf("空回收站应下发空数组: %s", body)
	}

	// 备料:一个角色(带私有技能)+ 一个共享技能,分别删掉
	if code, body := do(t, s, http.MethodPost, "/api/roles", `{"id":"drop"}`); code != 200 {
		t.Fatalf("建角色失败: %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodPost, "/api/skills", `{"role":"drop","name":"tax","description":"税务","body":"b"}`); code != 200 {
		t.Fatalf("建角色私有技能失败: %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodPost, "/api/skills", `{"name":"shared-gone","description":"共享","body":"b"}`); code != 200 {
		t.Fatalf("建共享技能失败: %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodDelete, "/api/skills/shared-gone", ""); code != 200 {
		t.Fatalf("删共享技能失败: %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodDelete, "/api/roles/drop", ""); code != 200 {
		t.Fatalf("删角色失败: %d %s", code, body)
	}

	v := getTrash(t, s)
	if len(v.Roles) != 1 || v.Roles[0].ID != "drop" || !strings.HasPrefix(v.Roles[0].Name, "drop-") {
		t.Fatalf("角色回收站条目不符: %+v", v.Roles)
	}
	if v.Roles[0].DeletedAt == "" {
		t.Errorf("角色条目应带删除时间: %+v", v.Roles[0])
	}
	foundShared := false
	for _, sk := range v.Skills {
		if sk.Skill == "shared-gone" && sk.Role == "" {
			foundShared = true
		}
		// 角色私有技能随角色目录**整体**进角色回收站(嵌在角色目录里),不单列 —— 这是刻意的。
		if sk.Skill == "tax" {
			t.Errorf("角色私有技能不该单列(随角色一起进回收站): %+v", v.Skills)
		}
	}
	if !foundShared {
		t.Fatalf("共享技能删除后应出现在技能回收站: %+v", v.Skills)
	}

	// 非法 kind / 坏条目名 → 400
	for _, tc := range []string{`{"kind":"nope","name":"x"}`, `{"kind":"role","name":""}`, `{"kind":"skill","name":"notatimestamp"}`} {
		if code, body := do(t, s, http.MethodPost, "/api/trash/restore", tc); code != 400 {
			t.Fatalf("坏恢复请求应 400: %d %s(%s)", code, body, tc)
		}
	}

	// 恢复角色:用**回收站目录名**定位;恢复后目录、私有技能与缓存都要回来
	roleName := v.Roles[0].Name
	code, body = do(t, s, http.MethodPost, "/api/trash/restore", `{"kind":"role","name":"`+roleName+`"}`)
	if code != 200 || !strings.Contains(body, `"id":"drop"`) {
		t.Fatalf("恢复角色 = %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(home, "roles", "drop", "role.yaml")); err != nil {
		t.Fatalf("恢复后 role.yaml 应回来: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "roles", "drop", "skills", "tax", "SKILL.md")); err != nil {
		t.Fatalf("恢复后私有技能应跟着回来: %v", err)
	}
	// 只 os.Rename 不 Reload 的话,角色缓存里仍然没有它 —— 这条就是钉"恢复即生效"。
	if _, body := do(t, s, http.MethodGet, "/api/roles", ""); !strings.Contains(body, `"id":"drop"`) {
		t.Fatalf("恢复的角色未进缓存(Reload 没生效): %s", body)
	}
	if got := getTrash(t, s); len(got.Roles) != 0 {
		t.Fatalf("恢复后角色回收站应空: %+v", got.Roles)
	}

	// 同名已存在 → 400(不覆盖现役角色,不静默丢数据)
	if code, body := do(t, s, http.MethodDelete, "/api/roles/drop", ""); code != 200 {
		t.Fatalf("再次删角色失败: %d %s", code, body)
	}
	second := getTrash(t, s).Roles
	if len(second) != 1 {
		t.Fatalf("再次删除应产生一条回收站记录: %+v", second)
	}
	if code, body := do(t, s, http.MethodPost, "/api/roles", `{"id":"drop"}`); code != 200 {
		t.Fatalf("重建同名角色失败: %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodPost, "/api/trash/restore", `{"kind":"role","name":"`+second[0].Name+`"}`); code != 400 {
		t.Fatalf("目标 ID 已存在应 400: %d %s", code, body)
	}

	// 恢复技能:共享库(role 空);恢复后索引里能读到
	var sharedTrash string
	for _, sk := range getTrash(t, s).Skills {
		if sk.Skill == "shared-gone" {
			sharedTrash = sk.Name
		}
	}
	if sharedTrash == "" {
		t.Fatal("技能回收站里找不到 shared-gone")
	}
	code, body = do(t, s, http.MethodPost, "/api/trash/restore", `{"kind":"skill","name":"`+sharedTrash+`"}`)
	if code != 200 || !strings.Contains(body, `"name":"shared-gone"`) {
		t.Fatalf("恢复技能 = %d %s", code, body)
	}
	if code, body := do(t, s, http.MethodGet, "/api/skills/shared-gone", ""); code != 200 {
		t.Fatalf("恢复后技能不可读: %d %s", code, body)
	}
}
