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
	// trashTimeLayout 回收站条目名尾部的时间戳口径(<名>-<YYYYMMDD-HHMMSS>)。
	// 单一事实源:Remove 落名、TrashList/Restore 还原都读它。
	trashTimeLayout = "20060102-150405"
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

// Relocate 改名 / 跨库移动(可一次两件都做);返回新技能名。
// dst 是目标库(Shared() 或 ForRole(id)),newName 空 = 沿用原名。
// 三件事必须一起做对:
//   - 目录名即身份:移动 = 在目标库下换成目标目录名;
//   - frontmatter 里的 name 必须跟着改 —— 扫描侧以 frontmatter 名为准,不改就会出现
//     “目录叫 x、清单里叫 y”两个名字(Write 的同一条不变量);
//   - 目标同名已存在 → 显式拒绝(不覆盖)。
//
// 顺序:先挪目录再改 frontmatter;改失败把目录挪回去(不留半截改名)。
func (l Library) Relocate(name string, dst Library, newName string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	if newName == "" {
		newName = name
	}
	if err := ValidateName(newName); err != nil {
		return "", err
	}
	if !l.Exists(name) {
		return "", fmt.Errorf("技能不存在:%s", name)
	}
	if dst.Root == l.Root && newName == name {
		return "", errors.New("名称与所在库都没有变")
	}
	if dst.Exists(newName) {
		return "", fmt.Errorf("目标库已有同名技能:%s", newName)
	}
	src := filepath.Join(l.Root, name)
	target := filepath.Join(dst.Root, newName)
	var content []byte
	if newName != name { // 只改名才需要改 frontmatter;纯移动正文一字不动
		raw, err := l.Read(name)
		if err != nil {
			return "", err
		}
		content = []byte(renameFrontmatterName(raw, newName))
	}
	if err := os.MkdirAll(dst.Root, 0o755); err != nil {
		return "", fmt.Errorf("目标库创建失败 %s: %w", dst.Root, err)
	}
	if err := os.Rename(src, target); err != nil {
		return "", fmt.Errorf("技能移动失败: %w", err)
	}
	if content != nil {
		if err := writeFileAtomic(filepath.Join(target, FileName), content, 0o644); err != nil {
			// 回滚命不命要如实报:目录名与 frontmatter 名必须一致,回滚失败时两半对不上
			// (目录名已是新名、frontmatter 还是旧名),再写「已回滚」就是骗人。
			if rbErr := os.Rename(target, src); rbErr != nil {
				return "", fmt.Errorf("技能改名失败,且回滚也失败:目录停在 %s 而 frontmatter 仍是旧名 %q(需手改目录名或 frontmatter);改名错误: %v;回滚错误: %w",
					target, name, err, rbErr)
			}
			return "", fmt.Errorf("技能改名失败(已回滚): %w", err)
		}
	}
	return newName, nil
}

// renameFrontmatterName 改写 frontmatter 里的 name 行;没有 frontmatter / 没有 name 键时
// 原样返回(扫描侧会回退目录名,不必凭空造一个键)。
func renameFrontmatterName(content, newName string) string {
	if !strings.HasPrefix(content, "---") {
		return content
	}
	rest := content[3:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return content
	}
	lines := strings.Split(rest[:end], "\n")
	changed := false
	for i, line := range lines {
		// 只看**零缩进**行:嵌套键(如 metadata 下的 name)被 TrimSpace 后也会以 "name:" 开头,
		// 误改会把嵌套键提升到顶层(重复顶层键 → 扫描侧整段 meta 解析失败、静默丢)。
		if line != strings.TrimLeft(line, " \t") {
			continue
		}
		if _, ok := strings.CutPrefix(line, "name:"); ok {
			lines[i] = "name: " + newName
			changed = true
		}
	}
	if !changed {
		return content
	}
	return "---" + strings.Join(lines, "\n") + rest[end:]
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
	dst := filepath.Join(trash, name+"-"+time.Now().Format(trashTimeLayout))
	if err := os.Rename(filepath.Join(l.Root, name), dst); err != nil {
		return fmt.Errorf("技能移入回收站失败: %w", err)
	}
	pruneTrash(trash)
	return nil
}

