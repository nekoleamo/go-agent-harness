// role.go:角色(role/persona)与技能索引的对外契约。
//
// 角色 = 一个目录($GAH_HOME/roles/<id>/)里的身份句 + AGENTS.md + 技能挂载清单,
// 由 host-roles 提供服务(ctx.roles);技能索引/过滤由 host-skills 提供(ctx.skills)。
// 为什么两者分开:技能插件不认识"角色"这个概念(单独装配也不炸),角色插件去驱动技能过滤;
// 依赖方向是「角色知道技能」,不是反的。
package sdk

import (
	"fmt"
	"strings"
)

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
	// Model / Thinking 角色携带的模型与思考档(空 = 跟随会话)。
	// 与 skills 不同,**不需要 Set 标记**:空串就是"跟随会话",没有"显式空"这种独立语义。
	// 真正生效的判据在 SDK 纯函数 EffectiveModel/EffectiveThinking(运行期注入与三端展示同一处),
	// 而**不**进系统提示 —— 模型不需要知道自己在哪个模型上跑。
	Model    string `json:"model,omitempty"`
	Thinking string `json:"thinking,omitempty"`
	// ToolsExclude 角色**排除**的工具名(第九十一批)。
	// 为什么是排除清单而不是白名单(与 Skills 的"挂载清单"方向相反):工具面随插件装卸
	// **频繁变化** —— 白名单会让新装的插件对老角色静默不可见,用户看到的是"这个角色莫名
	// 少了个工具",归因困难;排除清单的缺省(不写这个键)永远是"全部工具",能力面只增不减。
	// 与 model/thinking 同款:**不需要 Set 标记** —— 空清单与不写这个键行为一致(都不排除)。
	// 名字不存在不报错(工具可能被卸载):悬空名由 UI 标注,后端只校验名字的**形状**。
	ToolsExclude []string `json:"tools_exclude,omitempty"`
	// Approval / Sandbox 角色**收紧**档(第九十二批;空 = 随全局)。
	//
	// 安全口径(不可动摇):生效档 = 更严(全局声明档 → 审批档联动 → 角色收紧),
	// 角色档是**下限**,任何路径都不得让它比全局更松 —— 所以角色可写的值只有
	// 比缺省更严的那些(见 NormalizeRoleApproval/NormalizeRoleSandbox)。
	// 方向为什么是"只能收紧":放宽 = 提权面(切个角色就把 open + full-access 打开),
	// 那必须配显式开关 + 二次确认 + 审批面登记,不在本批范围。
	// 与 Model/Thinking 同款:不需要 Set 标记(空 = 跟随全局,没有"显式空"语义)。
	Approval string `json:"approval,omitempty"`
	Sandbox  string `json:"sandbox,omitempty"`
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

// —— 生效值派生(运行期注入与三端展示的**单一判据**) ——
//
// 为什么必须收在一处:同一件事有三个消费者 —— `host-roles` 往请求里填值(真正生效的),
// TUI 输入行右侧、Web `/api/state`+设置面板(展示给人看的)。各写一份判定就必然漂:
// 典型病征是"显示跟随会话、实际用角色模型",而且是那种没人报的错。
// 本函数无副作用、不读盘、不依赖服务(纯函数才能被三处共用)。

// 生效值来源(展示端据此标注"角色指定";模型与思考档共用同一对)。
const (
	// SourceRole 值来自当前角色的声明;SourceSession 值来自会话档(角色未声明)。
	SourceRole    = "role"
	SourceSession = "session"
)

// ToolVisible 判定一个工具名在当前角色下是否**可见/可调**(单一判据:运行期过滤与
// 三端展示共用 —— 各写一份判定就必然漂成"界面说能用、实际被拒")。
// role 为 nil(基线/未启用角色)或未声明排除清单 ⇒ 一律放行。
func ToolVisible(role *RoleSpec, name string) bool {
	if role == nil || len(role.ToolsExclude) == 0 {
		return true
	}
	for _, n := range role.ToolsExclude {
		if n == name {
			return false
		}
	}
	return true
}

