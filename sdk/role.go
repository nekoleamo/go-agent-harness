// role.go:角色(role/persona)与技能索引的对外契约。
//
// 角色 = 一个目录($GAH_HOME/roles/<id>/)里的身份句 + AGENTS.md + 技能挂载清单,
// 由 host-roles 提供服务(ctx.roles);技能索引/过滤由 host-skills 提供(ctx.skills)。
// 为什么两者分开:技能插件不认识"角色"这个概念(单独装配也不炸),角色插件去驱动技能过滤;
// 依赖方向是「角色知道技能」,不是反的。
package sdk

// RoleSpec 一个角色的定义(role.yaml 的可序列化视图 + 派生字段)。
type RoleSpec struct {
	ID          string `json:"id"`                    // 目录名(唯一,受校验:[a-z0-9][a-z0-9-]{0,31})
	Name        string `json:"name"`                  // 显示名(空 = 用 ID)
	Description string `json:"description,omitempty"` // 一句话定位(列表展示)
	// Identity 身份句:进「身份槽」(固定引导之后、指令层之前),回答"你是谁"。
	Identity string `json:"identity,omitempty"`
	// ExcludeGlobal 置真 = **不注入**全局指令($GAH_HOME/AGENTS.md)。
	// 为什么是反向布尔(opt-out)而不是 inherit_global(opt-in):Go/JSON 的零值都是 false,
	// 正着写会让"没写这个键"与"显式关闭"无法区分(缺省必须 = 保留全局指令)。
	// 典型用法:非开发角色(财务/小说家)不想背上满屏编码规范。
	ExcludeGlobal bool `json:"exclude_global,omitempty"`
	// Skills 挂载清单(库里的技能名)。语义(对齐 openclaw):显式列表**替换**默认池,
	// 除非 SkillsInherit 为真(再并上默认池)。SkillsSet 区分"没写这个键"(= 默认池全给)
	// 与"写了 []"(= 一个都不挂)。
	Skills        []string `json:"skills,omitempty"`
	SkillsSet     bool     `json:"skills_set"`
	SkillsInherit bool     `json:"skills_inherit,omitempty"`
	// AGENTS 角色工作规则正文(roles/<id>/AGENTS.md);AGENTSBytes 为其字节数(上限校验/展示用)。
	AGENTS      string `json:"agents,omitempty"`
	AGENTSBytes int    `json:"agents_bytes"`
	// OwnSkills 角色私有目录 roles/<id>/skills/ 下发现的技能名(只读挂载,始终生效)。
	OwnSkills []string `json:"own_skills,omitempty"`
	// EffectiveSkills 切换到这个角色后**实际可见**的技能名(由服务派生,不落盘;UI 展示用)。
	EffectiveSkills []string `json:"effective_skills,omitempty"`
	// Seed 该角色是否来自预置 seed(仅展示:预置同样可改可删)。
	Seed bool `json:"seed,omitempty"`
}

// RoleService 服务(ctx.roles):角色定义、当前角色与切换(host-roles 提供)。
//
// 写操作只由人经命令/UI 发起 —— **不提供模型可见的切换工具**:模型自主改人格等于把
// 不可信内容(网页/文件)变成提权入口。模型侧只有只读的 list_roles / read_role。
type RoleService interface {
	// List 全部角色(按显示名排序)与当前角色(Current)。
	List() []RoleSpec
	// Get 取单个角色。
	Get(id string) (RoleSpec, bool)
	// Current 当前角色 ID("" = 未启用角色,即基线行为)。
	Current() string
	// Use 切换角色("" = 停用角色回到基线)。实现须保证:校验失败不改任何状态。
	Use(id string) error
	// Create 新建角色(agents 为 AGENTS.md 正文;缺省给空模板)。ID 重复显式失败。
	Create(spec RoleSpec, agents string) (RoleSpec, error)
	// Update 更新角色定义(整体替换 role.yaml 的可编辑字段;ID 不可改)。
	Update(id string, spec RoleSpec) (RoleSpec, error)
	// SetAgents 写角色 AGENTS.md(原子写;超上限显式失败)。
	SetAgents(id, text string) error
	// Rename 改 ID(目录改名)/ 或仅改显示名(新 ID 与旧 ID 相同 = 只改显示名)。
	Rename(id, newID, newName string) (RoleSpec, error)
	// Delete 删除角色(移入 roles/.trash/);当前角色与"空目录"场景显式拒绝。
	Delete(id string) error
	// Reload 重读全部角色定义(/reload 连带;外部编辑 roles/*/AGENTS.md 后即生效)。
	Reload() error
	// MaxAgentsBytes 单个角色 AGENTS.md 的字节上限(超限拒绝保存/注入时截断)。
	MaxAgentsBytes() int
}

// RolesPolicy 可选扩展:角色对「指令层」的影响(ctx.roles 实现者按需实现)。
// 独立的窄接口而非塞进 RoleService:消费方(host-system-prompt)只需要这一个问题的答案,
// 不该为了它把整个角色服务绑成硬依赖(缺角色插件时行为必须与今天完全一致)。
type RolesPolicy interface {
	// InheritGlobalInstructions 当前是否允许注入全局指令(AGENTS.md)。
	// 语义:无角色 / 角色未 declare exclude_global → true;角色显式排除 → false。
	InheritGlobalInstructions() bool
}

// ReloadableRoles 可选扩展:角色定义热重载(/reload 连带)。
type ReloadableRoles interface {
	ReloadRoles() error
}

// SkillInfo 一个已加载技能的索引视图(不含正文 —— 正文仍由 read_skill 按需取)。
type SkillInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Triggers    []string `json:"triggers,omitempty"`
	// Role 归属角色 ID(空 = 共享技能库)。角色私有技能只能被其归属角色看到。
	Role string `json:"role,omitempty"`
}

// SkillsService 服务(ctx.skills):技能索引与**可见性过滤**(host-skills 提供)。
//
// 过滤而非重扫:切换角色只改一个判定函数,不必重读磁盘、不重建注册表。
// 过滤对三处同时生效:系统提示的「可用技能」片段、list_skills、read_skill ——
// 未挂载的技能对模型既不可见也不可读(显式报"未挂载",不静默返回空正文)。
type SkillsService interface {
	// List 全部已加载技能(不受过滤影响;UI 勾选/诊断用)。
	List() []SkillInfo
	// SetFilter 设置可见性判定(返回 Disposer 撤销;幂等,重复设置以后者为准)。
	SetFilter(f func(SkillInfo) bool) Disposer
	// Rescan 重扫技能目录(新建角色私有技能/新增 SKILL.md 后调用)。
	Rescan() error
}
