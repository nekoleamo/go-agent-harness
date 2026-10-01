package embed

// 预置技能的内容契约与「可持续」机制(第三批:12 个角色 × 2 条 = 24 条,2026-10-02)。
//
// 为什么要有这一层而不是「写 24 个文件就算完」:预置技能是**对所有用户生效**的
// 内容,而 gah 没有「技能市场审核」这一环 —— 写出来什么样就发什么样。上一批
// (4 个角色)靠人肉自觉,这一批要变成**机器判定**,否则下一次加角色/加技能时
// 质量会静默滑坡(新角色没技能、某条技能写成两行空话、某条从生态里抄来带注入
// 指令),而这些都要等到用户切到那个角色时才发现。
//
// 契约就是调研文档里的三道闸的可判定部分 + 一条结构契约:
//   闸① gah 已有等价能力不配 —— 不可机判,靠配方表人工过一遍(见
//        docs/PRESET_SKILLS.md);能机判的副作用是:技能正文不许承诺宿主已有的
//        能力(下面查重定向),否则模型会用技能去调一个不存在的东西。
//   闸② 依赖拿不到就是空壳 —— 预置技能一律**纯提示词、零外部依赖**,正文里
//        不许出现「请运行 X 命令」这类把外部 CLI 当前提的写法(黑名单)。
//   闸③ 提示注入面永不进默认池 —— 黑名单直查注入话术。
//
// 维护手册(含配方表与「怎么新增一条」):docs/PRESET_SKILLS.md。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// presetSkillNamesAll 解析全部预置角色的 skills 键 → 角色 id → 名字列表。
func presetSkillNamesAll(t *testing.T) map[string][]string {
	t.Helper()
	home := t.TempDir()
	if _, err := EnsureRoles(home); err != nil {
		t.Fatal(err)
	}
	rolesDir := filepath.Join(home, "roles")
	entries, err := os.ReadDir(rolesDir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(rolesDir, e.Name(), "role.yaml"))
		if err != nil {
			t.Fatalf("%s 缺 role.yaml: %v", e.Name(), err)
		}
		out[e.Name()] = presetSkillNames(string(raw))
	}
	return out
}

// presetSkillDoc 读一个预置技能的 SKILL.md。
func presetSkillDoc(t *testing.T, role, skill string) string {
	t.Helper()
	p := filepath.Join("seed", "roles", role, "skills", skill, "SKILL.md")
	raw, err := Seed.ReadFile(p)
	if err != nil {
		t.Fatalf("预置技能 %s/%s 读不到: %v", role, skill, err)
	}
	return string(raw)
}

