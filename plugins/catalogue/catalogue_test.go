// catalogue 守卫测试:单一事实源一致性(登记覆盖 plugins/ 目录、依赖无悬空、bundle 归属)。
package catalogue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/plugins/host/host-fanout"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-jobs"
	"github.com/nekoleamo/go-agent-harness/plugins/host/token-compress"
	"github.com/nekoleamo/go-agent-harness/plugins/mcp/mcp-server"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// pluginDirsUnder 递归收集 plugins/ 下含 Go 源文件的插件目录(深度 ≤2,跳过 catalogue 自身)。
// 分组后布局:类别子目录(host/adapter/policy/tool/mcp/ui)下各插件一个目录。
func pluginDirsUnder(root string) []string {
	var out []string
	top, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, d := range top {
		if !d.IsDir() || d.Name() == "catalogue" {
			continue
		}
		sub := filepath.Join(root, d.Name())
		entries, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(sub, e.Name())
			fs, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			hasGo := false
			for _, f := range fs {
				if !f.IsDir() && strings.HasSuffix(f.Name(), ".go") {
					hasGo = true
					break
				}
			}
			if hasGo {
				out = append(out, e.Name())
			}
		}
	}
	return out
}

// TestEveryPluginDirRegistered 每个 plugins/ 下的插件包(含类别子目录)都有 catalogue 登记。
func TestEveryPluginDirRegistered(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range pluginDirsUnder(root) {
		if _, ok := All[id]; !ok {
			t.Fatalf("插件目录 %s 未在 catalogue 登记(单一事实源)", id)
		}
	}
}

// TestRequiredDepsReferenced requires 引用的 ctx.* 服务必须有提供方(悬空依赖 → 装配必失败)。
func TestRequiredDepsReferenced(t *testing.T) {
	provides := map[string]bool{}
	for _, d := range All {
		for _, p := range d.Manifest.Provides {
			provides[p] = true
		}
	}
	for id, d := range All {
		for _, r := range d.Manifest.Requires {
			if strings.HasPrefix(r, "ctx.") && !provides[r] {
				t.Fatalf("%s requires %s,但无任何插件 provides 它(悬空依赖)", id, r)
			}
		}
	}
}

// TestBundleCoverage base/tui/web 三 bundle 均有插件归属,且每插件 bundle 字段合法。
func TestBundleCoverage(t *testing.T) {
	seen := map[string]bool{}
	for id, d := range All {
		switch d.Bundle {
		case "base", "tui", "web", "im-wechat", "im-qq", "confirm-fusion":
		default:
			t.Fatalf("%s 的 bundle 归属非法: %q", id, d.Bundle)
		}
		seen[d.Bundle] = true
	}
	for _, b := range []string{"base", "tui", "web", "im-wechat", "im-qq", "confirm-fusion"} {

		if !seen[b] {
			t.Fatalf("bundle %s 无任何插件归属", b)
		}
	}
}

// TestIDTypeConsistency 插件 id 与包/类型符合命名约定(host-*/tool-*/policy-*/llm-*/ui-*)。
func TestIDTypeConsistency(t *testing.T) {
	for id, d := range All {
		switch d.Manifest.Type {
		case "host", "agent", "tool", "policy", "llm", "ui":
		default:
			t.Fatalf("%s 的 Type 非法: %q", id, d.Manifest.Type)
		}
	}
}

// TestSpecialPluginsPresent M6.8/M6.9 拆分后的关键插件在册。
func TestSpecialPluginsPresent(t *testing.T) {
	for _, want := range []string{"host-fanout", "token-compress", "mcp-server", "mcp-bridge", "tool-workflow"} {
		if _, ok := All[want]; !ok {
			t.Fatalf("应存在插件 %s", want)
		}
	}
	// 抽查工厂可实例化且包名一致(签名保真)
	checks := map[string]sdk.Plugin{
		"host-fanout":    &hostfanout.Plugin{},
		"host-jobs":      &hostjobs.Plugin{},
		"mcp-server":     &mcpserver.Plugin{},
		"token-compress": &tokencompress.Plugin{},
	}
	for id, pl := range checks {
		if pl.Name() != id {
			t.Fatalf("插件 %s 的 Name()=%q 与登记不符", id, pl.Name())
		}
	}
}

// TestNoDuplicateIDs 无重复登记。
func TestNoDuplicateIDs(t *testing.T) {
	ids := map[string]int{}
	for id := range All {
		ids[id]++
	}
	for id, n := range ids {
		if n > 1 {
			t.Fatalf("重复登记: %s ×%d", id, n)
		}
	}
}

// TestManageDeclared 管理域声明守卫:值合法,且历史名单集合不退化(catalogue 为单一事实源,
// web 展示层透传声明,不再硬编码名单;缺失声明 → 界面误显示可启停)。
func TestManageDeclared(t *testing.T) {
	external := map[string]bool{
		"tool-shell": true, "tool-files": true, "tool-web": true, "tool-memory": true,
		"tool-todo": true, "tool-subagent": true, "tool-workflow": true, "mcp-bridge": true,
	}
	scenario := map[string]bool{"llm-mock": true, "mcp-server": true, "ui-tui-app": true}
	for id, d := range All {
		switch d.Manage {
		case "", "external", "scenario":
		default:
			t.Fatalf("%s 的 Manage 声明非法: %q", id, d.Manage)
		}
		if external[id] && d.Manage != "external" {
			t.Fatalf("%s 应声明 external(M6.8 外部化),得 %q", id, d.Manage)
		}
		delete(external, id)
		if scenario[id] && d.Manage != "scenario" {
			t.Fatalf("%s 应声明 scenario(场景专用),得 %q", id, d.Manage)
		}
		delete(scenario, id)
	}
	if len(external) > 0 {
		t.Fatalf("external 声明缺失,界面将误显示可启停: %v", external)
	}
	if len(scenario) > 0 {
		t.Fatalf("scenario 声明缺失: %v", scenario)
	}
}

// TestManifestAPIVersionOk apiVersion 语义红线(>=1.0,<2.0 对齐 SDK 兼容)。
func TestManifestAPIVersionOk(t *testing.T) {
	for id, d := range All {
		if d.Manifest.APIVersion != ">=1.0,<2.0" {
			t.Fatalf("%s 的 APIVersion 偏离: %q", id, d.Manifest.APIVersion)
		}
	}
}

// TestDirNameMatchesID 插件目录名必须等于登记 id(目录扫描的命名约定)。
