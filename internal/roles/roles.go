// Package roles 角色定义的持久化(单一根:$GAH_HOME/roles/)。
//
// 布局:
//
//	$GAH_HOME/roles/<id>/role.yaml       角色定义(显示名/身份句/技能挂载清单)
//	$GAH_HOME/roles/<id>/AGENTS.md       角色工作规则(可空)
//	$GAH_HOME/roles/<id>/skills/<名>/SKILL.md   角色私有技能(可选)
//	$GAH_HOME/roles/<id>/.seed-version   预置角色标记(由 internal/embed 释放时写入)
//	$GAH_HOME/roles/.trash/<id>-<ts>/    删除的角色(可恢复)
//
// 为什么写盘逻辑集中在 internal/ 而不是插件里:与 internal/prefs、internal/install 同款
// (宿主内置插件可 import internal/,不构成插件 import 环 —— 见 internal/prefs 包注释)。
// 插件(host-roles)只负责编排:切换五步、提示注入、技能过滤。
//
// 便携纪律:所有路径经 Path()/Dir() 派生自 sdk.RolesDir()(→ sdk.Home() → GAH_HOME)。
package roles

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 文件/目录名与上限。
const (
	FileName        = "role.yaml"
	AgentsName      = "AGENTS.md"
	SkillsDirName   = "skills"
	TrashName       = ".trash"
	SeedVersionName = ".seed-version"
	// MaxAgentsBytes 单个角色 AGENTS.md 的字节上限(32KiB)。
	// 为什么要上限:角色说明会**逐字进系统提示** —— 没有上限时一个角色文件就能把上下文吃掉,
	// 且用户很难意识到"我贴的那段文档"其实每一轮都在计费(dsh 的 agent-instructions 同样带 maxBytes)。
	MaxAgentsBytes = 32 * 1024
	// maxTrashKeep .trash 保留的最近份数(超出按目录名倒序淘汰)。
	maxTrashKeep = 20
	// trashTimeLayout 回收站条目名尾部的时间戳口径(<id>-<YYYYMMDD-HHMMSS>)。
	// 单一事实源:Delete 落名、TrashList/Restore 还原都读它,免得一边改了一边解析不出来。
	trashTimeLayout = "20060102-150405"
	// defaultOwnSkillMax 角色私有技能个数上限(防"整库复制进角色"的误用)。
	defaultOwnSkillMax = 200
)

// Path 角色根目录($GAH_HOME/roles)。
func Path() string { return sdk.RolesDir() }

// Dir 一个角色的目录(不做存在性校验;id 必须先过 ValidateID)。
func Dir(id string) string { return filepath.Join(Path(), id) }

// AgentsPath 角色 AGENTS.md 路径。
func AgentsPath(id string) string { return filepath.Join(Dir(id), AgentsName) }

// SkillsPath 角色私有技能目录。
func SkillsPath(id string) string { return filepath.Join(Dir(id), SkillsDirName) }

// TrashDir 删除角色的回收站。
func TrashDir() string { return filepath.Join(Path(), TrashName) }

// idRe 角色 ID 口径:小写字母/数字/连字符,首位字母数字,总长 ≤ 32。
// 收紧到这一集是为了:S3 路径穿越(id 直接进 filepath.Join)、Windows 大小写不敏感
// (混合大小写会让 two ids 指向同一目录)、以及各处展示/日志的稳定性。
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// reservedID 保留名(与回收站/内部目录冲突)。
var reservedID = map[string]bool{TrashName: true, "roles": true}

// ValidateID 校验角色 ID(非法时返回人话原因)。
func ValidateID(id string) error {
	switch {
	case id == "":
		return errors.New("角色 ID 不能为空")
	case strings.HasPrefix(id, "."):
		return fmt.Errorf("角色 ID 不能以点开头(%q)", id)
	case reservedID[id]:
		return fmt.Errorf("角色 ID %q 是保留名", id)
	case !idRe.MatchString(id):
		return fmt.Errorf("角色 ID 只允许小写字母/数字/连字符(首字符须为字母或数字,长度 ≤ 32):%q", id)
	}
	return nil
}

