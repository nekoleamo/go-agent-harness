// 记忆治理端点单测(与角色段同款:装**真实**插件,走懒解析,不写服务替身)。
//
// 判据是「面板改的东西是否真落到了记忆文件里」—— 替身会让这条判据失效。
package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-memory"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// memoryServer 起一个带真实 host-memory(+系统提示,注入依赖它)的 Server。
func memoryServer(t *testing.T, withMemory bool) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	s, _ := newTestServer()
	if !withMemory {
		return s, home
	}
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	for _, p := range []sdk.Plugin{&hostsystemprompt.Plugin{}, &hostmemory.Plugin{}} {
		if _, err := p.Start(c, &sdk.Manifest{}); err != nil {
			t.Fatalf("%s Start: %v", p.Name(), err)
		}
	}
	s.ctx = c
	return s, home
}

func decodeMemory(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, body)
	}
	return m
}

func TestMemoryEndpointCRUD(t *testing.T) {
	s, home := memoryServer(t, true)

	// 未装配 → 503(前端据此隐藏整段)
	if code, _ := do(t, s, "GET", "/api/memory", ""); code != http.StatusOK {
		t.Fatalf("装配后 GET 应 200,got %d", code)
	}

	// 空清单回 [] 不回 null
	code, body := do(t, s, "GET", "/api/memory", "")
	m := decodeMemory(t, body)
	if code != http.StatusOK {
		t.Fatalf("GET 200,got %d", code)
	}
	if arr, ok := m["user"].([]any); !ok || len(arr) != 0 {
		t.Fatalf("空清单应是 [],got %#v", m["user"])
	}
	if m["enabled"] != true {
		t.Fatalf("记忆默认应开启(口径建议 P2),got %#v", m["enabled"])
	}
	// 路径要给出来:记忆文件设计上就允许人手改
	if p, _ := m["user_path"].(string); !strings.HasPrefix(p, home) || !strings.HasSuffix(p, "memory/user.md") {
		t.Fatalf("user_path 应指向数据根下的 memory/user.md,got %q", p)
	}

	// add:两条
	for _, c := range []string{"报告图表用蓝灰配色,不要渐变", "部署前先跑一次 /verify"} {
		if code, _ := do(t, s, "POST", "/api/memory", `{"action":"add","content":`+jsonStr(c)+`}`); code != http.StatusOK {
			t.Fatalf("add %q 应 200,got %d", c, code)
		}
	}
	// 真落盘了(不是只在内存里)
	raw, err := os.ReadFile(filepath.Join(home, "memory", "user.md"))
	if err != nil {
		t.Fatalf("记忆文件没落盘: %v", err)
	}
	if !strings.Contains(string(raw), "蓝灰配色") {
		t.Fatalf("文件内容不含刚加的记忆:\n%s", raw)
	}

	// list:新的在前
	_, body = do(t, s, "GET", "/api/memory", "")
	m = decodeMemory(t, body)
	arr, _ := m["user"].([]any)
	if len(arr) != 2 {
		t.Fatalf("应有 2 条,got %d(%#v)", len(arr), arr)
	}
	if !strings.Contains(arr[0].(string), "部署前先跑") {
		t.Fatalf("新的应在前,got %v", arr[0])
	}

	// remove 1(序号从展示行取)
	if code, _ := do(t, s, "POST", "/api/memory", `{"action":"remove","index":1}`); code != http.StatusOK {
		t.Fatal("remove 应 200")
	}
	_, body = do(t, s, "GET", "/api/memory", "")
	m = decodeMemory(t, body)
	if arr, _ = m["user"].([]any); len(arr) != 1 {
		t.Fatalf("删后应剩 1 条,got %d", len(arr))
	}
	if !strings.Contains(arr[0].(string), "蓝灰配色") {
		t.Fatalf("删错了条:删掉的应是「部署前先跑」,剩下 %#v", arr[0])
	}

	// toggle off → 文件还在,只是不注入;再 on 回来
	if code, _ := do(t, s, "POST", "/api/memory", `{"action":"toggle","enabled":false}`); code != http.StatusOK {
		t.Fatal("toggle 应 200")
	}
	_, body = do(t, s, "GET", "/api/memory", "")
	if decodeMemory(t, body)["enabled"] != false {
		t.Fatal("toggle off 后 enabled 应为 false")
	}
	if raw2, _ := os.ReadFile(filepath.Join(home, "memory", "user.md")); len(raw2) == 0 {
		t.Fatal("关闭注入不该删掉记忆文件(关的是注入,不是数据)")
	}
	do(t, s, "POST", "/api/memory", `{"action":"toggle","enabled":true}`)
}

func TestMemoryEndpointRejects(t *testing.T) {
	s, _ := memoryServer(t, true)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"空内容", `{"action":"add","content":"   "}`, http.StatusBadRequest},
		{"未知动作", `{"action":"nuke"}`, http.StatusBadRequest},
		{"序号越界", `{"action":"remove","index":99}`, http.StatusBadRequest},
		{"序号为零", `{"action":"remove","index":0}`, http.StatusBadRequest},
		{"toggle 缺 enabled", `{"action":"toggle"}`, http.StatusBadRequest},
		{"按来源删缺 source", `{"action":"remove_source"}`, http.StatusBadRequest},
		{"坏 JSON", `{`, http.StatusBadRequest},
	}
	for _, c := range cases {
		if code, _ := do(t, s, "POST", "/api/memory", c.body); code != c.want {
			t.Errorf("%s:应 %d,got %d", c.name, c.want, code)
		}
	}
}

func TestMemoryRemoveSource(t *testing.T) {
	s, home := memoryServer(t, true)
	// 直接往文件里塞带来源的记忆(模拟 /memory add --from 那条路径写下的形态)
	p := filepath.Join(home, "memory", "user.md")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(strings.Join([]string{
		"- [2026-10-01] 来自会话 A 的结论(来源: 会话 abc)",
		"- [2026-10-02] 来自会话 B 的结论(来源: 会话 def)",
		"- [2026-10-02] 手工记的一条",
	}, "\n")+"\n"), 0o644)

	// 删一个不存在的来源:200 + deleted:0(不是「成功但什么都没发生」)
	code, body := do(t, s, "POST", "/api/memory", `{"action":"remove_source","source":"ghost"}`)
	if code != http.StatusOK {
		t.Fatalf("应 200,got %d", code)
	}
	if n := decodeMemory(t, body)["deleted"]; n != float64(0) {
		t.Fatalf("无此来源应回 deleted:0,got %#v", n)
	}
	// 真删
	code, body = do(t, s, "POST", "/api/memory", `{"action":"remove_source","source":"abc"}`)
	if code != http.StatusOK {
		t.Fatalf("应 200,got %d", code)
	}
	if n := decodeMemory(t, body)["deleted"]; n != float64(1) {
		t.Fatalf("应删 1 条,got %#v", n)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "会话 A") {
		t.Fatalf("来源 A 的记忆没删掉:\n%s", raw)
	}
	if !strings.Contains(string(raw), "会话 B") || !strings.Contains(string(raw), "手工记的") {
		t.Fatalf("不该动别的记忆:\n%s", raw)
	}
}

func TestMemoryEndpointUnavailable(t *testing.T) {
	s, _ := memoryServer(t, false)
	if code, _ := do(t, s, "GET", "/api/memory", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("未装配 ctx.memory 应 503,got %d", code)
	}
	if code, _ := do(t, s, "POST", "/api/memory", `{"action":"add","content":"x"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("未装配时 POST 也应 503,got %d", code)
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
