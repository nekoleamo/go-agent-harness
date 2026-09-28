// Package hostroles 提供 host-roles 插件:角色(role/persona)切换与技能编排。
//
// 角色 = $GAH_HOME/roles/<id>/ 一个目录(role.yaml + AGENTS.md + skills/),见 internal/roles。
// 本插件负责**编排**,不做写盘细节:
//   - 提供 ctx.roles(sdk.RoleService):CRUD + 切换 + 当前角色;
//   - 身份槽片段(SlotIdentity):把「身份句 + 角色 AGENTS.md」挂在固定引导之后、指令层之前;
//   - 驱动 ctx.skills 的可见性过滤:角色挂载清单决定模型能看见/读到哪些技能;
//   - 只读工具 list_roles / read_role(模型可自查角色,**不能**自改角色);
//   - /role 命令:切换/查看/新建/删除(写操作只走人的命令与 Web 面板)。
//
// 安全边界(为什么模型没有 switch_role 工具):角色文件是**信任锚** —— 它逐字进系统提示,
// 等于一段可执行的指令。若模型能自改角色,任何不可信内容(网页/文件/搜索结果)都能升级为
// “改人格+改规则”的持久化提权。写路径另有 policy-guard 兜底:改 roles/ 或全局 AGENTS.md
// 需用户确认。
package hostroles

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-roles。requires ctx.tools/ctx.systemPrompt/ctx.skills。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-roles" }

// Start 装配角色服务、身份槽片段、只读工具、技能过滤器与 /role 命令。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		return nil, err
	}
	var skills sdk.SkillsService
	if err := c.Inject("ctx.skills", &skills); err != nil {
		return nil, err
	}

	svc := &Service{c: c, skills: skills}
	if err := c.Provide("ctx.roles", svc); err != nil {
		return nil, err
	}
	if probs := svc.Refresh(); len(probs) > 0 {
		for _, pb := range probs {
			c.Logger().Warn("角色定义不可读", "id", pb.ID, "err", pb.Err)
		}
	}

	// 只读工具:模型可自查“我现在是谁”,但没有任何写工具(见包注释的安全边界)。
	d1 := tools.Register(&listRoles{svc: svc})
	d2 := tools.Register(&readRole{svc: svc})
	// 身份槽片段:紧接固定引导渲染(Content 每轮求值 → 切换后下一轮即生效)。
	d3 := sp.AddSection(sdk.SystemPromptSection{
		Name:    "当前角色",
		Slot:    sdk.SlotIdentity,
		Content: svc.BlockText,
	})
	// 技能可见性:切换角色只改判定函数,不重扫磁盘。
	d4 := skills.SetFilter(svc.skillVisible)

	disposers := []sdk.Disposer{d1, d2, d3, d4}
	// /role 命令(可选:未装配 ctx.commands 时跳过 —— 与 host-internal-commands 同款)。
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err == nil && cmds != nil {
		if d, err := cmds.Register(newRoleCommand(svc)); err == nil {
			disposers = append(disposers, d)
		} else {
			c.Logger().Warn("/role 命令注册失败", "err", err)
		}
	}
	return func() {
		for i := len(disposers) - 1; i >= 0; i-- {
			disposers[i]()
		}
	}, nil
}

// Service 实现 sdk.RoleService(sdk.RolesPolicy / sdk.ReloadableRoles 可选扩展)。
type Service struct {
	c      sdk.Ctx
	store  roles.Store
	skills sdk.SkillsService

	mu       sync.RWMutex
	active   string                  // 当前角色缓存(事实源仍是 prefs;Refresh 同步)
	specs    map[string]sdk.RoleSpec // 全量角色缓存(Refresh 刷新)
	problems []roles.Problem         // 上次刷新遇到坏文件(可见性)
	notices  sdk.NoticeService       // 可选(ctx.notices 未装配 = nil)
}