// truncate 按字节上限截断 UTF-8 文本(截断点落在 rune 边界;返回值已截断 = true)。
func truncate(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	b := []byte(s)[:max]
	// 回退到合法 UTF-8 边界(避免把多字节字符劈成半个 → 提示里出现替换符)。
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b), true
}

// Problem 一个读取失败的角色(可见性:坏文件不静默消失)。
type Problem struct {
	ID  string
	Err string
}

// Store 角色存储(无状态;所有方法直接操作磁盘)。
type Store struct{}

// roleFile role.yaml 的落盘结构(字段名与文档一致;指针字段区分"未写"与"写了零值")。
type roleFile struct {
	Name          string    `yaml:"name,omitempty"`
	Description   string    `yaml:"description,omitempty"`
	Identity      string    `yaml:"identity,omitempty"`
	ExcludeGlobal bool      `yaml:"exclude_global,omitempty"`
	Skills        *[]string `yaml:"skills,omitempty"`
	SkillsInherit bool      `yaml:"skills_inherit,omitempty"`
	// Model / Thinking 角色携带的模型与思考档(第八十六批;空 = 跟随会话)。
	// 必须与 sdk.RoleSpec 同步:Save 是"用 spec 重建一份 roleFile 全量覆盖写",
	// 少一个字段就手写在 role.yaml 里的键就会被下一欢保存/改名/移动**静默抹掉**。
	Model    string `yaml:"model,omitempty"`
	Thinking string `yaml:"thinking,omitempty"`
	// ToolsExclude 排除的工具名(第九十一批;空 = 不排除任何工具)。
	// 与 Model/Thinking 同款:无 Set 标记(空清单与未写这个键行为一致)。
	ToolsExclude []string `yaml:"tools_exclude,omitempty"`
}

// List 全部角色(按显示名排序)。**不读 AGENTS.md 正文**(只给字节数 + 私有技能名) ——
// 列表可能被 Web 轮询,逐个读正文是纯浪费;编辑时才走 Get。
func (s Store) List() []sdk.RoleSpec {
	entries, err := os.ReadDir(Path())
	if err != nil {
		return nil
	}
	var out []sdk.RoleSpec
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue // 回收站/隐藏目录不算角色
		}
		spec, err := s.get(e.Name(), false)
		if err != nil {
			continue // 坏角色由 Broken() 单独可见
		}
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

