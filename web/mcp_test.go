// MCP 配置端点测试(NOND-M1 第 3 步):列表状态推导(direct/search/env 三类)、
// 保存落盘 + 插件重载、部分失败(reload_err)、参数校验。
package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/mcpconfig"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// errReloadBoom 重载失败替身错误。
var errReloadBoom = errors.New("重启外部进程超时")

// stubExtPlugins 外部插件控制面替身(记录重载请求)。
// afterReload 用于模拟「重载后工具面变了」—— 保存响应必须反映重载后的状态。
type stubExtPlugins struct {
	sdk.ExternalPlugins
	reloads     []string
	err         error
	afterReload func()
}

func (s *stubExtPlugins) Reload(name string) error {
	s.reloads = append(s.reloads, name)
	if s.err == nil && s.afterReload != nil {
		s.afterReload()
	}
	return s.err
}

// mcpViewResp 端点返回的视图(测试解码用)。
type mcpViewResp struct {
	Path            string          `json:"path"`
	Servers         []mcpServerView `json:"servers"`
	ReloadAvailable bool            `json:"reload_available"`
	PluginLoaded    bool            `json:"plugin_loaded"`
	ReloadErr       string          `json:"reload_err"`
	Notes           []string        `json:"notes"`
}

func getMCP(t *testing.T, url string) mcpViewResp {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET 应为 200,得 %d", resp.StatusCode)
	}
	var v mcpViewResp
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func postMCP(t *testing.T, url, body string) (int, mcpViewResp) {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v mcpViewResp
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode, v
}

