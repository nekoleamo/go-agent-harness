// 记忆治理端点单测(与角色段同款:装**真实**插件,走懒解析,不写服务替身)。
//
// 判据是「面板改的东西是否真落到了记忆文件里」—— 替身会让这条判据失效。
package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
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

// TestMemoryEndpointRetrieveSection 面板要能显示"最近一次自动调取"——
// 三种空结果(没记忆/没命中/命中但超预算)在 JSON 里必须可区分。
func TestMemoryEndpointRetrieveSection(t *testing.T) {
	s, _ := memoryServer(t, true)
	code, body := do(t, s, "GET", "/api/memory", "")
	if code != http.StatusOK {
		t.Fatalf("GET 应 200,got %d", code)
	}
	m := decodeMemory(t, body)
	rv, ok := m["retrieve"].(map[string]any)
	if !ok {
		t.Fatalf("装配了 MemoryRetrieveStats 的构建应带 retrieve 段,got %#v", m["retrieve"])
	}
	if rv["mode"] != "per_step" {
		t.Fatalf("调取模式应回显 per_step,got %#v", rv["mode"])
	}
	if _, has := rv["at"]; has {
		t.Fatalf("还没跑过回合时不该编一个时间戳: %#v", rv)
	}
}

// TestMemoryEndpointFilter 面板过滤在后端:查询参数生效,且回显 counts/filter
// (前端要靠它显示"过滤中 N/M 条",不自己解析展示行)。
func TestMemoryEndpointFilter(t *testing.T) {
	s, _ := memoryServer(t, true)
	// 造两条:一条自动(带类型)、一条手工
	if err := memory.AppendMeta(memory.UserPath(), "交付前跑 gate.sh", "s1", "convention", 0.9); err != nil {
		t.Fatal(err)
	}
	if err := memory.Append(memory.UserPath(), "手工记的一条", ""); err != nil {
		t.Fatal(err)
	}
	code, body := do(t, s, "GET", "/api/memory?auto=1", "")
	if code != http.StatusOK {
		t.Fatalf("GET 200,got %d", code)
	}
	m := decodeMemory(t, body)
	user, _ := m["user"].([]any)
	if len(user) != 1 {
		t.Fatalf("auto=1 应只留自动写入的 1 条,got %#v", m["user"])
	}
	fv, ok := m["filter"].(map[string]any)
	if !ok || fv["auto"] != true {
		t.Fatalf("应回显 filter.auto,got %#v", m["filter"])
	}
	cv, ok := m["counts"].(map[string]any)
	if !ok || cv["user"] != float64(2) || cv["auto"] != float64(1) {
		t.Fatalf("counts 应给总量口径,got %#v", m["counts"])
	}
	// kind 过滤
	code, body = do(t, s, "GET", "/api/memory?kind=convention", "")
	if code != http.StatusOK {
		t.Fatalf("GET 200,got %d", code)
	}
	m = decodeMemory(t, body)
	if user, _ := m["user"].([]any); len(user) != 1 {
		t.Fatalf("kind=convention 应只留 1 条,got %#v", m["user"])
	}
	// 无过滤时不该带 filter 字段(形状与旧版逐字一致)
	_, body = do(t, s, "GET", "/api/memory", "")
	if _, has := decodeMemory(t, body)["filter"]; has {
		t.Fatal("无过滤时不该回 filter 字段")
	}
}