// MountUsers 挂载了 name 这个技能名的角色 ID(排序)。用途:技能被移进某个角色的私有库时,
// 提醒“这些角色挂着的同名技能已经看不见了”(挂载按名字存,纯移动不会留下失效挂载,
// 但**可见性**随库变 —— 不说一声就是静默失信)。
func (s Store) MountUsers(name string) []string {
	var out []string
	for _, id := range s.IDs() {
		spec, err := s.Get(id)
		if err != nil {
			continue // 坏角色不参与(它本来就没在跑)
		}
		if !spec.SkillsSet {
			continue // 未写 skills 键 = 默认池:整个共享库都可见,私有技能本来就不在其中
		}
		for _, m := range spec.Skills {
			if m == name {
				out = append(out, id)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// IDs 全部角色目录名(不含 .trash/隐藏目录;不判断 role.yaml 是否可读 —— 坏角色也算)。
// 用途:回收站面板要把每个角色的私有技能 .trash 一并列出,坏角色的目录也不该漏。
func (s Store) IDs() []string {
	entries, err := os.ReadDir(Path())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// Broken 读取失败的角色目录(role.yaml 缺失/坏 YAML/ID 非法)。
func (s Store) Broken() []Problem {
	entries, err := os.ReadDir(Path())
	if err != nil {
		return nil
	}
	var out []Problem
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if _, err := s.Get(e.Name()); err != nil {
			out = append(out, Problem{ID: e.Name(), Err: err.Error()})
		}
	}
	return out
}

// Get 读单个角色(含 AGENTS.md 正文与私有技能名);不存在/坏文件返回错误。
func (s Store) Get(id string) (sdk.RoleSpec, error) { return s.get(id, true) }

// get 读角色定义;withBody=false 时**不读 AGENTS.md 正文**(只 Stat 出字节数)——
// 列表/面板列表只要元信息,读全文既浪费又会让“不读正文”的注释成为谎话。
func (s Store) get(id string, withBody bool) (sdk.RoleSpec, error) {
	if err := ValidateID(id); err != nil {
		return sdk.RoleSpec{}, err
	}
	dir := Dir(id)
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return sdk.RoleSpec{}, fmt.Errorf("角色不存在:%s", id)
	}
	raw, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		return sdk.RoleSpec{}, fmt.Errorf("角色 %s 缺 %s: %w", id, FileName, err)
	}
	var f roleFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return sdk.RoleSpec{}, fmt.Errorf("角色 %s 的 %s 解析失败: %w", id, FileName, err)
	}
	spec := sdk.RoleSpec{
		ID:            id,
		Name:          f.Name,
		Description:   f.Description,
		Identity:      f.Identity,
		ExcludeGlobal: f.ExcludeGlobal,
		SkillsInherit: f.SkillsInherit,
		Model:         strings.TrimSpace(f.Model),
	}
	if spec.Name == "" {
		spec.Name = id
	}
	// 思考档名规范化 + 校验:手写文件里的非法值必须**显式失败**(进 Broken()),
	// 不能静默当 off(见 sdk.NormalizeThinking)。
	th, err := sdk.NormalizeThinking(f.Thinking)
	if err != nil {
		return sdk.RoleSpec{}, fmt.Errorf("角色 %s 的 %s: %w", id, FileName, err)
	}
	spec.Thinking = th
	// 工具排除清单:形状校验(空项/空白/过长/重复)—— 坏值显式失败进 Broken(),
	// 不静默丢弃(默默删一条 = 给出与实际不符的角色定义)。
	ex, err := sdk.NormalizeToolNames(f.ToolsExclude)
	if err != nil {
		return sdk.RoleSpec{}, fmt.Errorf("角色 %s 的 %s: %w", id, FileName, err)
	}
	spec.ToolsExclude = ex
	if f.Skills != nil {
		spec.Skills = append([]string(nil), (*f.Skills)...)
		spec.SkillsSet = true
	}
	if withBody {
		if b, err := os.ReadFile(filepath.Join(dir, AgentsName)); err == nil {
			spec.AGENTS = string(b)
		} else if !os.IsNotExist(err) {
			return sdk.RoleSpec{}, fmt.Errorf("角色 %s 的 %s 读取失败: %w", id, AgentsName, err)
		}
		spec.AGENTSBytes = len(spec.AGENTS)
	} else if fi, err := os.Stat(filepath.Join(dir, AgentsName)); err == nil {
		spec.AGENTSBytes = int(fi.Size())
	}
	spec.OwnSkills = ownSkills(dir)
	if _, err := os.Stat(filepath.Join(dir, SeedVersionName)); err == nil {
		spec.Seed = true
	}
	return spec, nil
}

// ownSkills 角色私有技能名 = roles/<id>/skills/<名>/SKILL.md 的目录名(排序去重)。
func ownSkills(dir string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, SkillsDirName))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue // 回收站(.trash)等点开头目录不算技能
		}
		if _, err := os.Stat(filepath.Join(dir, SkillsDirName, e.Name(), "SKILL.md")); err != nil {
			continue
		}
		out = append(out, e.Name())
		if len(out) >= defaultOwnSkillMax {
			break
		}
	}
	sort.Strings(out)
	return out
}