// TestMCPListAndStatus GET /api/mcp:文件条目 + env 条目合并,状态按模式推导。
func TestMCPListAndStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := mcpconfig.Save([]mcpconfig.Server{
		{Name: "alpha", Command: "/bin/alpha"},
		{Name: "big", Command: "/bin/big", Mode: mcpconfig.ModeSearch},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_MCP_COMMANDS", "fromenv=/bin/env -x")

	s, _ := newTestServer()
	s.tools = &stubTools{
		defs: map[string]sdk.ToolDefinition{
			"mcp_alpha_a": {Name: "mcp_alpha_a"},
			"mcp_alpha_b": {Name: "mcp_alpha_b"},
			"mcp_search":  {Name: "mcp_search"},
			"mcp_call":    {Name: "mcp_call"},
		},
		out: map[string]string{"mcp_search": `{"count":2,"total":2,"tools":[` +
			`{"name":"mcp_big_x","server":"big"},{"name":"mcp_big_y","server":"big"}]}`},
	}
	s.extp = &stubExtPlugins{}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	v := getMCP(t, hs.URL+"/api/mcp")
	if v.Path != filepath.Join(home, "config", "mcp.yaml") {
		t.Fatalf("配置路径应落在 GAH_HOME 下: %s", v.Path)
	}
	if len(v.Servers) != 3 {
		t.Fatalf("应有 3 个 server(2 文件 + 1 env): %+v", v.Servers)
	}
	byName := map[string]mcpServerView{}
	for _, sv := range v.Servers {
		byName[sv.Name] = sv
	}
	// direct:工具名前缀计数
	if a := byName["alpha"]; !a.Loaded || a.Tools != 2 || a.Source != mcpconfig.SourceFile {
		t.Fatalf("alpha(文件/direct)状态错: %+v", a)
	}
	// search:计数来自 mcp_search 索引
	if b := byName["big"]; !b.Loaded || b.Tools != 2 || b.Mode != mcpconfig.ModeSearch {
		t.Fatalf("big(search)状态错: %+v", b)
	}
	// env 独有条目:标来源、无运行期工具(=未加载)
	if e := byName["fromenv"]; e.Source != mcpconfig.SourceEnv || e.Loaded || e.Tools != 0 {
		t.Fatalf("env 条目应标只读来源且未加载: %+v", e)
	}
	if !v.ReloadAvailable || !v.PluginLoaded {
		t.Fatalf("应报告可重载且插件已加载: %+v", v)
	}
}

// TestMCPSaveAndReload POST /api/mcp:保存写盘 + 重启插件;env 条目不被复制进文件。
func TestMCPSaveAndReload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := mcpconfig.Save([]mcpconfig.Server{{Name: "alpha", Command: "/bin/alpha"}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_MCP_COMMANDS", "fromenv=/bin/env")

	s, _ := newTestServer()
	s.tools = &stubTools{defs: map[string]sdk.ToolDefinition{
		"mcp_new_x":  {Name: "mcp_new_x"},
		"mcp_search": {Name: "mcp_search"},
	}}
	extp := &stubExtPlugins{}
	s.extp = extp
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	// 保存:改 alpha 为停用、新增 search 模式 server,并附带一个 env 来源条目
	body := `{"servers":[
		{"name":"alpha","command":"/bin/alpha","enabled":false},
		{"name":"new","command":"/bin/new --flag","mode":"search"},
		{"name":"fromenv","command":"/bin/env","source":"env"}]}`
	code, v := postMCP(t, hs.URL+"/api/mcp", body)
	if code != http.StatusOK {
		t.Fatalf("保存应 200,得 %d", code)
	}
	if v.ReloadErr != "" {
		t.Fatalf("重载不应失败: %s", v.ReloadErr)
	}
	if len(extp.reloads) != 1 || extp.reloads[0] != "tool-mcp" {
		t.Fatalf("应重载 tool-mcp: %v", extp.reloads)
	}
	// 落盘内容:2 条(env 条目不入文件);alpha 停用;new 是 search 且命令拆出 args
	got, _, err := mcpconfig.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Load 含 env 条目,故只看文件侧:重新读文件
	fp := filepath.Join(home, "config", "mcp.yaml")
	raw, err := os.ReadFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# gah MCP server") {
		t.Fatalf("文件应有表头注释: %s", raw)
	}
	var fileNames []string
	for _, g := range got {
		if g.Source == mcpconfig.SourceFile {
			fileNames = append(fileNames, g.Name)
		}
	}
	if strings.Join(fileNames, ",") != "alpha,new" {
		t.Fatalf("文件条目应只有 alpha,new: %v(原始:\n%s)", fileNames, raw)
	}
	for _, g := range got {
		switch g.Name {
		case "alpha":
			if g.IsEnabled() {
				t.Fatal("alpha 应被停用")
			}
		case "new":
			if g.ModeOrDefault() != mcpconfig.ModeSearch {
				t.Fatalf("new 应为 search: %+v", g)
			}
			if len(g.Args) != 1 || g.Args[0] != "--flag" {
				t.Fatalf("命令串应拆出参数: %+v", g)
			}
		}
	}
	// 视图里 new 已加载(search 索引里没有它 → 未加载;证明状态是按运行期推导的)
	byName := map[string]mcpServerView{}
	for _, sv := range v.Servers {
		byName[sv.Name] = sv
	}
	if byName["new"].Loaded {
		t.Fatalf("new 未连接不应显示已加载: %+v", byName["new"])
	}

	// reload:false → 只写盘不重载
	extp.reloads = nil
	code, v2 := postMCP(t, hs.URL+"/api/mcp", `{"servers":[{"name":"solo","command":"/bin/solo"}],"reload":false}`)
	if code != http.StatusOK || len(extp.reloads) != 0 {
		t.Fatalf("reload:false 不应重载: %d %v", code, extp.reloads)
	}
	_ = v2

	// 回归(真机现象):保存响应必须反映**重载后**的工具面。此前视图在重载前组装,
	// 面板点「保存并重载」会看到「0 个工具/未生效」 ⇒ 用户当成保存失败(而日志里
	// 插件其实已连上并报了「已连接:N 个工具」)。
	extp.reloads = nil
	extp.afterReload = func() {
		s.tools = &stubTools{defs: map[string]sdk.ToolDefinition{"mcp_solo_y": {Name: "mcp_solo_y"}}}
	}
	code, v3 := postMCP(t, hs.URL+"/api/mcp", `{"servers":[{"name":"solo","command":"/bin/solo"}]}`)
	if code != http.StatusOK {
		t.Fatalf("保存应 200,得 %d", code)
	}
	solo := mcpServerView{}
	for _, sv := range v3.Servers {
		if sv.Name == "solo" {
			solo = sv
		}
	}
	if !solo.Loaded || solo.Tools != 1 {
		t.Fatalf("保存响应应给出重载后的状态(loaded/1 个工具),得 %+v", solo)
	}
}

// TestMCPSaveValidation 参数校验与部分失败报告。
func TestMCPSaveValidation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv("GAH_MCP_COMMANDS", "")

	s, _ := newTestServer()
	s.extp = &stubExtPlugins{}
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	// 坏 JSON → 400
	if code, _ := postMCP(t, hs.URL+"/api/mcp", `{not json`); code != http.StatusBadRequest {
		t.Fatalf("坏请求体应 400,得 %d", code)
	}
	// 非法 mode → 400(不落盘)
	if code, _ := postMCP(t, hs.URL+"/api/mcp", `{"servers":[{"name":"x","command":"/bin/x","mode":"nope"}]}`); code != http.StatusBadRequest {
		t.Fatalf("非法 mode 应 400,得 %d", code)
	}
	// 空命令 → 400
	if code, _ := postMCP(t, hs.URL+"/api/mcp", `{"servers":[{"name":"x"}]}`); code != http.StatusBadRequest {
		t.Fatalf("空命令应 400,得 %d", code)
	}
	// 重载失败 → 200 + reload_err(写盘已成功,如实报告部分成功)
	s.extp = &stubExtPlugins{err: errReloadBoom}
	if code, v := postMCP(t, hs.URL+"/api/mcp", `{"servers":[{"name":"ok","command":"/bin/ok"}]}`); code != http.StatusOK ||
		!strings.Contains(v.ReloadErr, "重载 tool-mcp 失败") {
		t.Fatalf("重载失败应报告: %d %+v", code, v)
	}
	// 未装配控制面 → 提示需重启
	s.extp = nil
	if code, v := postMCP(t, hs.URL+"/api/mcp", `{"servers":[{"name":"ok2","command":"/bin/ok2"}]}`); code != http.StatusOK ||
		!strings.Contains(v.ReloadErr, "未装配") {
		t.Fatalf("未装配应提示重启: %d %+v", code, v)
	}
	if v := getMCP(t, hs.URL+"/api/mcp"); v.ReloadAvailable {
		t.Fatal("未装配时 reload_available 应为 false")
	}
}

// TestMCPListBrokenConfig 配置文件坏掉 → 显式 500(不静默显示空列表)。
func TestMCPListBrokenConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv("GAH_MCP_COMMANDS", "")
	dir := filepath.Join(home, "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mcp.yaml"), []byte("servers: [ {name: !!!}"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("坏配置应 500,得 %d", resp.StatusCode)
	}
}
