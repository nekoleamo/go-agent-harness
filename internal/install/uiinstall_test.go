// UI 插件安装单测(M7.2):v-html 扫描拒装、manifest 校验、落位/卸载/清单、copyDir 排除。
// 成功路径含 npm build(外部依赖)由真机冒烟覆盖(gah -install-ui 本地目录示例插件)。
package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeT(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanVHTML(t *testing.T) {
	dir := t.TempDir()
	writeT(t, dir, "src/Ok.vue", "<template><div>{{ x }}</div></template>")
	writeT(t, dir, "src/Bad.vue", "<div v-html=\"evil\"></div>")
	file, ok := scanVHTML(dir)
	if !ok || !strings.Contains(file, "Bad.vue") {
		t.Fatalf("应命中 Bad.vue,得 %q ok=%v", file, ok)
	}
	// 注释/文档提及 v-html 不误伤(指令形态 v-html= 才命中)
	dir1 := t.TempDir()
	writeT(t, dir1, "src/Cmt.vue", "<!-- 渲染层禁止 v-html(契约) --><div>{{ x }}</div>")
	if _, ok := scanVHTML(dir1); ok {
		t.Fatal("注释提及 v-html 不应命中(指令形态才算)")
	}
	// 无 v-html 源码不命中
	dir2 := t.TempDir()
	writeT(t, dir2, "a.js", "const x = 1;")
	writeT(t, dir2, "node_modules/pkg/index.js", "v-html") // 跳过
	if _, ok := scanVHTML(dir2); ok {
		t.Fatal("无源码 v-html 不应命中(node_modules 跳过)")
	}
}

func TestScanBareProcessEnv(t *testing.T) {
	// 命中:vite lib 模式未替换 process.env.NODE_ENV 的产物
	d1 := t.TempDir()
	writeT(t, d1, "dist/plugin.js", "const K = process.env.NODE_ENV !== 'production' ? {} : {};")
	if f, ok := scanBareProcessEnv(d1); !ok || !strings.Contains(f, "plugin.js") {
		t.Fatalf("含裸 process.env 的产物应命中,得 %q ok=%v", f, ok)
	}
	// 不命中:已替换(define)/ 非 .js / 无引用
	d2 := t.TempDir()
	writeT(t, d2, "dist/plugin.js", `const K = "production" !== "production" ? {} : {};`)
	writeT(t, d2, "dist/readme.txt", "process.env.NODE_ENV")
	if f, ok := scanBareProcessEnv(d2); ok {
		t.Fatalf("已替换产物不应命中,得 %q", f)
	}
}

func TestReadUIManifestAndErrors(t *testing.T) {
	dir := t.TempDir()
	// 缺 manifest → 错误
	if _, err := readUIManifest(dir); err == nil {
		t.Fatal("缺 manifest 应报错")
	}
	writeT(t, dir, "manifest.json", `{"id":"demo","version":"0.1.0","slots":[{"name":"stream","priority":10,"module":"./dist/plugin.js"},{"name":"extra-panel","priority":10,"module":"./dist/panel.js","title":"示例面板"}]}`)
	m, err := readUIManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "demo" || len(m.Slots) != 2 || m.Slots[0].Name != "stream" {
		t.Fatalf("manifest 解析不符 %+v", m)
	}
	// v2 扩展点 title 必须落到结构体并在落位 manifest 里保留(丢了标题就退化为宿主默认文案)
	if m.Slots[1].Title != "示例面板" {
		t.Fatalf("slot title 未解析: %+v", m.Slots[1])
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"title":"示例面板"`) {
		t.Fatalf("落位 manifest 丢失 slot title: %s", raw)
	}
}

// TestInstallUIRejections:错误路径(v-html / id 缺失 / 未知槽位 / module 缺失产物)。
func TestInstallUIRejections(t *testing.T) {
	home := t.TempDir()
	// 1. v-html 拒装
	bad := t.TempDir()
	writeT(t, bad, "manifest.json", `{"id":"evil","slots":[{"name":"stream","module":"./dist/p.js"}]}`)
	writeT(t, bad, "src/Bad.vue", "<div v-html=\"x\"></div>")
	_, err := InstallUI(bad, home)
	if err == nil || !strings.Contains(err.Error(), "v-html") {
		t.Fatalf("v-html 源码应拒装,得 %v", err)
	}
	// 2. 缺 id
	nom := t.TempDir()
	writeT(t, nom, "manifest.json", `{"slots":[]}`)
	if _, err := InstallUI(nom, home); err == nil {
		t.Fatal("缺 id 应报错")
	}
	// 3. 未知槽位
	unk := t.TempDir()
	writeT(t, unk, "manifest.json", `{"id":"x","slots":[{"name":"bogus","module":"./dist/p.js"}]}`)
	if _, err := InstallUI(unk, home); err == nil || !strings.Contains(err.Error(), "未知槽位") {
		t.Fatalf("未知槽位应报错,得 %v", err)
	}
	// 4. module 缺失产物(构建成功但产物无该入口)—— 需 npm 构建,改为直接校验产物一致性函数路径:
	//    用 ListUI 空目录与 UninstallUI 未安装确认底线行为
	if err := UninstallUI("nope", home); err == nil {
		t.Fatal("卸载未安装插件应报错")
	}
	if len(ListUI(home)) != 0 {
		t.Fatal("空 home 不应有 UI 插件")
	}
}

func TestCopyDirSkipsNodeModules(t *testing.T) {
	src := t.TempDir()
	writeT(t, src, "dist/plugin.js", "export default {}")
	writeT(t, src, "dist/node_modules/big/index.js", "skip")
	writeT(t, src, ".vite/cache", "skip")
	dst := t.TempDir()
	if err := copyDir(filepath.Join(src, "dist"), dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "plugin.js")); err != nil {
		t.Fatal("产物应拷贝")
	}
	if _, err := os.Stat(filepath.Join(dst, "node_modules")); err == nil {
		t.Fatal("node_modules 应排除")
	}
	if _, err := os.Stat(filepath.Join(dst, ".vite")); err == nil {
		t.Fatal("隐藏目录应排除")
	}
}