// Refresh 重读角色定义与当前角色(磁盘为准);返回坏文件清单(不阻塞装配)。
// 逐个 Get 而不是直接用 List 的结果:List 有意**不带 AGENTS.md 正文**(列表接口别读全文),
// 而身份槽的每一轮组装都要正文 —— 缓存里就得是真身(角色几十个、每个 ≤ 32KiB,可接受)。
func (s *Service) Refresh() []roles.Problem {
	probs := s.store.Broken()
	specs := map[string]sdk.RoleSpec{}
	for _, brief := range s.store.List() {
		spec, err := s.store.Get(brief.ID)
		if err != nil {
			continue // 坏文件已进 probs
		}
		specs[spec.ID] = spec
	}
	s.mu.Lock()
	s.specs = specs
	s.active = s.store.Active()
	s.problems = probs
	active := s.active
	_, activeOK := specs[active]
	s.mu.Unlock()
	// 当前角色在磁盘上没了(外部删了目录、回滚了角色文件):不静默回落基线 ——
	// 记一条警告日志,身份槽里显式说明(见 BlockText),状态栏/面板继续显示这个 id。
	// 不改偏好:偏好是用户的状态,静默替他清掉下次他自己都查不出为什么角色没了。
	if active != "" && !activeOK {
		s.log().Warn("当前角色不存在:已按基线运行(未改动偏好)", "role", active,
			"hint", "用 /role list 查看现有角色,或 /role use <id> 重新选择")
	}
	// 第二遍才算 EffectiveSkills:visibleFor 要读 s.specs(判"这个技能归不归我"),
	// 在**旧**表上算会把刚改名/刚新建的角色自己的私有技能判成不可见(si.Role 已随
	// 目录改名,旧表里没有新 id)。代价是多一次加锁 —— Refresh 不在热路径上。
	eff := make(map[string][]string, len(specs))
	for id := range specs {
		eff[id] = s.effectiveSkills(id)
	}
	s.mu.Lock()
	for id, names := range eff {
		if sp, ok := s.specs[id]; ok {
			sp.EffectiveSkills = names
			s.specs[id] = sp
		}
	}
	s.mu.Unlock()
	return probs
}

// Problems 上次刷新遇到的坏角色文件(Web/TUI 可见性用)。
func (s *Service) Problems() []roles.Problem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]roles.Problem(nil), s.problems...)
}

// —— sdk.RoleService ——

// List 全部角色(缓存视图,已派生 EffectiveSkills)。
func (s *Service) List() []sdk.RoleSpec {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]sdk.RoleSpec, 0, len(s.specs))
	for _, spec := range s.specs {
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get 取单个角色(缓存;正文也在缓存里)。
func (s *Service) Get(id string) (sdk.RoleSpec, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	spec, ok := s.specs[id]
	return spec, ok
}

// Current 当前角色 ID("" = 未启用角色)。
func (s *Service) Current() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.active
}

// Use 切换角色("" 或 "none" = 停用)。
//
// 五步纪律(对齐 openclaw 社区 personality-switcher 的教训:切一半的装态比不切更糟):
//  1. 校验目标存在(不改任何状态);
//  2. 写偏好(原子);
//  3. 重读磁盘刷新缓存;
//  4. 回读校验(目标确实可用);
//  5. 任一步失败 → 恢复原偏好并刷新,错误如实上报。
//
// **不换会话**:历史与工作区都不动,只有下一轮的系统提示与技能可见集合变化。
func (s *Service) Use(id string) error {
	if id == "none" || id == "off" {
		id = ""
	}
	if id != "" {
		if _, ok := s.Get(id); !ok {
			return fmt.Errorf("角色不存在:%s(可用 /role list 查看)", id)
		}
	}
	prev := s.store.Active()
	if prev == id {
		return nil // 幂等:同角色重复切换不动状态、不发提示
	}
	if err := s.store.SetActive(id); err != nil {
		return err
	}
	rollback := func(cause error) error {
		if err := s.store.SetActive(prev); err != nil {
			return fmt.Errorf("切换失败(%v),且回滚失败: %w", cause, err)
		}
		s.Refresh()
		return cause
	}
	if probs := s.Refresh(); len(probs) > 0 {
		for _, pb := range probs {
			if pb.ID == id {
				return rollback(fmt.Errorf("角色 %s 定义不可读:%s", id, pb.Err))
			}
		}
	}
	if id != "" {
		if _, ok := s.Get(id); !ok {
			return rollback(fmt.Errorf("角色 %s 在切换后不可读", id))
		}
	}
	// 角色私有技能可能刚被放进磁盘:重扫一次让它们可见(失败不阻断切换)。
	if err := s.skills.Rescan(); err != nil {
		s.log().Warn("技能重扫失败", "err", err)
	}
	// 迁移期刷新缓存里的 EffectiveSkills(重扫后技能集合可能变了)。
	s.Refresh()
	s.notifySwitch(id)
	return nil
}

