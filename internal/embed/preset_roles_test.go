package embed

// 预置角色的**内容质量**契约(第一百零二批:5 → 12,按场景分组)。
//
// 为什么给内容也钉测试:角色是「可改可删」的数据,seed 只保证"存在",不保证"能用"。
// 一次手滑(忘了写 description、把 identity 写成占位、AGENTS.md 只有标题)不会有任何
// 报错 —— 角色照样被释放、照样出现在面板里,只是点了没反应、或者像"没换角色"。
// 这些正是「预置角色看起来很多、其实没区别」的来源,所以在这里挡一道。
//
// 质量纪律:**宁少勿滥** —— 新增预置角色必须同时满足下面的全部断言,
// 写不出真实验收判据的角色不上线(见 DESIGN 第一百零二批的验收表)。

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
)

// presetRoles 预置角色清单(有测试覆盖的角色必须列在这里,反向也成立)。
var presetRoles = map[string]string{ // id → 期望分组
	"assistant":     "通用",
	"coding-master": "工程",
	"tech-lead":     "工程",
	"reviewer":      "工程",
	"ops-sre":       "工程",
	"data-analyst":  "数据",
	"finance":       "数据",
	"novelist":      "写作",
	"news-writer":   "写作",
	"tech-writer":   "写作",
	"translator":    "写作",
	"teacher":       "学习",
}

func TestPresetRoleContentContract(t *testing.T) {
	if SeedRolesVersion < 2 {
		t.Fatalf("预置角色集合版本应 ≥2(第一批扩到 12 个),got %d", SeedRolesVersion)
	}
	// 释放到临时数据根,按真实路径读内容(不走解析器:这里验的是**文件本身**)
	home := t.TempDir()
	if _, err := EnsureRoles(home); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, "roles"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		if e.IsDir() {
			got = append(got, e.Name())
		}
	}
	sort.Strings(got)
	if len(got) != len(presetRoles) {
		t.Fatalf("预置角色应恰好 %d 个,got %d(%v)", len(presetRoles), len(got), got)
	}
	for id, wantGroup := range presetRoles {
		dir := filepath.Join(home, "roles", id)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Fatalf("预置角色 %s 未释放: %v", id, err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "role.yaml"))
		if err != nil {
			t.Fatalf("%s 缺 role.yaml: %v", id, err)
		}
		yamlTxt := string(raw)
		// ① 用途(description):面板列表就靠它显示 —— 没有它等于这个角色没有说明书
		if !hasYAMLValue(yamlTxt, "description") {
			t.Errorf("%s 缺 description(列表里将没有任何用途说明)", id)
		}
		// ② 分组(group):12 个平铺认不出该挑哪个
		if g := yamlValue(yamlTxt, "group"); g != wantGroup {
			t.Errorf("%s 的 group 应为 %q,got %q", id, wantGroup, g)
		}
		// ③ 身份句(identity):身份槽的内容,缺了就是"没换角色"
		if !hasYAMLValue(yamlTxt, "identity") {
			t.Errorf("%s 缺 identity(身份槽会是空的)", id)
		}
		// ④ 一律不写 skills 键:用户技能池不可预知,写错会**静默缩小**可见技能
		if strings.Contains(yamlTxt, "\nskills:") {
			t.Errorf("%s 预置了 skills 键:会静默缩小用户可见技能(应交默认池)", id)
		}
		// ⑤ 工作规则(AGENTS.md)要有实质内容:标题 + 至少两个小节
		ag, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
		if err != nil {
			t.Fatalf("%s 缺 AGENTS.md: %v", id, err)
		}
		body := string(ag)
		if n := strings.Count(body, "\n## "); n < 2 {
			t.Errorf("%s 的 AGENTS.md 只有 %d 个小节(至少 2 个才算有规则)", id, n)
		}
		if len([]rune(body)) < 200 {
			t.Errorf("%s 的 AGENTS.md 过短(%d 字):写不出实质规则", id, len([]rune(body)))
		}
	}
}

// yamlValue 取顶层键的值(仅测试用的小解析;只处理 "key: value" 一行)。
func yamlValue(doc, key string) string {
	for _, line := range strings.Split(doc, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(t, key+":"))
		}
	}
	return ""
}

func hasYAMLValue(doc, key string) bool { return yamlValue(doc, key) != "" }

// TestPresetRolesParseClean 12 个预置角色都要能被真实解析器接受(不许有进 Broken() 的)。
func TestPresetRolesParseClean(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsureRoles(home); err != nil {
		t.Fatal(err)
	}
	dirs, err := os.ReadDir(filepath.Join(home, "roles"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range dirs {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(home, "roles", e.Name(), "role.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		// strictKeys=true:角色包导入侧的严格口径,预置内容自己就该干净
		if _, err := roles.ParseDefinition(e.Name(), raw, true); err != nil {
			t.Errorf("预置角色 %s 解析失败(会进 Broken() 对用户可见): %v", e.Name(), err)
		}
	}
}