// Save 写 role.yaml(原子替换;目录与 AGENTS.md 不存在时只写定义)。
func (s Store) Save(spec sdk.RoleSpec) error {
	if err := ValidateID(spec.ID); err != nil {
		return err
	}
	// 落盘前同样校验思考档与工具排除清单:写进去一个 Get 读不回来的值,等于当场造一个坏角色。
	th, err := sdk.NormalizeThinking(spec.Thinking)
	if err != nil {
		return err
	}
	ex, err := sdk.NormalizeToolNames(spec.ToolsExclude)
	if err != nil {
		return err
	}
	f := roleFile{Name: spec.Name, Description: spec.Description, Identity: spec.Identity,
		ExcludeGlobal: spec.ExcludeGlobal, SkillsInherit: spec.SkillsInherit,
		Model: strings.TrimSpace(spec.Model), Thinking: th, ToolsExclude: ex}
	if spec.SkillsSet {
		list := spec.Skills
		if list == nil {
			list = []string{} // 显式空 = 一个都不挂(与"未写"区分)
		}
		f.Skills = &list
	}
	raw, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("角色 %s 序列化失败: %w", spec.ID, err)
	}
	dir := Dir(spec.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("角色目录创建失败 %s: %w", dir, err)
	}
	return writeFileAtomic(filepath.Join(dir, FileName), raw, 0o644)
}

// SetAgents 写角色 AGENTS.md(超上限显式失败;原子替换)。
func (s Store) SetAgents(id, text string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	if len(text) > MaxAgentsBytes {
		return fmt.Errorf("AGENTS.md 超上限:%d 字节 > %d 字节(角色说明会逐字进系统提示,请精简)",
			len(text), MaxAgentsBytes)
	}
	dir := Dir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("角色目录创建失败 %s: %w", dir, err)
	}
	return writeFileAtomic(filepath.Join(dir, AgentsName), []byte(text), 0o644)
}

// Create 新建角色(已存在 → 显式失败,不覆盖)。
func (s Store) Create(spec sdk.RoleSpec, agents string) error {
	if err := ValidateID(spec.ID); err != nil {
		return err
	}
	if s.Exist(spec.ID) {
		return fmt.Errorf("角色已存在:%s", spec.ID)
	}
	if err := s.Save(spec); err != nil {
		return err
	}
	if agents != "" {
		if err := s.SetAgents(spec.ID, agents); err != nil {
			// 回滚:不留下"半截角色"(定义写了但正文超限失败)
			_ = os.RemoveAll(Dir(spec.ID))
			return err
		}
	}
	return nil
}

// Exist 角色目录是否存在(含 role.yaml 缺失的半截目录)。
func (s Store) Exist(id string) bool {
	if ValidateID(id) != nil {
		return false
	}
	fi, err := os.Stat(Dir(id))
	return err == nil && fi.IsDir()
}

// Rename 改 ID(目录改名)与/或显示名;改的是当前角色时同步更新偏好里的当前角色。
func (s Store) Rename(id, newID, newName string) (sdk.RoleSpec, error) {
	spec, err := s.Get(id)
	if err != nil {
		return sdk.RoleSpec{}, err
	}
	if newID == "" {
		newID = id
	}
	if newID != id {
		if err := ValidateID(newID); err != nil {
			return sdk.RoleSpec{}, err
		}
		if s.Exist(newID) {
			return sdk.RoleSpec{}, fmt.Errorf("目标角色 ID 已存在:%s", newID)
		}
		if err := os.Rename(Dir(id), Dir(newID)); err != nil {
			return sdk.RoleSpec{}, fmt.Errorf("角色目录改名失败: %w", err)
		}
		if prefs.Load().Role == id {
			prefs.SetRole(newID)
		}
		spec.ID = newID
	}
	if newName != "" {
		spec.Name = newName
	}
	if err := s.Save(spec); err != nil {
		return sdk.RoleSpec{}, err
	}
	return s.Get(spec.ID)
}

