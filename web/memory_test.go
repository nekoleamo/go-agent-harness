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
	"strconv"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/memory"

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
	// 路径要给出来:记忆文件设计上就允许人手改。
	// 判据 = **与服务端同一条解析链**(`sdk.Home()` + memory.UserPath()),而不是
	// 「前缀是 t.TempDir()」—— Windows 上 TempDir 有时给短名(RUNNER~1)、有时给长名,
	// 前缀比较会假红(run 36960818167 test-windows 就是这么红的)。
	// 比前缀也证明不了「写在数据根内」这条纪律,而比解析链能。
	want := filepath.ToSlash(memory.UserPath())
	if p, _ := m["user_path"].(string); filepath.ToSlash(p) != want {
		t.Fatalf("user_path 应由数据根派生(%s),got %q", want, p)
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

// 候选池端点(记忆层 M2 前置件)。判据与上面同款:面板的操作**真落到了候选文件/记忆文件**。
func TestMemoryCandidateEndpoints(t *testing.T) {
	s, home := memoryServer(t, true)

	// 初始为空
	_, body := do(t, s, "GET", "/api/memory", "")
	m := decodeMemory(t, body)
	if cs, _ := m["candidates"].([]any); len(cs) != 0 {
		t.Fatalf("初始候选应为空,got %#v", m["candidates"])
	}
	if m["candidate_limit"] != float64(memory.MaxCandidates) {
		t.Fatalf("应回候选池上限,got %#v", m["candidate_limit"])
	}

	// propose → 进候选文件,**不进记忆文件**
	if code, _ := do(t, s, "POST", "/api/memory", `{"action":"propose","content":"候选A"}`); code != http.StatusOK {
		t.Fatal("propose 应 200")
	}
	raw, _ := os.ReadFile(filepath.Join(home, "memory", "candidates.md"))
	if !strings.Contains(string(raw), "候选A") {
		t.Fatalf("候选没落盘:\n%s", raw)
	}
	if _, err := os.Stat(filepath.Join(home, "memory", "user.md")); !os.IsNotExist(err) {
		t.Fatal("提候选不该写记忆文件(候选永不进上下文)")
	}

	// 限流:当日 5 条 ⇒ 第 6 条 400 且**不落盘**
	for i := 2; i <= memory.MaxCandidatesPerDay; i++ {
		if code, _ := do(t, s, "POST", "/api/memory", `{"action":"propose","content":`+jsonStr("候选"+strconv.Itoa(i))+`}`); code != http.StatusOK {
			t.Fatalf("第 %d 条应成功", i)
		}
	}
	code, _ := do(t, s, "POST", "/api/memory", `{"action":"propose","content":"超限的一条"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("超当日额度应 400,got %d", code)
	}
	raw, _ = os.ReadFile(filepath.Join(home, "memory", "candidates.md"))
	if strings.Contains(string(raw), "超限的一条") {
		t.Fatal("被限流拒的候选不该落盘")
	}

	// accept 1 ⇒ 进记忆,候选少一条
	if code, _ := do(t, s, "POST", "/api/memory", `{"action":"accept","index":1}`); code != http.StatusOK {
		t.Fatal("accept 应 200")
	}
	raw, _ = os.ReadFile(filepath.Join(home, "memory", "user.md"))
	if !strings.Contains(string(raw), "候选") {
		t.Fatalf("accept 后记忆文件应有内容:\n%s", raw)
	}
	_, body = do(t, s, "GET", "/api/memory", "")
	if cs, _ := decodeMemory(t, body)["candidates"].([]any); len(cs) != memory.MaxCandidatesPerDay-1 {
		t.Fatalf("候选应剩 %d 条,got %d", memory.MaxCandidatesPerDay-1, len(cs))
	}

	// reject 1 ⇒ 丢弃,不进记忆
	before, _ := os.ReadFile(filepath.Join(home, "memory", "user.md"))
	if code, _ := do(t, s, "POST", "/api/memory", `{"action":"reject","index":1}`); code != http.StatusOK {
		t.Fatal("reject 应 200")
	}
	after, _ := os.ReadFile(filepath.Join(home, "memory", "user.md"))
	if string(before) != string(after) {
		t.Fatal("reject 不该动记忆文件")
	}

	// accept_all ⇒ 全进;半截状态由后端负责如实报
	_, body = do(t, s, "POST", "/api/memory", `{"action":"accept_all"}`)
	if n := decodeMemory(t, body)["deleted"]; n == float64(0) {
		t.Fatalf("accept_all 应回转正条数,got %#v", n)
	}
	_, body = do(t, s, "GET", "/api/memory", "")
	m = decodeMemory(t, body)
	if cs, _ := m["candidates"].([]any); len(cs) != 0 {
		t.Fatalf("accept_all 后候选应清空,got %d", len(cs))
	}
	// 5 条候选里:1 条 accept、1 条 reject(丢弃)、其余 accept_all ⇒ 记忆里 4 条
	if want := memory.MaxCandidatesPerDay - 1; func() int { us, _ := m["user"].([]any); return len(us) }() != want {
		t.Fatalf("记忆里应有 %d 条(5 提 - 1 驳回),got %#v", want, m["user"])
	}

	// reject_all 在空池上 ⇒ 0 条,不报错
	if code, _ := do(t, s, "POST", "/api/memory", `{"action":"reject_all"}`); code != http.StatusOK {
		t.Fatal("空池 reject_all 应 200")
	}
}

// 未实现候选能力的老构建:候选动作 501(不是 400 也不是**偷偷写进记忆**)。
func TestMemoryCandidatesUnsupported(t *testing.T) {
	s, _ := memoryServer(t, true)
	// 拿一个只实现 MemoryService 的替身(不实现候选接口)覆盖缓存
	var only sdk.MemoryService = &plainMemory{}
	s.roleMu.Lock()
	s.memory = only
	s.roleMu.Unlock()
	for _, act := range []string{`{"action":"propose","content":"x"}`, `{"action":"accept","index":1}`, `{"action":"accept_all"}`} {
		if code, _ := do(t, s, "POST", "/api/memory", act); code != http.StatusNotImplemented {
			t.Fatalf("%s 应 501,got %d", act, code)
		}
	}
	// 读视图里候选组应缺席(前端据此不渲染),而不是给一个空的
	_, body := do(t, s, "GET", "/api/memory", "")
	if strings.Contains(body, "candidates") {
		t.Fatalf("未实现候选能力时不该返回 candidates 字段:%s", body)
	}
}

// plainMemory 只实现 sdk.MemoryService(模拟老构建的 ctx.memory)。
type plainMemory struct{}

func (plainMemory) Enabled() bool                      { return true }
func (plainMemory) SetEnabled(bool) bool               { return true }
func (plainMemory) Budget() int                        { return 2048 }
func (plainMemory) Add(string, string) error           { return nil }
func (plainMemory) List() []string                     { return []string{} }
func (plainMemory) Remove(int) (string, error)         { return "", nil }
func (plainMemory) RemoveBySource(string) (int, error) { return 0, nil }
