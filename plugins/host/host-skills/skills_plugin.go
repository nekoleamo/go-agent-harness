// Package hostskills 提供 host-skills 插件:技能(skill)加载机制,对齐 pi/dsc 语义。
//   - 扫描技能目录(角色私有 $GAH_HOME/roles/<id>/skills + 全局 $GAH_HOME/skills
//   - 项目 <cwd>/.gah/skills + data.dirs 扩展;顺序即优先级);
//     每个技能 = SKILL.md(YAML frontmatter: name/description/trigger + 正文);
//   - 注册两个工具:list_skills(技能索引)/ read_skill(按名读全文);
//   - 注册 system prompt 片段「可用技能索引」:模型在任务匹配触发词时按需 read_skill,
//     不全文灌提示(同 pi 的按需加载语义);
//   - 提供 ctx.skills:索引 + **可见性过滤**(角色挂载 → 切换角色即改索引与可读范围)。
package hostskills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
	skills, err := sc.Scan(append(roleSkillDirs(), dirs...)...)
	if err != nil {
		return nil, err
	}
	for _, d := range sc.Duplicates() {
		c.Logger().Warn("技能重名,已忽略后到者", "detail", d)
	}
	reg := newRegistry(dirs, skills)

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
	if err := c.Provide("ctx.skills", reg); err != nil {
		return nil, err
	}
	return func() { d1(); d2(); d3() }, nil
}

// defaultSkillDirs 静态技能目录(顺序即优先级):全局 $GAH_HOME/skills → 项目 <cwd>/.gah/skills。
// 角色私有目录是动态的(角色可随时新建/删除),由 roleSkillDirs() 在每次扫描时现算。
func defaultSkillDirs() []string {
	dirs := []string{filepath.Join(sdk.Home(), "skills")} // GAH_HOME 恒设(空仅嵌入/单测 → TempDir)
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, ".gah", "skills"))
	}
	return dirs
}

// roleSkillDirs 角色私有技能目录($GAH_HOME/roles/<id>/skills),排序保证扫描稳定。
// 为何排在静态目录**之前**:同名时角色私有技能应当压过通用库技能(用户为这个角色
// 专门写的覆盖版,期望它赢 —— 对齐 openclaw workspace 技能覆盖共享根)。
func roleSkillDirs() []string {
	dirs, err := filepath.Glob(filepath.Join(sdk.RolesDir(), "*", "skills"))
	if err != nil {
		return nil
	}
	sort.Strings(dirs) // Glob 顺序未定义:排序让扫描结果可复现
	return dirs
}

// Registry 技能表(线程安全):索引 + 可见性过滤 + 重扫。
//
// 为什么过滤器而不是重扫:角色切换是高频交互,重扫要碰磁盘且会丢掉解析结果;
// 而“哪些技能对当前角色可见”本来就是一个纯函数(角色定义 + 技能归属)。
type Registry struct {
	mu     sync.RWMutex
	skills []Skill
	dirs   []string                 // 静态目录集(启动时确定;重扫前再拼上角色私有目录)
	filter func(sdk.SkillInfo) bool // nil = 全部可见(无角色基线)
}

// newRegistry 构造技能表(dirs = 静态目录集;角色私有目录每次扫描现算)。
func newRegistry(dirs []string, skills []Skill) *Registry {
	return &Registry{dirs: append([]string(nil), dirs...), skills: skills}
}

func (r *Registry) all() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Skill(nil), r.skills...)
}

// visible 当前可见技能(过滤后;过滤为纯函数,读锁内求值)。
func (r *Registry) visible() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.filter == nil {
		return append([]Skill(nil), r.skills...)
	}
	out := make([]Skill, 0, len(r.skills))
	for _, s := range r.skills {
		if r.filter(s.Info()) {
			out = append(out, s)
		}
	}
	return out
}

func (r *Registry) find(name string) (Skill, bool) {
	for _, s := range r.all() {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// findVisible 只查可见集合(模型不得读到未挂载技能的正文;与索引可见性保持一致)。
func (r *Registry) findVisible(name string) (Skill, bool) {
	for _, s := range r.visible() {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// List 全部已加载技能(不受过滤影响;sdk.SkillsService)。
func (r *Registry) List() []sdk.SkillInfo {
	all := r.all()
	out := make([]sdk.SkillInfo, 0, len(all))
	for _, s := range all {
		out = append(out, s.Info())
	}
	return out
}

// SetFilter 设置可见性判定(sdk.SkillsService;返回撤销函数,幂等)。
func (r *Registry) SetFilter(f func(sdk.SkillInfo) bool) sdk.Disposer {
	r.mu.Lock()
	r.filter = f
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		r.filter = nil
		r.mu.Unlock()
	}
}

// Rescan 重扫技能目录(新建角色/角色私有技能/新增 SKILL.md 后调用;sdk.SkillsService)。
// 重名警告只在 Start 打(此处无 logger);重扫后重名仍按 first-wins 去重。
func (r *Registry) Rescan() error {
	r.mu.RLock()
	dirs := append([]string(nil), r.dirs...)
	r.mu.RUnlock()
	sc := &Scanner{}
	skills, err := sc.Scan(append(roleSkillDirs(), dirs...)...)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.skills = skills
	r.mu.Unlock()
	return nil
}

// IndexText 技能索引文本(system prompt 片段;只列**可见**技能)。
func (r *Registry) IndexText() string {
	all := r.visible()
	if len(all) == 0 {
		total := len(r.all())
		if total > 0 {
			return "(当前角色未挂载任何技能;当前共加载技能 " + strconv.Itoa(total) + " 个,可用 /role 查看或调整挂载)"
		}
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
	return t.reg.visible(), nil
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
	s, ok := t.reg.findVisible(a.Name)
	if !ok {
		if _, exists := t.reg.find(a.Name); exists {
			return map[string]any{"error": "技能未挂载到当前角色: " + a.Name + "(可用 list_skills 查看当前可见技能)"}, nil
		}
		return map[string]any{"error": "技能不存在: " + a.Name}, nil
	}
	return map[string]any{"name": s.Name, "description": s.Description, "content": s.Body}, nil
}