// Create 新建角色(缺省给一份可改写的规则模板)。
func (s *Service) Create(spec sdk.RoleSpec, agents string) (sdk.RoleSpec, error) {
	if spec.Name == "" {
		spec.Name = spec.ID
	}
	if agents == "" {
		agents = AgentsTemplate(spec.Name)
	}
	// 重扫在前:挂载清单可能引用刚放进磁盘的技能(启动后新增的技能不重扫就看不到)。
	if err := s.skills.Rescan(); err != nil {
		s.log().Warn("技能重扫失败", "err", err)
	}
	if err := s.validateSkills(nil, spec); err != nil { // 新建:全部按新增严格校验
		return sdk.RoleSpec{}, err
	}
	if err := s.store.Create(spec, agents); err != nil {
		return sdk.RoleSpec{}, err
	}
	s.Refresh()
	return s.mustGet(spec.ID)
}

// Update 更新角色定义(ID 不可改;技能清单/身份句/显示名都可改)。
func (s *Service) Update(id string, spec sdk.RoleSpec) (sdk.RoleSpec, error) {
	prev, ok := s.Get(id) // 改动前的定义:挂载校验要拿它区分「新增」与「存量悬空」
	if !ok {
		return sdk.RoleSpec{}, fmt.Errorf("角色不存在:%s", id)
	}
	spec.ID = id
	// 技能名必须存在:显式失败而非静默丢掉一个挂载(静默会让用户以为已生效)。
	if err := s.skills.Rescan(); err != nil {
		s.log().Warn("技能重扫失败", "err", err)
	}
	if err := s.validateSkills(&prev, spec); err != nil {
		return sdk.RoleSpec{}, err
	}
	if err := s.store.Save(spec); err != nil {
		return sdk.RoleSpec{}, err
	}
	s.Refresh()
	// 当前角色被改 → EffectiveSkills 变了,刷新即可(过滤函数实时读缓存)。
	return s.mustGet(id)
}

// SetAgents 写角色 AGENTS.md。
func (s *Service) SetAgents(id, text string) error {
	if _, ok := s.Get(id); !ok {
		return fmt.Errorf("角色不存在:%s", id)
	}
	if err := s.store.SetAgents(id, text); err != nil {
		return err
	}
	s.Refresh()
	return nil
}

// Rename 改 ID / 显示名。
//
// 必须**重扫技能索引**:角色私有技能目录随角色目录一起改名(roles/<旧>/skills →
// roles/<新>/skills),而索引里那些技能的归属(roleOf 取路径段)还停在旧 id ⇒
// 重命名后该角色**自己的**私有技能会对它自己不可见(visibleFor: si.Role != roleID),
// 直到下一次重扫才恢复 —— 实测踩到。
func (s *Service) Rename(id, newID, newName string) (sdk.RoleSpec, error) {
	spec, err := s.store.Rename(id, newID, newName)
	if err != nil {
		return sdk.RoleSpec{}, err
	}
	if err := s.skills.Rescan(); err != nil {
		s.log().Warn("技能重扫失败", "err", err)
	}
	s.Refresh()
	return s.mustGet(spec.ID)
}