// NormalizeToolNames 规范化并校验一组工具名的形状(去空白、去重保序)。
// 与 NormalizeThinking 同款纪律:坏值**显式失败**,不静默丢弃 —— 悄悄删掉用户写的
// 条目等于给出一个与实际不符的角色定义。
// 只校验形状(**不查存在性**):internal/roles 不依赖 ctx.tools(分层纪律),
// 而且插件装卸是常态,悬空名不该让整个角色变成坏角色。
func NormalizeToolNames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, raw := range names {
		n := strings.TrimSpace(raw)
		switch {
		case n == "":
			return nil, fmt.Errorf("工具名不能为空(排除清单里第 %d 项)", len(out)+1)
		case len(n) > MaxToolNameLen:
			return nil, fmt.Errorf("工具名过长(≤ %d 字符):%q", MaxToolNameLen, n)
		case strings.ContainsFunc(n, func(r rune) bool { return r <= ' ' || r == 0x7f }):
			return nil, fmt.Errorf("工具名不能含空白/控制字符:%q", n)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

// MaxToolNameLen 工具名长度上限(形状校验用;现有工具名最长约 30 字符,留足余量)。
const MaxToolNameLen = 64

// EffectiveModel 派生"本回合实际使用的模型"及其来源。
// 语义:角色声明了 model(且角色存在)→ 用它;否则用会话模型。
func EffectiveModel(sessionModel string, role *RoleSpec) (model, src string) {
	if role != nil && role.Model != "" {
		return role.Model, SourceRole
	}
	return sessionModel, SourceSession
}

// EffectiveThinking 派生"本回合实际使用的思考档"及其来源。
// sessionThinking 为会话档名(如 `high`);返回的 level 为 0(Off) 时 src 仍如实标注来源。
// 注意与运行期的差别:真正注入时角色声明的档还要置 `LLMRequest.ThinkingSet`(让显式 `off` 能压过会话档),
// 那是**注入侧的事**(见 host-llm/host-roles);本函数只回答"最终是什么、来自哪"。
func EffectiveThinking(sessionThinking string, role *RoleSpec) (level ThinkingLevel, src string) {
	if role != nil && role.Thinking != "" {
		return ParseThinking(role.Thinking), SourceRole
	}
	return ParseThinking(sessionThinking), SourceSession
}

// —— 角色档收紧(第九十二批):运行期裁决与三端展示**共用的唯一判据** ——
//
// 为什么也要收在一处:合成链有三段(全局声明档 → 审批档联动 → 角色收紧),而消费者有
// 四类(沙箱策略器/审批策略器/TUI 状态栏/命令回执)。各写一份就必然漂成
// "界面说 read-only、实际 full-access" —— 那正是安全面最不该出现的错。

// 档位偏离来源(策略器实现 EffectiveSource 时报告:有效档为何不等于声明档)。
const (
	// TierSourceApproval 审批档联动所致(open → 沙箱 full-access;strict → read-only)。
	TierSourceApproval = "approval"
	// TierSourceRole 当前角色收紧所致。
	TierSourceRole = "role"
)

// EffectiveSource 可选能力:报告"有效档为何不等于声明档"(沙箱与审批策略器都实现)。
//
// 为什么需要它:三端展示要么写死"联动所致的偏差"(角色收紧时就成了假话),
// 要么各自重算一遍合成链(必然漂)。问策略器本身,它与实际拦截行为同源。
// 未实现该接口 = 没有"偏离"概念(旧实现/测试替身),展示端按无偏离处理。
type EffectiveSource interface {
	// EffectiveFrom 返回偏离来源;"" = 有效档 == 声明档(展示端显示"有效一致")。
	EffectiveFrom() string
}

// approvalRank / sandboxRank 松紧序,**统一成"越大越严"**:合成取更严者。
// 方向坑:审批档的自然序恰好与松紧一致(open<smart<strict),沙箱档**相反**
// (read-only 最严、full-access 最松)—— 直接拿自然序比较会把角色档当成"更严"
// 而实际放宽全局(单测已钉住:全局 read-only + 角色 workspace-write 必须仍是 read-only)。
var approvalRank = map[string]int{string(ApprovalOpen): 0, string(ApprovalSmart): 1, string(ApprovalStrict): 2}
var sandboxRank = map[string]int{string(SandboxReadOnly): 2, string(SandboxWorkspace): 1, string(SandboxFullAccess): 0}

// NormalizeRoleApproval 归一角色声明的审批档(空 = 不收紧)。
// 只接受 smart / strict:open 是**放宽**,角色无权声明(见 RoleSpec.Approval 的说明)。
// 与 NormalizeThinking 同款纪律:非法值显式报错 —— 静默当成"未声明"是安全问题,
// 静默当成"最严"会让人以为角色坏了(两条都被 D13 排除)。
func NormalizeRoleApproval(v string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(v))
	if t == "" {
		return "", nil
	}
	if _, ok := approvalRank[t]; !ok {
		return "", fmt.Errorf("审批档 %q 不合法(角色可选 smart|strict,或留空 = 跟随全局)", v)
	}
	if t == string(ApprovalOpen) {
		return "", fmt.Errorf("角色不能声明审批档 open:角色只能收紧,不能放宽(可选 smart|strict,或留空 = 跟随全局)")
	}
	return t, nil
}

// NormalizeRoleSandbox 归一角色声明的沙箱档(空 = 不收紧)。
// 只接受 read-only / workspace-write:full-access 是**放宽**,角色无权声明。
func NormalizeRoleSandbox(v string) (string, error) {
	t := strings.ToLower(strings.TrimSpace(v))
	if t == "" {
		return "", nil
	}
	if _, ok := sandboxRank[t]; !ok {
		return "", fmt.Errorf("沙箱档 %q 不合法(角色可选 read-only|workspace-write,或留空 = 跟随全局)", v)
	}
	if t == string(SandboxFullAccess) {
		return "", fmt.Errorf("角色不能声明沙箱档 full-access:角色只能收紧,不能放宽(可选 read-only|workspace-write,或留空 = 跟随全局)")
	}
	return t, nil
}

// TightenApproval 合成生效审批档:角色只能更严,绝不更松。
// global 为**联动后**的当前档(顺序错了会破防:全局 open + sync 联动把沙箱放成
// full-access,若角色收紧算在联动之前,角色的 read-only 会被联动覆盖掉)。
// 返回 byRole = 这次差异确实由角色造成(展示端据此标"角色收紧"而不是"联动")。
func TightenApproval(role, global string) (mode string, byRole bool) {
	if role == "" {
		return global, false
	}
	r, ok := approvalRank[role]
	if !ok {
		// 到不了这里:角色定义在校验期就会把非法档判成坏角色(不进运行期)。
		// 若真有非校验路径塞进来(单测桩/程序化构造),**故障闭合**:当成最严,而不是
		// 当成"未声明"静默放行(用户的意图是收紧)。
		return string(ApprovalStrict), true
	}
	g, ok := approvalRank[global]
	if !ok {
		// 当前档不可知(空/非法):当成最松 —— 让角色声明的收紧**生效**才是故障闭合方向;
		// 当成最严会把"用户明确要求收紧"静默丢掉(与上一段同一个判断,只是对象不同)。
		g = 0
	}
	if r > g {
		return role, true
	}
	return global, false
}

// TightenSandbox 合成生效沙箱档(语义同 TightenApproval)。
func TightenSandbox(role, global string) (mode string, byRole bool) {
	if role == "" {
		return global, false
	}
	r, ok := sandboxRank[role]
	if !ok {
		return string(SandboxReadOnly), true // 故障闭合(同 TightenApproval)
	}
	g, ok := sandboxRank[global]
	if !ok {
		// 当前档不可知(空/非法):当成最松 —— 让角色声明的收紧生效才是故障闭合方向
		// (与 TightenApproval 同款判断)。
		g = 0
	}
	if r > g {
		return role, true
	}
	return global, false
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

// RoleMutator 可选扩展:在**串行化窗口内**做「读-改-写」(面板的部分更新必须原子)。
//
// 为何是可选窄接口:消费方(web)拿到的是 `ctx.roles`,而实现者可能有多个(内置 host-roles
// 与单测桩)。不实现时调用方回落 Get+Update —— 那是个**非原子读改写**(两个 PATCH 重叠时
// 后者用旧快照盖掉前者的改动,两次都 200),窗口 = 服务端写盘耗时,窄但非零。
// mutate 返回 error = 本次不写盘(调用方据此回 400,不会留下半成品)。
type RoleMutator interface {
	PatchRole(id string, mutate func(*RoleSpec) error) (RoleSpec, error)
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