// RewriteMount 把所有角色 role.yaml 挂载清单里的 old 改成 new(技能改名后引用不断);
// 返回被改动的角色 ID。为什么必须做:挂载是**按名字**的 —— 光改技能目录名会让每个挂载它的
// 角色悄悹多出一条“已失效挂载”(技能从该角色消失)。坏角色文件跳过(不因它中断全局改写)。
// 直接操作 roleFile 而非 RoleSpec→Save:不把 Get 的缺省显示名(= id)写成显式 name 键。
func (s Store) RewriteMount(old, new string) ([]string, error) {
	if old == "" || new == "" || old == new {
		return nil, nil
	}
	var touched []string
	for _, id := range s.IDs() {
		path := filepath.Join(Dir(id), FileName)
		raw, err := os.ReadFile(path)
		if err != nil {
			continue // 坏角色:跳过(它本来就读不出来)
		}
		var f roleFile
		if err := yaml.Unmarshal(raw, &f); err != nil || f.Skills == nil {
			continue // 未写 skills 键 = 默认池,改名对它无影响
		}
		list, changed := *f.Skills, false
		for i, n := range list {
			if n == old {
				list[i] = new
				changed = true
			}
		}
		if !changed {
			continue
		}
		f.Skills = &list
		out, err := yaml.Marshal(f)
		if err != nil {
			return touched, fmt.Errorf("角色 %s 序列化失败: %w", id, err)
		}
		if err := writeFileAtomic(path, out, 0o644); err != nil {
			return touched, fmt.Errorf("角色 %s 挂载改写失败: %w", id, err)
		}
		touched = append(touched, id)
	}
	return touched, nil
}

// Delete 删除角色:移入 .trash(可恢复),不做物理删除。
// 当前角色拒绝删除(否则"当前角色"悬空,下一轮提示里会静默少一层指令)。
func (s Store) Delete(id string) error {
	if err := ValidateID(id); err != nil {
		return err
	}
	if !s.Exist(id) {
		return fmt.Errorf("角色不存在:%s", id)
	}
	if cur := s.Active(); cur == id {
		return fmt.Errorf("角色 %s 正在使用中:先切换到其它角色(或 /role none)再删除", id)
	}
	if err := os.MkdirAll(TrashDir(), 0o755); err != nil {
		return fmt.Errorf("回收站创建失败: %w", err)
	}
	dst := filepath.Join(TrashDir(), trashEntryName(id))
	if err := os.Rename(Dir(id), dst); err != nil {
		return fmt.Errorf("角色移入回收站失败: %w", err)
	}
	pruneTrash()
	return nil
}

// TrashEntry 回收站里的一份角色(目录名 = <id>-<时间戳>)。
// ID/DeletedAt 为空 = 目录名不合约定(手工放进来的),面板照实列出但恢复会被拒。
// 为什么带 ID 而不是只给目录名:面板要显示"这是哪个角色",且恢复目标就是这个 ID。
type TrashEntry struct {
	Name      string `json:"name"`       // 回收站目录名(恢复时用它定位)
	ID        string `json:"id"`         // 原角色 ID("" = 认不出)
	DeletedAt string `json:"deleted_at"` // 删除时间(目录名尾部时间戳,格式 20060102-150405)
}

// trashEntryName 回收站目录名(<id>-<时间戳>);Delete 与测试共用一处命名。
func trashEntryName(id string) string { return id + "-" + time.Now().Format(trashTimeLayout) }

// splitTrashName 从回收站条目名还原原 ID 与删除时间戳(不合约定 → ok=false)。
// 技能名/角色 ID 本身可以含连字符,所以只能按**固定长度的尾部时间戳**切,不能按"最后一个连字符"。
func splitTrashName(name string) (id, ts string, ok bool) {
	cut := len(name) - len(trashTimeLayout) - 1
	if cut <= 0 || name[cut] != '-' {
		return "", "", false
	}
	id, ts = name[:cut], name[cut+1:]
	if _, err := time.Parse(trashTimeLayout, ts); err != nil {
		return "", "", false
	}
	return id, ts, true
}