// Delete 删除角色(移入回收站;当前角色拒绝)。
//
// 同样要重扫:整个角色目录(含 skills/)移进 .trash,索引若不重扫,那些技能会一直
// 算「已加载」—— 面板技能库继续列出一条属于已删角色的技能,甚至能按旧 id 写回去。
func (s *Service) Delete(id string) error {
	if err := s.store.Delete(id); err != nil {
		return err
	}
	if err := s.skills.Rescan(); err != nil {
		s.log().Warn("技能重扫失败", "err", err)
	}
	s.Refresh()
	return nil
}

// Reload 重读全部角色定义(sdk.ReloadableRoles;/reload 连带)。
func (s *Service) Reload() error {
	if probs := s.Refresh(); len(probs) > 0 {
		for _, pb := range probs {
			s.log().Warn("角色定义不可读", "id", pb.ID, "err", pb.Err)
		}
	}
	return s.skills.Rescan()
}

// ReloadRoles 实现 sdk.ReloadableRoles(/reload 连带角色)。
func (s *Service) ReloadRoles() error { return s.Reload() }

// MaxAgentsBytes 单个角色 AGENTS.md 字节上限。
func (s *Service) MaxAgentsBytes() int { return roles.MaxAgentsBytes }

// InheritGlobalInstructions 实现 sdk.RolesPolicy:
// 无角色或角色未 declare exclude_global → true(全局 AGENTS.md 照常注入)。
func (s *Service) InheritGlobalInstructions() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active == "" {
		return true
	}
	spec, ok := s.specs[s.active]
	if !ok {
		return true // 角色读不到:按基线放行(不因坏文件悄悄少一层指令)
	}
	return !spec.ExcludeGlobal
}

