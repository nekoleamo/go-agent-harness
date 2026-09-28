// Package skills:技能库的读写落点(路径单一事实源)。
//
//	$GAH_HOME/skills/<名>/SKILL.md                 共享技能库(Library{})
//	$GAH_HOME/roles/<id>/skills/<名>/SKILL.md      角色私有技能(ForRole)
//	以上两处的 .trash/<名>-<时间戳>/               删除的技能(可恢复)
//
// 为什么单独一个包:与 internal/roles 同款纪律 —— 写盘细节集中在一处,路径一律经
// Path()/Dir() 派生自 sdk.Home(),Web/命令两条入口共用同一份校验与原子写。
// 扫描侧(host-skills)会跳过点开头的目录,所以 .trash 不会被打成"已加载技能"。
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

const (
	// FileName 技能正文文件名(与 scan 侧同一常量口径)。
	FileName = "SKILL.md"
	// TrashName 删除技能的回收站目录(位于各技能根下;点开头 → 扫描跳过)。
	TrashName = ".trash"
	// MaxBytes 单个 SKILL.md 的字节上限(64KiB)。
	// 为什么要上限:技能正文会被模型按需读进上下文,写错一个文件不该把上下文吃掉;
	// 同时防"以写技能为名"往数据根塞大文件。
	MaxBytes = 64 * 1024
	// maxTrashKeep 回收站保留份数(超出按名字倒序淘汰)。
	maxTrashKeep = 20
)

// nameRe 技能名口径:小写字母/数字/点/下划线/连字符,首位字母数字,总长 ≤ 64。
// 不能含路径分隔符与 ".."(目录名即身份,顺带堵路径穿越)。
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidateName 校验技能名(非法返回人话原因)。
func ValidateName(name string) error {
	switch {
	case name == "":
		return errors.New("技能名不能为空")
	case name == "." || strings.HasPrefix(name, "."):
		return fmt.Errorf("技能名不能以点开头(%q)", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("技能名不能含路径分隔符:%q", name)
	case !nameRe.MatchString(name):
		return fmt.Errorf("技能名只允许小写字母/数字/点/下划线/连字符(首字符须为字母或数字,长度 ≤ 64):%q", name)
	}
	return nil
}

// Dir 共享技能库根目录($GAH_HOME/skills)。
func Dir() string { return filepath.Join(sdk.Home(), "skills") }

// Library 一个技能根目录(共享库或某角色的私有目录)。
type Library struct {
	Root string
}

// Shared 共享技能库。
func Shared() Library { return Library{Root: Dir()} }

// ForRole 某个角色的私有技能目录。
func ForRole(roleID string) Library { return Library{Root: roles.SkillsPath(roleID)} }

// Path 技能正文路径(不做存在性校验;name 必须先过 ValidateName)。
func (l Library) Path(name string) string { return filepath.Join(l.Root, name, FileName) }

// Exists 技能是否存在于本库。
func (l Library) Exists(name string) bool {
	if ValidateName(name) != nil {
		return false
	}
	fi, err := os.Stat(l.Path(name))
	return err == nil && !fi.IsDir()
}

// Write 写 SKILL.md(原子替换;overwrite=false 时已存在显式失败)。
// 校验三件:技能名合法、正文不超上限、frontmatter 里的 name(若写了)与目录名一致 ——
// 不一致时扫描出来的名字是 frontmatter 名,而私有技能列表用的是目录名,用户会看到"两个名字"。
func (l Library) Write(name, content string, overwrite bool) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if len(content) > MaxBytes {
		return fmt.Errorf("技能正文超上限:%d 字节 > %d 字节", len(content), MaxBytes)
	}
	if fn := ParseName(content); fn != "" && fn != name {
		return fmt.Errorf("frontmatter 里的 name(%s)与技能目录名(%s)不一致", fn, name)
	}
	if !overwrite && l.Exists(name) {
		return fmt.Errorf("技能已存在:%s(改写请勾选覆盖)", name)
	}
	dir := filepath.Join(l.Root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("技能目录创建失败 %s: %w", dir, err)
	}
	return writeFileAtomic(filepath.Join(dir, FileName), []byte(content), 0o644)
}

// Read 读技能正文。
func (l Library) Read(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	raw, err := os.ReadFile(l.Path(name))
	if err != nil {
		return "", fmt.Errorf("技能 %s 读取失败: %w", name, err)
	}
	return string(raw), nil
}

// Remove 删除技能:整目录移入 <root>/.trash/<名>-<时间戳>(可恢复),不做物理删除。
func (l Library) Remove(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if !l.Exists(name) {
		return fmt.Errorf("技能不存在:%s", name)
	}
	trash := filepath.Join(l.Root, TrashName)
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return fmt.Errorf("回收站创建失败: %w", err)
	}
	dst := filepath.Join(trash, fmt.Sprintf("%s-%s", name, time.Now().Format("20060102-150405")))
	if err := os.Rename(filepath.Join(l.Root, name), dst); err != nil {
		return fmt.Errorf("技能移入回收站失败: %w", err)
	}
	pruneTrash(trash)
	return nil
}

// pruneTrash 只保留最近 maxTrashKeep 份(目录名带时间戳,倒序淘汰)。
func pruneTrash(trash string) {
	entries, err := os.ReadDir(trash)
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
		_ = os.RemoveAll(filepath.Join(trash, n))
	}
}

// Content 组装 SKILL.md(frontmatter + 正文)。与 host-skills 的解析口径一致:
// frontmatter 键 name/description/trigger + 三横线包夹。
func Content(name, description string, triggers []string, body string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString("name: " + name + "\n")
	if description != "" {
		sb.WriteString("description: " + quoteYAML(description) + "\n")
	}
	if len(triggers) > 0 {
		sb.WriteString("trigger:\n")
		for _, t := range triggers {
			if strings.TrimSpace(t) == "" {
				continue
			}
			sb.WriteString("  - " + quoteYAML(t) + "\n")
		}
	}
	sb.WriteString("---\n\n")
	sb.WriteString(strings.TrimSpace(body))
	sb.WriteString("\n")
	return sb.String()
}

// quoteYAML 给可能含冒号/井号/引号的值加双引号并转义(手写 frontmatter 的最小安全带)。
func quoteYAML(s string) string {
	if !strings.ContainsAny(s, `:#"'{}[]&*!|>%@`+"`") && s == strings.TrimSpace(s) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// ParseName 取 frontmatter 里的 name(无 frontmatter/无该键 → 空串)。
func ParseName(content string) string {
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "name:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// writeFileAtomic 同目录临时文件 + rename 原子替换(与 internal/roles 同款约定:
// 读方永不看到半截文件;失败清理临时文件)。
func writeFileAtomic(path string, raw []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".gah-skill-*.tmp")
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
