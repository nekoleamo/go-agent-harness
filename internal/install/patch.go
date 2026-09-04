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

// EnsureProfilePicks 让 home 全部 profile 引用安装 patch(幂等;文本级插入保留注释)。
func EnsureProfilePicks(home string) error {
	files, err := filepath.Glob(filepath.Join(home, "config", "profile-*.yaml"))
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := ensureProfilePick(f); err != nil {
			return fmt.Errorf("注入 patch 到 %s: %w", f, err)
		}
	}
	return nil
}

// ensureProfilePick 单个 profile 注入(幂等:已含则跳过)。
func ensureProfilePick(f string) error {
	b, err := os.ReadFile(f)
	if err != nil {
		return err
	}
	s := string(b)
	if strings.Contains(s, PatchFile) {
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
	// 在 patches: 行后插入一项
	lines = append(lines[:idx+1], append([]string{"  - " + PatchFile}, lines[idx+1:]...)...)
	return os.WriteFile(f, []byte(strings.Join(lines, "\n")), 0o644)
}