// BlockText 身份槽内容(每轮组装求值;无当前角色 = 空 → 不产出块)。
func (s *Service) BlockText() string {
	s.mu.RLock()
	active := s.active
	spec, ok := s.specs[active]
	s.mu.RUnlock()
	if active == "" {
		return ""
	}
	if !ok {
		// 悬空当前角色(定义没了):显式说明已回落基线,别让模型/用户以为人设还在生效
		// (与「缺失依赖显式失败,不静默降级」同一条纪律)。
		return "当前角色:" + active + "\n" +
			"(找不到这个角色的定义:本轮按基线运行 —— 只有全局与项目指令;" +
			"请用 /role list 查看现有角色,或用 /role use <id> 重新选择)"
	}
	var sb strings.Builder
	sb.WriteString("当前角色:")
	sb.WriteString(spec.Name)
	sb.WriteString("(")
	sb.WriteString(spec.ID)
	sb.WriteString(")\n")
	if id := strings.TrimSpace(spec.Identity); id != "" {
		sb.WriteString(id)
		sb.WriteString("\n")
	}
	if body := strings.TrimSpace(spec.AGENTS); body != "" {
		sb.WriteString("\n工作规则:\n")
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	sb.WriteString("\n(角色设定;与本轮用户指令或项目规则冲突时,以后者为准)")
	text, cut := roles.Shrink(sb.String())
	if cut {
		text += "\n(角色说明超过 " + fmt.Sprint(roles.MaxAgentsBytes) + " 字节上限,已截断)"
	}
	return text
}

// —— 技能可见性 ——

// skillVisible 过滤判定(装配给 ctx.skills;每次求值实时读当前角色,故切换即生效)。
func (s *Service) skillVisible(si sdk.SkillInfo) bool {
	return s.visibleFor(s.Current(), si)
}

// visibleFor roleID 视角下 si 是否可见:
//   - 别人的私有技能:永不可见;
//   - 无角色:只有共享库(角色私有技能不泄漏到基线);
//   - 自己的私有技能:始终可见(与挂载清单无关);
//   - 共享技能:未写 skills 键 = 默认池全给;显式清单 = **替换**(除非 skills_inherit)。
func (s *Service) visibleFor(roleID string, si sdk.SkillInfo) bool {
	if si.Role != "" && si.Role != roleID {
		return false
	}
	if roleID == "" {
		return si.Role == ""
	}
	s.mu.RLock()
	spec, ok := s.specs[roleID]
	s.mu.RUnlock()
	if !ok {
		return si.Role == ""
	}
	if si.Role == roleID {
		return true // 自己的私有技能:只增不减
	}
	if !spec.SkillsSet || spec.SkillsInherit {
		return true
	}
	for _, n := range spec.Skills {
		if n == si.Name {
			return true
		}
	}
	return false
}

// effectiveSkills 角色生效技能名(UI 展示「切换后能看到什么」)。
func (s *Service) effectiveSkills(roleID string) []string {
	all := s.skills.List()
	var out []string
	for _, si := range all {
		if s.visibleFor(roleID, si) {
			out = append(out, si.Name)
		}
	}
	sort.Strings(out)
	return out
}

// Skills 全部已加载技能索引(面板勾选用;含归属角色)。
func (s *Service) Skills() []sdk.SkillInfo { return s.skills.List() }

// validateSkills 校验挂载清单:**本次新增**的技能名必须存在于技能库(或为角色私有技能),
// 否则显式失败。existing = 改动前的定义(nil = 新建,全部按新增严格校验);
// **改动前就有的悬空名放行** —— 技能被删/改名后那条挂载就成了库里的未知名,若一律拒,
// 这个角色的任何一次保存(改显示名、改人设、甚至把那条挂载取消掉)都会 400
// 「技能不存在」,面板从此编辑不了它(实测踩到)。严进宽出:新增严格,存量允许清理。
func (s *Service) validateSkills(existing *sdk.RoleSpec, spec sdk.RoleSpec) error {
	if !spec.SkillsSet || len(spec.Skills) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, si := range s.skills.List() {
		if si.Role == "" || si.Role == spec.ID {
			known[si.Name] = true
		}
	}
	stale := map[string]bool{}
	if existing != nil {
		for _, n := range existing.Skills {
			stale[n] = true
		}
	}
	for _, n := range spec.Skills {
		if !known[n] && !stale[n] {
			return fmt.Errorf("技能不存在:%s(SkillsInherit/挂载只能引用已加载的技能)", n)
		}
	}
	return nil
}

// —— 内部工具 ——

func (s *Service) mustGet(id string) (sdk.RoleSpec, error) {
	if spec, ok := s.Get(id); ok {
		return spec, nil
	}
	return sdk.RoleSpec{}, fmt.Errorf("角色不可读:%s", id)
}

func (s *Service) log() *slog.Logger { return s.c.Logger() }

// notifySwitch 切换后发一条用户提示(可选通道):
// 讲清两件事 —— 提示缓存失效(首轮变慢)、会话不换(与 /workspace 的差异)。
func (s *Service) notifySwitch(id string) {
	s.mu.RLock()
	nt := s.notices
	s.mu.RUnlock()
	if nt == nil {
		var dep sdk.NoticeService
		if err := s.c.Inject("ctx.notices", &dep); err != nil {
			return
		}
		nt = dep
		s.mu.Lock()
		s.notices = dep
		s.mu.Unlock()
	}
	title, body := "已停用角色(回到基线)", "系统提示与技能索引已重建(下一轮生效);会话与历史不变。Prompt 缓存失效,首轮可能变慢。"
	if id != "" {
		name := id
		if spec, ok := s.Get(id); ok {
			name = spec.Name
		}
		title = "已切换到角色:" + name
	}
	nt.Publish(sdk.Notice{
		Level: sdk.NoticeInfo, Source: "host-roles", Key: "role-switch",
		Title: title, Body: body,
	})
}

// AgentsTemplate 新建角色时的 AGENTS.md 初始内容(可执行动作,不写空话)。
func AgentsTemplate(name string) string {
	return "# " + name + " 的工作规则\n\n" +
		"## 先做什么\n- 先确认任务目标与交付物,信息不足时一次问清(不要边猜边做)。\n\n" +
		"## 输出格式\n- 结论在前,依据在后;数字与来源必须可追溯。\n\n" +
		"## 不做的事\n- 不编造数据、引用或文件内容。\n"
}
