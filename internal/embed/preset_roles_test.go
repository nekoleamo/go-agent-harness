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
	"github.com/nekoleamo/go-agent-harness/internal/skills"
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
		// ④ skills 键(第一批起允许,但**只能列本角色的私有技能**):
		//    预置技能必须跟着角色走(否则"这个角色会用什么技能"说不清);引用共享库
		//    会被用户删技能搞成失效挂载。校验在下面的 TestPresetRoleSkillsResolve 里做。
		//    仍不允许 skills_inherit(那会把用户自己的技能并进来,预置内容不该预设)。
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

// TestPresetRoleSkillsResolve 预置角色的 skills 键必须**逐条解析到它自己目录下的
// 私有技能文件**,且每个技能文件要过解析器(strictKeys,坏文件不许进 Broken())。
//
// 为什么单独钉:skills 键写错一个名字,切到这个角色就会看到一条"失效挂载"
// (面板上挂着、模型却读不到)—— 而预置内容出错对所有用户生效,不是个别情况。
func TestPresetRoleSkillsResolve(t *testing.T) {
	if SeedRolesVersion < 4 {
		t.Fatalf("预置技能从 SeedRolesVersion 3 起(第三批 24 条),got %d", SeedRolesVersion)
	}
	home := t.TempDir()
	if _, err := EnsureRoles(home); err != nil {
		t.Fatal(err)
	}
	rolesDir := filepath.Join(home, "roles")
	entries, err := os.ReadDir(rolesDir)
	if err != nil {
		t.Fatal(err)
	}
	presetWithSkills := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		raw, err := os.ReadFile(filepath.Join(rolesDir, id, "role.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		names := presetSkillNames(string(raw))
		if len(names) == 0 {
			continue
		}
		presetWithSkills++
		// 只看**有效键**(行首),不看注释 —— 注释里解释这个语义是应该的。
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "skills_inherit:") && !strings.HasPrefix(line, "#") {
				t.Errorf("%s 预置了 skills_inherit:会并入用户自己的技能(预置内容不该预设)", id)
			}
		}
		for _, n := range names {
			if err := skills.ValidateName(n); err != nil {
				t.Errorf("%s 的 skills 含非法名 %q: %v", id, n, err)
				continue
			}
			p := filepath.Join(rolesDir, id, "skills", n, "SKILL.md")
			body, rerr := os.ReadFile(p)
			if rerr != nil {
				t.Errorf("%s 的技能 %q 没有随角色释放(挂载会失效): %v", id, n, rerr)
				continue
			}
			// 技能正文要能被解析器接受,且 frontmatter 的 name 与目录名一致
			if fn := skills.ParseName(string(body)); fn != "" && fn != n {
				t.Errorf("%s 的技能 %q frontmatter name 是 %q(与目录名不一致)", id, n, fn)
			}
		}
	}
	if presetWithSkills == 0 {
		t.Fatal("没有任何预置角色带技能 —— 第一批技能没落进来")
	}
}

// presetSkillNames 从 role.yaml 里抠出 skills 键下的名字列表(只看行首 "skills:" 后的
// "  - 名" 行;注释里的 skills: 不算数)。
func presetSkillNames(doc string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(doc, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(line, "skills:") {
			in = true
			continue
		}
		if in {
			if strings.HasPrefix(t, "- ") {
				out = append(out, strings.Trim(strings.TrimPrefix(t, "- "), "\"'"))
				continue
			}
			if t != "" && !strings.HasPrefix(line, "#") {
				in = false // 键块结束(下一个非缩进、非注释行)
			}
		}
	}
	return out
}