// TestMemoryEndpointSourcesUnaffectedByFilter "按来源整段删"的选项取自**全量**来源:
// 过滤只缩列表,不该让一部分来源永远选不到(整段删是整段动作,与筛选无关)。
func TestMemoryEndpointSourcesUnaffectedByFilter(t *testing.T) {
	s, _ := memoryServer(t, true)
	if err := memory.AppendMeta(memory.UserPath(), "约定 A", "sess-a", "convention", 0.9); err != nil {
		t.Fatal(err)
	}
	if err := memory.AppendMeta(memory.UserPath(), "偏好 B", "sess-b", "preference", 0.9); err != nil {
		t.Fatal(err)
	}
	code, body := do(t, s, "GET", "/api/memory?kind=convention", "")
	if code != http.StatusOK {
		t.Fatalf("GET 200,got %d", code)
	}
	m := decodeMemory(t, body)
	// 过滤后列表只剩 sess-a
	user, _ := m["user"].([]any)
	if len(user) != 1 {
		t.Fatalf("kind=convention 应只剩 1 条,got %#v", m["user"])
	}
	// 但来源清单必须两个都在(否则 sess-b 的记忆永远删不掉)
	src, _ := m["sources"].([]any)
	got := map[string]bool{}
	for _, x := range src {
		got[x.(string)] = true
	}
	if !got["sess-a"] || !got["sess-b"] {
		t.Fatalf("sources 应是全量来源(不受过滤影响),got %#v", m["sources"])
	}
}

// —— 成本账与试查(B 批)——

// 面板要能回答"这套记忆值不值它的 token":累计口径必须随视图下发(没跑过请求时
// requests=0、hit_rate=0,而不是省略字段让前端猜)。
func TestMemoryEndpointInjectLedger(t *testing.T) {
	s, _ := memoryServer(t, true)
	code, body := do(t, s, "GET", "/api/memory", "")
	if code != http.StatusOK {
		t.Fatalf("GET 200, got %d", code)
	}
	m := decodeMemory(t, body)
	inj, ok := m["inject"].(map[string]any)
	if !ok {
		t.Fatalf("应下发 inject 组(未实现窄接口才省略): %#v", m["inject"])
	}
	if inj["requests"].(float64) != 0 || inj["hit_rate"].(float64) != 0 {
		t.Fatalf("没跑过请求时应是 0/0, got %#v", inj)
	}
	if line, _ := inj["line"].(string); !strings.Contains(line, "还没有记账") {
		t.Fatalf("应有口径描述行, got %q", line)
	}
}

// ?probe= 试查:命中要有分数与"会不会注入"的判定;空查询给 400 之外的错因字段
// (面板照常渲染其余部分,不整段 500)。
func TestMemoryEndpointProbe(t *testing.T) {
	s, _ := memoryServer(t, true)
	for _, c := range []string{"部署统一走 make deploy", "发布前先跑 gate 与覆盖率门"} {
		if code, _ := do(t, s, "POST", "/api/memory", `{"action":"add","content":`+jsonStr(c)+`}`); code != http.StatusOK {
			t.Fatalf("add 应 200")
		}
	}
	code, body := do(t, s, "GET", "/api/memory?probe="+url.QueryEscape("部署"), "")
	if code != http.StatusOK {
		t.Fatalf("试查 200, got %d", code)
	}
	m := decodeMemory(t, body)
	pv, ok := m["probe"].(map[string]any)
	if !ok {
		t.Fatalf("应下发 probe 组: %#v", m["probe"])
	}
	hits, _ := pv["hits"].([]any)
	if len(hits) == 0 {
		t.Fatalf("「部署」应命中一条: %#v", pv)
	}
	first := hits[0].(map[string]any)
	if _, ok := first["score"].(float64); !ok {
		t.Fatalf("命中要带分数(诊断面要能看'为什么选中'): %#v", first)
	}
	if _, ok := first["selected"].(bool); !ok {
		t.Fatalf("命中要带'会不会注入'的判定: %#v", first)
	}
	// 零命中也要照常返回(空数组,不是 null)
	if code, body := do(t, s, "GET", "/api/memory?probe="+url.QueryEscape("量子色动力学胶子禁闭"), ""); code != http.StatusOK {
		t.Fatalf("零命中 200, got %d", code)
	} else if pv, ok := decodeMemory(t, body)["probe"].(map[string]any); !ok {
		t.Fatal("零命中也要给 probe 组")
	} else if hits, ok := pv["hits"].([]any); !ok || len(hits) != 0 {
		t.Fatalf("零命中应是空数组, got %#v", pv["hits"])
	}
}
