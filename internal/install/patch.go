// patch.go:安装登记的 patch 文件管理(幂等合并条目)与 profile 装配注入。
package install

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Entry 一条配置条目(patch 文件内)。
type Entry struct {
	ID      string `yaml:"id"`
	Enabled bool   `yaml:"enabled,omitempty"`
	Data    any    `yaml:"data,omitempty"`
}

// patchFile patch 文件结构。
type patchFile struct {
	Entries []Entry `yaml:"entries"`
}

// EnsurePatch 幂等合并:按 id 更新/追加条目(保留其它条目)。
func EnsurePatch(path string, e Entry) error {
	var pf patchFile
	if b, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(b, &pf) // 解析失败按空处理,不破坏写入
	}
	replaced := false
	for i := range pf.Entries {
		if pf.Entries[i].ID == e.ID {
			pf.Entries[i] = e
			replaced = true
			break
		}
	}
	if !replaced {
		pf.Entries = append(pf.Entries, e)
	}
	header := "# gah install 自动登记(幂等合并;手工编辑可覆盖,勿删本文件)\n"
	b, err := yaml.Marshal(pf)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(header), b...), 0o644)
}

// RuntimePatch TUI 持久开关登记文件(home/config/patch-runtime.yaml,profile 自动引用)。
func RuntimePatch(home string) string {
	return filepath.Join(home, "config", "patch-runtime.yaml")
}

// EnsureProfilePicks 兼容封装:引用安装登记 patch(patch-installed.yaml)。
func EnsureProfilePicks(home string) error {
	return EnsureProfileRef(home, PatchFile)
}

// EnsureProfileRef 让 home 全部 profile 引用指定 patch(幂等;文本级插入保留注释)。
// TUI 持久开关(patch-runtime.yaml)与 install 登记共用。
func EnsureProfileRef(home, patchName string) error {
	files, err := filepath.Glob(filepath.Join(home, "config", "profile-*.yaml"))
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ensureProfilePickName(f, patchName); err != nil {
			return fmt.Errorf("注入 patch %s 到 %s: %w", patchName, f, err)
		}
	}
	return nil
}

// ensureProfilePickName 单文件注入指定 patch 名(幂等:已含则跳过)。
func ensureProfilePickName(f, patchName string) error {
	b, err := os.ReadFile(f)
	if err != nil {
		return err
	}
	s := string(b)
	if strings.Contains(s, patchName) {
		return nil
	}
	lines := strings.Split(s, "\n")
	idx := -1
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "patches:") {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("profile 缺少 patches 列表")
	}
	if strings.TrimSpace(lines[idx]) == "patches: []" {
		lines[idx] = "patches:"
	}
	lines = append(lines[:idx+1], append([]string{"  - " + patchName}, lines[idx+1:]...)...)
	return os.WriteFile(f, []byte(strings.Join(lines, "\n")), 0o644)
}

// RemoveEntry 按 id 移除 patch 条目(幂等;不存在不报错)。
func RemoveEntry(path, id string) error {
	var pf patchFile
	if b, err := os.ReadFile(path); err == nil {
		_ = yaml.Unmarshal(b, &pf)
	}
	kept := pf.Entries[:0]
	for _, e := range pf.Entries {
		if e.ID != id {
			kept = append(kept, e)
		}
	}
	pf.Entries = kept
	header := "# gah 自动管理(幂等合并;手工编辑可覆盖,勿删本文件)\n"
	out, err := yaml.Marshal(pf)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(header), out...), 0o644)
}

// ReadEnablements 读 patch 文件的 enabled 状态表(id → enabled;文件缺失返回空表)。
func ReadEnablements(path string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var pf patchFile
	if yaml.Unmarshal(b, &pf) != nil {
		return out
	}
	for _, e := range pf.Entries {
		out[e.ID] = e.Enabled
	}
	return out
}