// TrashEntry 回收站里的一份技能(目录名 = <名>-<时间戳>)。
// Skill 为空 = 目录名不合约定(手工放进来的),面板照实列出但恢复会被拒。
type TrashEntry struct {
	Name      string `json:"name"`       // 回收站目录名(恢复时用它定位)
	Skill     string `json:"skill"`      // 原技能名("" = 认不出)
	DeletedAt string `json:"deleted_at"` // 删除时间(格式 20060102-150405)
}

// splitTrashName 从回收站条目名还原原技能名与删除时间戳(不合约定 → ok=false)。
// 技能名本身可含连字符,所以按**固定长度的尾部时间戳**切,不按"最后一个连字符"。
func splitTrashName(name string) (skill, ts string, ok bool) {
	cut := len(name) - len(trashTimeLayout) - 1
	if cut <= 0 || name[cut] != '-' {
		return "", "", false
	}
	skill, ts = name[:cut], name[cut+1:]
	if _, err := time.Parse(trashTimeLayout, ts); err != nil {
		return "", "", false
	}
	return skill, ts, true
}

// TrashList 本库回收站条目(最近的在前)。坏名也列出(不替用户隐藏磁盘上的东西)。
func (l Library) TrashList() []TrashEntry {
	entries, err := os.ReadDir(filepath.Join(l.Root, TrashName))
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
		skill, ts, _ := splitTrashName(n)
		out = append(out, TrashEntry{Name: n, Skill: skill, DeletedAt: ts})
	}
	return out
}

// sortTrashNewestFirst 回收站条目按**删除时间**倒序(最新在前)。
// 为什么不用目录名字典序:时间戳在名字**尾部**,字典序的第一主键是技能名 —— 那会把
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

// Restore 把回收站里的技能恢复回本库;返回恢复后的技能名。
// 同名技能已存在 → 显式拒绝(不静默覆盖现役技能);条目名不含时间戳 → 拒绝(手放的目录不猜)。
func (l Library) Restore(trashName string) (string, error) {
	if trashName == "" || strings.ContainsAny(trashName, `/\`) || trashName != filepath.Base(trashName) || trashName == "." || trashName == ".." {
		return "", fmt.Errorf("非法回收站条目名:%q", trashName)
	}
	name, _, ok := splitTrashName(trashName)
	if !ok {
		return "", fmt.Errorf("回收站条目名不含 <名>-<时间戳>,无法还原:%s", trashName)
	}
	if err := ValidateName(name); err != nil {
		return "", err
	}
	src := filepath.Join(l.Root, TrashName, trashName)
	fi, err := os.Stat(src)
	if err != nil || !fi.IsDir() {
		return "", fmt.Errorf("回收站条目不存在:%s", trashName)
	}
	if l.Exists(name) {
		return "", fmt.Errorf("技能 %s 已存在:先改名或删除现有技能,再恢复回收站里那一份", name)
	}
	if err := os.Rename(src, filepath.Join(l.Root, name)); err != nil {
		return "", fmt.Errorf("技能恢复失败: %w", err)
	}
	return name, nil
}

// pruneTrash 只保留最近 maxTrashKeep 份(按删除时间倒序淘汰;见 sortTrashNewestFirst)。
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
	sortTrashNewestFirst(names)
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
	// 先转义反斜杠再转义引号(YAML 双引号标量里 `\` 是转义引导符):
	// 否则 Windows 路径/C:\temp 这类值要么生成非法 YAML(`\l` 未知转义),要么被静默改写(`\t` 变 TAB)。
	s = strings.ReplaceAll(s, `\`, `\\`)
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
		// 零缩进才算顶层键(嵌套的 name: 不是本技能的 name)
		if line != strings.TrimLeft(line, " \t") {
			continue
		}
		if v, ok := strings.CutPrefix(line, "name:"); ok {
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
	// Sync 再 rename:只 rename 不落盘的话,掉电后可能“文件在、内容空”。口径同 prefs/searchfile。
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