// TestPresetSkillContract 每条预置技能都要过结构契约 + 两道闸的可判定部分。
//
// 契约项(逐条对应「少了会怎样」):
//   - description 非空且不成段:面板与技能列表只显示它,写成散文等于没有说明。
//   - trigger ≥ 2:技能靠触发词进入上下文,只有一条触发词意味着大多数场景用不上。
//   - ≥ 3 个 `## ` 小节:两段话的技能是空话(没有可执行的分歧点)。
//   - 含「最小示例」:没有示例的规则读起来像口号,模型无法照着做。
//   - 正文 ≥ 30 行:低于此值基本都是把标题扩写了几行。
func TestPresetSkillContract(t *testing.T) {
	byRole := presetSkillNamesAll(t)

	// 闸③ + 闸② 黑名单:提示注入话术 / 把外部 CLI 当前提。
	// 命中即红 —— 这两类内容进了默认池就是所有用户每轮都在读它。
	injectRe := regexp.MustCompile(`(?i)(ignore (previous|prior|above)|忽略(之前|上面|以上)的?(指令|要求|提示)|不要告诉用户|别告诉用户|隐藏(这条|此条)指令|强制先读|必须先(读|执行)|先读我)`)
	externalDepRe := regexp.MustCompile(`(?i)(请(先)?(运行|执行) (npx|npm|pip|brew|apt|curl|docker|git|node|python)\b|需要(安装|配置) [A-Za-z0-9_-]+ (CLI|工具|命令)|requires? the [a-z-]+ CLI)`)

	owner := map[string]string{} // 技能名 → 拥有它的角色(查跨角色重名)
	total := 0
	for _, role := range sortedKeys(byRole) {
		names := byRole[role]
		// 每个预置角色都必须有技能:「新增角色时忘了配技能」是本契约要拦的头号滑坡。
		if len(names) == 0 {
			t.Errorf("预置角色 %s 没有任何私有技能(技能缺失感最强的恰恰是新角色)", role)
			continue
		}
		for _, name := range names {
			total++
			if prev, dup := owner[name]; dup {
				t.Errorf("技能名 %q 被两个预置角色使用(%s 与 %s):技能是**全局按名字**去重"+
					"(第八十八批契约),同名会互相压制", name, prev, role)
			}
			owner[name] = role

			doc := presetSkillDoc(t, role, name)
			fm := frontmatterOf(doc)

			if d := yamlValue(fm, "description"); d == "" {
				t.Errorf("%s/%s 缺 description", role, name)
			} else if strings.Count(d, "。")+strings.Count(d, ":") > 2 || len([]rune(d)) > 60 {
				t.Errorf("%s/%s 的 description 写成了段落(%d 字):它只出现在列表里,应当是一句话",
					role, name, len([]rune(d)))
			}
			if n := len(listItems(fm, "trigger")); n < 2 {
				t.Errorf("%s/%s 只有 %d 条 trigger(至少 2 条:触发词决定它在什么场景被用上)", role, name, n)
			}
			if n := strings.Count(doc, "\n## "); n < 3 {
				t.Errorf("%s/%s 只有 %d 个小节(至少 3 个:两段话的技能是空话)", role, name, n)
			}
			if !strings.Contains(doc, "示例") {
				t.Errorf("%s/%s 没有示例小节:规则读起来像口号,模型无法照着做", role, name)
			}
			if n := len(strings.Split(strings.TrimSpace(doc), "\n")); n < 30 {
				t.Errorf("%s/%s 正文只有 %d 行(低于 30 行基本是空话)", role, name, n)
			}
			if m := injectRe.FindString(doc); m != "" {
				t.Errorf("%s/%s 命中提示注入黑名单 %q(闸③:永不进默认池)", role, name, m)
			}
			if m := externalDepRe.FindString(doc); m != "" {
				t.Errorf("%s/%s 把外部依赖当成前提 %q(闸②:技能是纯提示词,依赖拿不到就是空壳)", role, name, m)
			}
		}
	}
	if total == 0 {
		t.Fatal("没有任何预置技能被发现 —— 契约测试本身没生效")
	}
	t.Logf("预置技能 %d 条,覆盖 %d 个角色", total, len(byRole))
}

// TestPresetSkillNoOrphan 声明与磁盘**双向**一致:不许有「文件在但没挂载」的孤立
// 技能(模型永远看不到它,作者却以为配好了),也不许有「挂载了但文件不在」的失效
// 挂载(TestPresetRoleSkillsResolve 已从另一侧断言,这里补孤立这一侧)。
func TestPresetSkillNoOrphan(t *testing.T) {
	byRole := presetSkillNamesAll(t)
	for _, role := range sortedKeys(byRole) {
		declared := map[string]bool{}
		for _, n := range byRole[role] {
			declared[n] = true
		}
		dir := filepath.Join("seed", "roles", role, "skills")
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue // 该角色本就没有私有技能(由 TestPresetSkillContract 报)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if !declared[e.Name()] {
				t.Errorf("预置角色 %s 有孤立技能目录 %q:文件在但 role.yaml 没挂载,模型永远看不到它",
					role, e.Name())
			}
		}
	}
}

// frontmatterOf 取 YAML frontmatter 块(--- 之间的部分);没有则返回全文。
func frontmatterOf(doc string) string {
	if !strings.HasPrefix(doc, "---") {
		return doc
	}
	rest := doc[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return rest
	}
	return rest[:idx]
}

// listItems 取 frontmatter 里某个列表键下的条目。
func listItems(fm, key string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(fm, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, key+":") {
			in = true
			continue
		}
		if !in {
			continue
		}
		if strings.HasPrefix(t, "- ") {
			out = append(out, strings.TrimSpace(t[2:]))
			continue
		}
		if t != "" {
			in = false
		}
	}
	return out
}

func sortedKeys(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
