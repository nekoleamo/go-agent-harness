// Package hostskills 提供 host-skills 插件:技能(skill)加载机制,对齐 pi/dsc 语义。
//  - 扫描技能目录(全局 $GAH_HOME/skills + 项目 <cwd>/.gah/skills + data.dirs 扩展);
//    每个技能 = SKILL.md(YAML frontmatter: name/description/trigger + 正文);
//  - 注册两个工具:list_skills(技能索引)/ read_skill(按名读全文);
//  - 注册 system prompt 片段「可用技能索引」:模型在任务匹配触发词时按需 read_skill,
//    不全文灌提示(同 pi 的按需加载语义)。
package hostskills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-skills。requires ctx.tools/ctx.systemPrompt。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-skills" }

// Start 扫描技能目录并注册工具与提示索引。
// data.dirs: 附加技能目录(绝对路径列表)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	dirs := defaultSkillDirs()
	if m != nil && m.Data != nil {
		if xs, ok := m.Data["dirs"].([]any); ok {
			for _, x := range xs {
				if s, ok := x.(string); ok {
					dirs = append(dirs, s)
				}
			}
		}
	}
	sc := &Scanner{}
	skills, err := sc.Scan(dirs...)
	if err != nil {
		return nil, err
	}
	reg := &Registry{skills: skills}

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		return nil, err
	}
	d1 := tools.Register(&listSkills{reg: reg})
	d2 := tools.Register(&readSkill{reg: reg})
	d3 := sp.AddSection(sdk.SystemPromptSection{
		Name:    "可用技能",
		Content: func() string { return reg.IndexText() },
	})
	return func() { d1(); d2(); d3() }, nil
}

// defaultSkillDirs 全局 + 项目技能目录。
func defaultSkillDirs() []string {
	var dirs []string
	home := os.Getenv("GAH_HOME")
	if home == "" {
		if uh, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(uh, ".gah")
		}
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "skills"))
	}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, ".gah", "skills"))
	}
	return dirs
}

// Registry 技能表(线程安全)。
type Registry struct {
	mu     sync.RWMutex
	skills []Skill
}

func (r *Registry) all() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Skill(nil), r.skills...)
}

func (r *Registry) find(name string) (Skill, bool) {
	for _, s := range r.all() {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// IndexText 技能索引文本(system prompt 片段)。
func (r *Registry) IndexText() string {
	all := r.all()
	if len(all) == 0 {
		return "(无可用技能;缺省目录 $GAH_HOME/skills 与 <cwd>/.gah/skills)"
	}
	var sb strings.Builder
	sb.WriteString("任务匹配以下技能描述/触发词时,先行调用 read_skill 读取全文再执行:\n")
	for _, s := range all {
		sb.WriteString(fmt.Sprintf("- %s: %s", s.Name, s.Description))
		if len(s.Triggers) > 0 {
			sb.WriteString(" 触发: " + strings.Join(s.Triggers, ","))
		}
		sb.WriteString("\n")
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// listSkills 工具:列出技能索引。
type listSkills struct {
	reg *Registry
}

func (t *listSkills) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "list_skills",
		Description: "列出已加载的技能索引(名称/描述/触发词),判断任务是否需要读取。",
		InputSchema: map[string]any{"type": "object"},
	}
}

func (t *listSkills) Execute(ctx context.Context, args string) (any, error) {
	return t.reg.all(), nil
}

// readSkill 工具:按名读技能全文。
type readSkill struct {
	reg *Registry
}

func (t *readSkill) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "read_skill",
		Description: "读取一个技能的全文(SKILL.md),调用前先用 list_skills 确认。",
		InputSchema: map[string]any{
			"type":       "object",
			"required":   []any{"name"},
			"properties": map[string]any{"name": map[string]any{"type": "string", "description": "技能名"}},
		},
	}
}

func (t *readSkill) Execute(ctx context.Context, args string) (any, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return nil, fmt.Errorf("read_skill: args: %w", err)
	}
	s, ok := t.reg.find(a.Name)
	if !ok {
		return map[string]any{"error": "技能不存在: " + a.Name}, nil
	}
	return map[string]any{"name": s.Name, "description": s.Description, "content": s.Body}, nil
}
