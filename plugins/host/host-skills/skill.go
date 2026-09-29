// Skill 类型与扫描解析(host-skills 插件)。
package hostskills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Skill 一个已加载的技能。
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Triggers    []string `json:"triggers,omitempty"`
	// Role 归属角色 ID(空 = 共享技能库)。角色私有技能只对其归属角色可见
	// (可见性由 Registry 过滤器实现,不在扫描期丢弃 —— 不然切换角色就得重扫磁盘)。
	Role string `json:"role,omitempty"`
	Path string `json:"-"`
	Body string `json:"-"`
}

// Info 转 SDK 索引视图(跨插件传递不带正文/路径)。
func (s Skill) Info() sdk.SkillInfo {
	return sdk.SkillInfo{Name: s.Name, Description: s.Description, Triggers: s.Triggers, Role: s.Role}
}

// Scanner 扫描目录树中的 SKILL.md。
type Scanner struct{ dups []string }

// Duplicates 扫描期因重名被忽略的技能(形如 "name: 保留路径 ← 忽略路径")。
// 为什么要它:重名 first-wins 是**静默**行为 —— 插件没有日志就把这件事藏了,
// 而“我明明写了这个技能它却没生效”极难排查(尤其角色私有技能撞库名)。
func (s *Scanner) Duplicates() []string { return s.dups }

// Scan 返回找到的技能(按名称排序;目录不存在/坏文件静默跳过;重名 first-wins)。
func (s *Scanner) Scan(dirs ...string) ([]Skill, error) {
	var found []string
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // 目录不存在/无权限:跳过
			}
			// 点开头的目录一律不进:技能回收站(.trash:删除的技能移到这里,留着可恢复)、
			// .git、编辑器临时目录 —— 否则“删掉的技能还在索引里”。根目录本身除外。
			if d.IsDir() && path != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if d.IsDir() || d.Name() != "SKILL.md" {
				return nil
			}
			found = append(found, path)
			return nil
		})
	}
	seen := map[string]string{} // name → path(重名保留先出现者:角色私有 → 全局 → 项目)
	var out []Skill
	for _, path := range found {
		skill, err := parseSkill(path)
		if err != nil {
			continue
		}
		if prev, dup := seen[skill.Name]; dup {
			s.dups = append(s.dups, skill.Name+": 保留 "+prev+" ← 忽略 "+path)
			continue
		}
		seen[skill.Name] = path
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// roleOf 从技能文件路径推断归属角色:$GAH_HOME/roles/<id>/skills/<名>/SKILL.md → <id>。
// 布局锚点取自 sdk.RolesDir()(与 internal/roles 同一事实源,避免两处各拼一份路径)。
func roleOf(path string) string {
	rel, err := filepath.Rel(sdk.RolesDir(), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	parts := strings.Split(rel, string(filepath.Separator))
	// ≥3 段即可:roles/<id>/skills/<名>/SKILL.md 是正常布局,但 SKILL.md 直接放在
	// roles/<id>/skills/ 下(手写/搬运)也是这个角色的私有技能 —— 用 >=4 会把它
	// 判成共享技能,私有面就泄到基线与其它角色了。
	if len(parts) >= 3 && parts[1] == "skills" {
		return parts[0]
	}
	return ""
}

// parseSkill 解析 SKILL.md(frontmatter + 正文)。
func parseSkill(path string) (Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	s := Skill{Path: path}
	s.Name = filepath.Base(filepath.Dir(path))
	body := string(raw)
	if strings.HasPrefix(body, "---") {
		rest := body[3:]
		if end := strings.Index(rest, "---"); end >= 0 {
			var meta struct {
				Name        string   `yaml:"name"`
				Description string   `yaml:"description"`
				Trigger     []string `yaml:"trigger"`
			}
			if yerr := yaml.Unmarshal([]byte(rest[:end]), &meta); yerr == nil {
				if meta.Name != "" {
					s.Name = meta.Name
				}
				s.Description = meta.Description
				s.Triggers = meta.Trigger
			}
			body = strings.TrimPrefix(rest[end+3:], "\n")
		}
	}
	s.Body = strings.TrimSpace(body)
	s.Role = roleOf(path)
	return s, nil
}
