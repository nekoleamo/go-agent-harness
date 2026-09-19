package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePlugin 造一个 UI 插件目录(manifest + 若干产物文件)。
func writePlugin(t *testing.T, dir, id string, files map[string]string) {
	t.Helper()
	plug := filepath.Join(dir, id)
	if err := os.MkdirAll(filepath.Join(plug, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"` + id + `","version":"1.0.0","slots":[{"name":"statusbar","priority":5,"module":"./dist/plugin.js"}]}`
	if err := os.WriteFile(filepath.Join(plug, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		full := filepath.Join(plug, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDigestPluginDirDeterministic 摘要确定性:同内容同值;内容变即变;
// 路径进摘要(同名同长内容互换位置不算撞值)。
func TestDigestPluginDirDeterministic(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{"dist/plugin.js": "export default {}", "dist/extra.css": ".a{}"}
	writePlugin(t, dir, "demo", files)
	slots := []SlotDef{{Name: "statusbar", Priority: 5, Module: "./dist/plugin.js"}}

	a := digestPluginDir(filepath.Join(dir, "demo"), slots)
	if a.Sum == "" || a.Scope != "full" || a.Note != "" {
		t.Fatalf("正常产物应为 full 摘要且无降级说明: %+v", a)
	}
	if b := digestPluginDir(filepath.Join(dir, "demo"), slots); b != a {
		t.Fatalf("同内容摘要应稳定: %+v vs %+v", a, b)
	}

	// 改一个字节 → 摘要必变
	if err := os.WriteFile(filepath.Join(dir, "demo", "dist", "extra.css"), []byte(".b{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := digestPluginDir(filepath.Join(dir, "demo"), slots); c.Sum == a.Sum {
		t.Fatal("产物内容变化后摘要必须变化")
	}

	// 路径参与摘要:把内容挪到另一个文件名(内容集合相同)也必须换值
	dir2 := t.TempDir()
	writePlugin(t, dir2, "demo", map[string]string{"dist/a.js": "X", "dist/b.js": "Y"})
	dir3 := t.TempDir()
	writePlugin(t, dir3, "demo", map[string]string{"dist/a.js": "Y", "dist/b.js": "X"})
	if x, y := digestPluginDir(filepath.Join(dir2, "demo"), slots), digestPluginDir(filepath.Join(dir3, "demo"), slots); x.Sum == y.Sum {
		t.Fatal("路径与内容必须一起进摘要(否则改名即撞值)")
	}
}

// TestDigestPluginDirDegradesExplicitly 降级必须显式:超预算只摘要入口且 scope=entry +
// Note 说明;空目录 = none + 原因(绝不出现「有校验值但只盖了一半」而不吭声)。
func TestDigestPluginDirDegradesExplicitly(t *testing.T) {
	slots := []SlotDef{{Name: "statusbar", Priority: 5, Module: "./dist/plugin.js"}}

	dir := t.TempDir()
	writePlugin(t, dir, "big", map[string]string{"dist/plugin.js": "entry"})
	// 造一个超预算的大文件(预算 4 MiB):全量摘要退化成仅入口
	blob := make([]byte, pluginHashBudget+1024)
	if err := os.WriteFile(filepath.Join(dir, "big", "dist", "big.bin"), blob, 0o600); err != nil {
		t.Fatal(err)
	}
	got := digestPluginDir(filepath.Join(dir, "big"), slots)
	if got.Scope != "entry" || got.Sum == "" {
		t.Fatalf("超预算应退化为仅入口摘要: %+v", got)
	}
	if !strings.Contains(got.Note, "仅摘要入口产物与 manifest") || !strings.Contains(got.Note, "MiB") {
		t.Fatalf("降级说明应含原因与体积: %q", got.Note)
	}
	// scope=entry = 覆盖集合正好是「入口 + manifest」(dir2 只有这两个文件,于是与 full 同值)
	dir2 := t.TempDir()
	writePlugin(t, dir2, "big", map[string]string{"dist/plugin.js": "entry"})
	if only := digestPluginDir(filepath.Join(dir2, "big"), slots); only.Sum != got.Sum {
		t.Fatalf("scope=entry 时应只覆盖入口: %s vs %s", only.Sum, got.Sum)
	}

	// 单文件过大(未超总预算)也必须显式计入 skipped 而不是静默漏掉
	dir3 := t.TempDir()
	writePlugin(t, dir3, "huge", map[string]string{"dist/plugin.js": "entry"})
	if err := os.WriteFile(filepath.Join(dir3, "huge", "dist", "huge.bin"), make([]byte, pluginHashMaxFile+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if h := digestPluginDir(filepath.Join(dir3, "huge"), slots); !strings.Contains(h.Note, "未摘要") {
		t.Fatalf("超大文件应计入说明: %+v", h)
	}

	// 空目录:none + 原因(不是空字符串了事)
	empty := t.TempDir()
	if e := digestPluginDir(empty, slots); e.Scope != "none" || e.Sum != "" || e.Note == "" {
		t.Fatalf("空目录应显式说明: %+v", e)
	}
}

// TestUIPluginsIntegrityFields /api/ui-plugins 下发摘要与覆盖范围,且与前端口径一致。
func TestUIPluginsIntegrityFields(t *testing.T) {
	s, _ := newTestServer()
	dir := t.TempDir()
	writePlugin(t, dir, "demo", map[string]string{"dist/plugin.js": "export default {}", "dist/x.css": ".a{}"})
	writePlugin(t, dir, "hollow", nil) // 只有 manifest(无产物文件)
	s.cfg.UIPluginsDir = dir
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	resp, err := http.Get(hs.URL + "/api/ui-plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []UIPlugin
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	byID := map[string]UIPlugin{}
	for _, p := range list {
		byID[p.ID] = p
	}
	demo, ok := byID["demo"]
	if !ok {
		t.Fatalf("应扫到 demo: %+v", list)
	}
	if len(demo.SHA256) != 64 || demo.HashScope != "full" || demo.HashNote != "" {
		t.Fatalf("完整产物应给 full 摘要: %+v", demo)
	}
	if demo.Trusted != true || demo.TrustNote != uiPluginTrustNote {
		t.Fatalf("信任字段不应被本次改动破坏: %+v", demo)
	}
	// manifest.json 也算产物的一部分:改它要让摘要变(否则"只改 slots 指向"可绕过)
	before := demo.SHA256
	writePlugin(t, dir, "demo2", map[string]string{"dist/plugin.js": "export default {}"})
	if digestPluginDir(filepath.Join(dir, "demo"), demo.Slots).Sum != before {
		t.Fatal("摘要应稳定")
	}
	// 只有 manifest 的插件:摘要只盖 manifest 也必须如实标 full(覆盖范围就是它全部文件),
	// 且与 demo 的摘要不同(不是同一个值套在所有插件上)
	hollow := byID["hollow"]
	if hollow.HashScope != "full" || len(hollow.SHA256) != 64 || hollow.SHA256 == demo.SHA256 {
		t.Fatalf("仅 manifest 的插件也应给独立摘要: %+v", hollow)
	}
	// 目录里只剩点文件 → none + 原因(unit 级:API 路径不会走到,因为要先有 manifest)
	dot := t.TempDir()
	if err := os.WriteFile(filepath.Join(dot, ".DS_Store"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d := digestPluginDir(dot, demo.Slots); d.Scope != "none" || d.Sum != "" || d.Note == "" {
		t.Fatalf("无可摘要文件应显式标注: %+v", d)
	}
}

// TestUIPluginsDigestIncludesManifest manifest 变更必须改变摘要(slots 指向可被改掉)。
func TestUIPluginsDigestIncludesManifest(t *testing.T) {
	dir := t.TempDir()
	writePlugin(t, dir, "demo", map[string]string{"dist/plugin.js": "x"})
	slots := []SlotDef{{Name: "statusbar", Priority: 5, Module: "./dist/plugin.js"}}
	before := digestPluginDir(filepath.Join(dir, "demo"), slots)
	raw, err := os.ReadFile(filepath.Join(dir, "demo", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(raw), `"priority":5`, `"priority":9`, 1)
	if err := os.WriteFile(filepath.Join(dir, "demo", "manifest.json"), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if after := digestPluginDir(filepath.Join(dir, "demo"), slots); after.Sum == before.Sum {
		t.Fatal("manifest 变更应改变摘要(否则可改指向而校验值不变)")
	}
}

// TestUIPluginsCarriesSlotTitle:v2 扩展点的 title 必须随聚合下发 —— 丢掉它,
// 插件的区段/动作/面板标题在宿主里只能显示默认文案(2026-09-19 本机验收逮到:
// 落位的 manifest 带 title,但 SlotDef 无该字段 → 前端 p.title 为 undefined)。
func TestUIPluginsCarriesSlotTitle(t *testing.T) {
	s, _ := newTestServer()
	dir := t.TempDir()
	plug := filepath.Join(dir, "demo")
	if err := os.MkdirAll(filepath.Join(plug, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	man := `{"id":"demo","version":"1.0.0","slots":[{"name":"extra-panel","priority":10,"module":"./dist/panel.js","title":"示例面板"}]}`
	if err := os.WriteFile(filepath.Join(plug, "manifest.json"), []byte(man), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plug, "dist", "panel.js"), []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.cfg.UIPluginsDir = dir
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	resp, err := http.Get(hs.URL + "/api/ui-plugins")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list []UIPlugin
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].Slots) != 1 || list[0].Slots[0].Title != "示例面板" {
		t.Fatalf("slot title 未随聚合下发: %+v", list)
	}
}
