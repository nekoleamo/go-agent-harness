// 角色携带模型/思考档的 Web 侧契约(第八十六批)。
//
// 走真实角色链 + 真实 role.yaml 落盘(与 roles_test.go 同一纪律):钉的不是"路由通了",
// 而是"面板改的确实进了角色定义,且 /api/state 报的是生效值而非会话档原值"。
package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoleModelPatchAndState(t *testing.T) {
	s, home := rolesServer(t, true)
	if code, body := do(t, s, http.MethodPost, "/api/roles", `{"id":"finance","name":"财务"}`); code != 200 {
		t.Fatalf("建角色失败: %d %s", code, body)
	}

	// 1) 未声明:state 报会话档(与旧行为一致)
	code, body := do(t, s, http.MethodGet, "/api/state", "")
	if code != 200 {
		t.Fatalf("state = %d %s", code, body)
	}
	var st StateView
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if st.ModelFrom != "session" || st.ThinkingFrom != "session" {
		t.Fatalf("无角色声明时应标会话来源: %+v", st)
	}

	// 2) 建/改带上模型与思考档 → 落进 role.yaml
	code, body = do(t, s, http.MethodPatch, "/api/roles/finance", `{"model":"claude-sonnet-4-5","thinking":"HIGH"}`)
	if code != 200 {
		t.Fatalf("PATCH 模型失败: %d %s", code, body)
	}
	raw, err := os.ReadFile(filepath.Join(home, "roles", "finance", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "claude-sonnet-4-5") || !strings.Contains(string(raw), "thinking: high") {
		t.Fatalf("模型/思考档未落盘(档位应归一为小写):\n%s", raw)
	}

	// 3) 切到该角色 → state 生效值 = 角色值,且给出会话原值供面板解释
	if code, body := do(t, s, http.MethodPost, "/api/roles/finance/use", "{}"); code != 200 {
		t.Fatalf("切换失败: %d %s", code, body)
	}
	code, body = do(t, s, http.MethodGet, "/api/state", "")
	if code != 200 {
		t.Fatalf("state = %d %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if st.Model != "claude-sonnet-4-5" || st.ModelFrom != "role" {
		t.Fatalf("生效模型应是角色值: %+v", st)
	}
	if st.Thinking != "high" || st.ThinkingFrom != "role" {
		t.Fatalf("生效思考档应是角色值: %+v", st)
	}
	if st.ModelSession != "deepseek-chat" || st.ThinkingSession != "medium" {
		t.Fatalf("会话原值必须一并下发(否则面板说不清「被谁覆盖」): %+v", st)
	}

	// 4) 非法档位显式 400(不能等到每回合静默归 Off)
	if code, _ := do(t, s, http.MethodPatch, "/api/roles/finance", `{"thinking":"bogus"}`); code != 400 {
		t.Fatalf("非法思考档应 400,got %d", code)
	}
	// 5) 传空串 = 清掉(跟随会话),不是"非法值"
	if code, body := do(t, s, http.MethodPatch, "/api/roles/finance", `{"thinking":"","model":""}`); code != 200 {
		t.Fatalf("清空应成功: %d %s", code, body)
	}
	if _, body := do(t, s, http.MethodGet, "/api/roles/finance", ""); strings.Contains(body, "claude-sonnet-4-5") {
		t.Fatalf("清空后详情不该还有模型: %s", body)
	}
	if _, body := do(t, s, http.MethodGet, "/api/state", ""); !strings.Contains(body, `"model_from":"session"`) {
		t.Fatalf("清空后应回到会话来源: %s", body)
	}
}
