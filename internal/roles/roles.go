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
}

// List 全部角色(按显示名排序)。**不读 AGENTS.md 正文**(只给字节数 + 私有技能名)——
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
		spec, err := s.Get(e.Name())
		if err != nil {
			continue // 坏角色由 Broken() 单独可见
		}
		spec.AGENTS = "" // 列表不带正文
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
func (s Store) Get(id string) (sdk.RoleSpec, error) {
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
	}
	if spec.Name == "" {
		spec.Name = id
	}
	if f.Skills != nil {
		spec.Skills = append([]string(nil), (*f.Skills)...)
		spec.SkillsSet = true
	}
	if b, err := os.ReadFile(filepath.Join(dir, AgentsName)); err == nil {
		spec.AGENTS = string(b)
	} else if !os.IsNotExist(err) {
		return sdk.RoleSpec{}, fmt.Errorf("角色 %s 的 %s 读取失败: %w", id, AgentsName, err)
	}
	spec.AGENTSBytes = len(spec.AGENTS)
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
		if !e.IsDir() {
			continue
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
	f := roleFile{Name: spec.Name, Description: spec.Description, Identity: spec.Identity,
		ExcludeGlobal: spec.ExcludeGlobal, SkillsInherit: spec.SkillsInherit}
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
	dst := filepath.Join(TrashDir(), fmt.Sprintf("%s-%s", id, time.Now().Format("20060102-150405")))
	if err := os.Rename(Dir(id), dst); err != nil {
		return fmt.Errorf("角色移入回收站失败: %w", err)
	}
	pruneTrash()
	return nil
}

// pruneTrash 只保留最近 maxTrashKeep 份(目录名带时间戳,倒序淘汰)。
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
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
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