// TrashList 回收站条目(最近的在前)。不做清洗:坏名也列出来(否则用户看不见自己手放的目录)。
func (s Store) TrashList() []TrashEntry {
	entries, err := os.ReadDir(TrashDir())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sortTrashNewestFirst(names)
	out := make([]TrashEntry, 0, len(names))
	for _, n := range names {
		id, ts, _ := splitTrashName(n)
		out = append(out, TrashEntry{Name: n, ID: id, DeletedAt: ts})
	}
	return out
}

// sortTrashNewestFirst 回收站条目按**删除时间**倒序(最新在前)。
// 为什么不用目录名字典序:时间戳在名字**尾部**,字典序的第一主键是 id —— 那会把
// 「刚删的那一份」当成最旧的淘汰掉(名字最小的先出局),用户正要恢复的东西被静默销毁。
// 认不出时间戳的坏名排最后(优先淘汰);同刻/同为坏名时按名字倒序,保证顺序确定。
func sortTrashNewestFirst(names []string) {
	sort.Slice(names, func(i, j int) bool {
		_, ti, oki := splitTrashName(names[i])
		_, tj, okj := splitTrashName(names[j])
		if oki != okj {
			return oki
		}
		if ti != tj {
			return ti > tj // 固定宽度格式 → 字典序即时间序
		}
		return names[i] > names[j]
	})
}

// Restore 把回收站里的角色恢复回 roles/<id>/;返回恢复后的角色 ID。
// 目标 ID 已存在 → 显式拒绝(不覆盖现役角色);条目名不含时间戳 → 拒绝(手放的目录不猜)。
func (s Store) Restore(trashName string) (string, error) {
	if trashName == "" || strings.ContainsAny(trashName, `/\`) || trashName != filepath.Base(trashName) || trashName == "." || trashName == ".." {
		return "", fmt.Errorf("非法回收站条目名:%q", trashName)
	}
	id, _, ok := splitTrashName(trashName)
	if !ok {
		return "", fmt.Errorf("回收站条目名不含 <id>-<时间戳>,无法还原:%s", trashName)
	}
	if err := ValidateID(id); err != nil {
		return "", err
	}
	src := filepath.Join(TrashDir(), trashName)
	fi, err := os.Stat(src)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("回收站条目不存在:%s", trashName)
	}
	if s.Exist(id) {
		return "", fmt.Errorf("角色 %s 已存在:先改名或删除现有角色,再恢复回收站里那一份", id)
	}
	if err := os.Rename(src, Dir(id)); err != nil {
		return "", fmt.Errorf("角色恢复失败: %w", err)
	}
	return id, nil
}

// pruneTrash 只保留最近 maxTrashKeep 份(按删除时间倒序淘汰;见 sortTrashNewestFirst)。
func pruneTrash() {
	entries, err := os.ReadDir(TrashDir())
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) <= maxTrashKeep {
		return
	}
	sortTrashNewestFirst(names)
	for _, n := range names[maxTrashKeep:] {
		_ = os.RemoveAll(filepath.Join(TrashDir(), n))
	}
}

// Active 当前角色 ID("" = 未启用)。
func (s Store) Active() string { return prefs.Load().Role }

// SetActive 持久化当前角色(空 = 停用);不校验存在性 —— 由服务层在切换前校验。
func (s Store) SetActive(id string) error {
	if id != "" {
		if err := ValidateID(id); err != nil {
			return err
		}
	}
	prefs.SetRole(id)
	if prefs.Path() == "" {
		return errors.New("数据根未就绪(GAH_HOME 未设):当前角色无法持久化")
	}
	return nil
}

// writeFileAtomic 同目录临时文件 + rename 原子替换(与 internal/prefs 同款约定:
// 读方永不看到半截文件;失败清理临时文件)。
func writeFileAtomic(path string, raw []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gah-role-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	// Sync 再 rename:只 rename 不落盘的话,掉电后可能“文件在、内容空”
	// (ext4 等延迟分配的常见表现——rename 已生效而数据还在页缓存)。口径同 prefs/searchfile。
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Shrink 按上限截断角色正文(注入侧用;返回是否发生截断)。
func Shrink(text string) (string, bool) { return truncate(text, MaxAgentsBytes) }
