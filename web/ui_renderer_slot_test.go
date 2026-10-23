// tool-renderer 槽位的后端校验(第一百六十五批)。
//
// 钉的是「声明不完整要**看得见**」:缺 tools 的 tool-renderer 不会匹配任何工具,
// 表现是「装了插件,结果区没变」—— 用户只会以为插件坏了。所以:
//
//	① 槽位被摘掉(不下发一个不会生效的声明);
//	② 同一个插件的其它槽位照常下发(不因一个写错的槽位把整份产物挡在门外);
//	③ 声明完整时 tools 原样下发(前端按它匹配)。
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestUIScanDropsRendererWithoutTools(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	uiRoot := filepath.Join(home, "ui-plugins")
	s := newPluginServer(t, "")
	s.cfg.UIPluginsDir = uiRoot
	dir := filepath.Join(uiRoot, "demo")
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"demo","version":"1","slots":[
		{"name":"tool-renderer","priority":10,"module":"./dist/r.js"},
		{"name":"tool-renderer","priority":10,"module":"./dist/r2.js","tools":["image_ops"]},
		{"name":"extra-panel","priority":5,"module":"./dist/p.js","title":"示例"}
	]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"r.js", "r2.js", "p.js"} {
		if err := os.WriteFile(filepath.Join(dir, "dist", f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/ui-plugins", nil)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	var out []UIPlugin
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("插件本身应照常下发: %s", rec.Body.String())
	}
	if len(out[0].Slots) != 2 {
		t.Fatalf("缺 tools 的 tool-renderer 应被摘掉,只剩 2 个槽位, got %+v", out[0].Slots)
	}
	if out[0].Slots[0].Name != "tool-renderer" || len(out[0].Slots[0].Tools) != 1 || out[0].Slots[0].Tools[0] != "image_ops" {
		t.Fatalf("声明完整的 tool-renderer 必须原样下发(含 tools): %+v", out[0].Slots[0])
	}
	if out[0].Slots[1].Name != "extra-panel" {
		t.Fatalf("其它槽位不该受影响: %+v", out[0].Slots[1])
	}
	// JSON 契约:前端读 snake_case 以外原字段名(tools),空则省略。
	if !jsonHasKey(t, rec.Body.Bytes(), "tools") {
		t.Fatal("tools 字段必须出现在下发 JSON 里(前端按它匹配工具名)")
	}
}

// jsonHasKey 原始 JSON 里是否出现该键(防"结构体有字段但 tag 写错"这类静默丢失)。
func jsonHasKey(t *testing.T, raw []byte, key string) bool {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	var walk func(any) bool
	walk = func(x any) bool {
		switch t := x.(type) {
		case map[string]any:
			if _, ok := t[key]; ok {
				return true
			}
			for _, vv := range t {
				if walk(vv) {
					return true
				}
			}
		case []any:
			for _, vv := range t {
				if walk(vv) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
}
