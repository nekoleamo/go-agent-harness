// 预置角色(seed/roles/)的首启释放。
package embed

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SeedRolesVersion 预置角色集合的版本号(写进各角色目录的 .seed-version,便于盘点来源)。
// 注意:角色是**用户可改可删**的数据,升级策略是"缺失才释放,已存在就整体跳过" ——
// 不做 seed→磁盘 的覆盖升级(用户改过的 AGENTS.md 被覆盖等于丢用户数据)。
// 因此新增预置角色会落到老用户机器上,而预置内容的修订不会(要改就自己改,或删掉重放)。
const SeedRolesVersion = 2

// EnsureRoles 把预置角色释放到 $GAH_HOME/roles/(缺失即创建;角色目录已存在 = 整体跳过)。
// 返回新建的角色目录清单(已存在的不计入)。
func EnsureRoles(home string) ([]string, error) {
	root := filepath.Join(home, "roles")
	var written []string
	err := fs.WalkDir(Seed, "seed/roles", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(path, "seed/roles/")
		if rel == path || rel == "" {
			return nil // seed/roles 本身
		}
		parts := strings.Split(rel, "/")
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if d.IsDir() {
			// 角色目录已存在:用户的地盘,整个跳过(不覆盖其中任何文件)。
			if len(parts) == 1 {
				if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
					return fs.SkipDir
				}
				if err := os.MkdirAll(dst, 0o755); err != nil {
					return err
				}
				written = append(written, dst)
				return nil
			}
			return os.MkdirAll(dst, 0o755)
		}
		if _, err := os.Stat(dst); err == nil {
			return nil // 已存在不覆盖(逐文件兜底:理论上目录级已拦住)
		}
		raw, err := Seed.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, raw, 0o644)
	})
	if err != nil {
		return nil, err
	}
	for _, dir := range written {
		marker := filepath.Join(dir, ".seed-version")
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			_ = os.WriteFile(marker, []byte(strconv.Itoa(SeedRolesVersion)+"\n"), 0o644)
		}
	}
	return written, nil
}
