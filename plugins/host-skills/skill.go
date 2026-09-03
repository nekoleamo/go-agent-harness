// Skill 类型与扫描解析(host-skills 插件)。
package hostskills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Skill 一个已加载的技能。
type Skill struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Triggers    []string `json:"triggers,omitempty"`
	Path        string   `json:"-"`
	Body        string   `json:"-"`
}

// Scanner 扫描目录树中的 SKILL.md。
type Scanner struct{}

// Scan 返回找到的技能(按名称排序;目录不存在/坏文件静默跳过)。
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
			if d.IsDir() || d.Name() != "SKILL.md" {
				return nil
			}
			found = append(found, path)
			return nil
		})
	}
	seen := map[string]string{} // name → path(重名保留先出现者:全局优先)
	var out []Skill
	for _, path := range found {
		skill, err := parseSkill(path)
		if err != nil {
			continue
		}
		if _, dup := seen[skill.Name]; dup {
			continue
		}
		seen[skill.Name] = path
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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
	return s, nil
}
