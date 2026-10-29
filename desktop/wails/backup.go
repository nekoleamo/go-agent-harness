// backup:升级前把数据根复制到用户主目录(时间戳目录),并只保留最近几份。
//
// 为什么必须有:升级是**整包替换**,一旦新版本把数据读坏/写坏,用户没有第二份。
// 备份落在应用目录**之外**(用户主目录)才有意义 —— 放应用目录里会被升级本身覆盖掉。
// 备份是保险而不是归档:磁盘占用随升级次数单调增长,留最近 3 份足够。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// backupKeep 升级前备份保留份数(超出即删更早的时间戳目录)。
const backupKeep = 3

// userHome 用户主目录。顺序:HOME → USERPROFILE(Git Bash/MSYS 下两者都在时以前者为准,
// 与 Go 侧 sdk.UserHome() 同口径 —— 旧壳曾因两处顺序相反,把备份和默认工作区放到了不同目录)。
func userHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE")
}

// backupRoot 备份落地目录:用户主目录下 `gah-upgrade-backup/`。
func backupRoot() (string, error) {
	h := userHome()
	if h == "" {
		return "", fmt.Errorf("未取到用户主目录(用于放升级前备份)")
	}
	return filepath.Join(h, "gah-upgrade-backup"), nil
}

// backupBeforeUpgrade 把数据根复制到 `<home>/gah-upgrade-backup/<unix秒>/gah-data`,
// 返回落地目录。无数据(首次安装即升级)→ 返回空路径;失败 → 错误(调用方中止升级)。
func backupBeforeUpgrade(dataRoot string) (string, error) {
	if _, err := os.Stat(dataRoot); err != nil {
		return "", nil // 没有数据:无可备份
	}
	root, err := backupRoot()
	if err != nil {
		return "", err
	}
	dst := filepath.Join(root, strconv.FormatInt(time.Now().Unix(), 10))
	if err := copyTree(dataRoot, filepath.Join(dst, "gah-data")); err != nil {
		return "", fmt.Errorf("复制 %s → %s 失败: %w", dataRoot, dst, err)
	}
	pruneUpgradeBackups(root, backupKeep)
	return dst, nil
}

// pruneUpgradeBackups 只保留最新 keep 份,删更早的。清理失败只记日志、不阻断升级
// (清理是附带好处,升级才是目的)。
func pruneUpgradeBackups(root string, keep int) {
	for _, old := range upgradeBackupsToRemove(root, keep) {
		if err := os.RemoveAll(old); err != nil {
			shellLogf("升级备份清理失败(不影响升级): %s %v", old, err)
		}
	}
}

// upgradeBackupsToRemove 选出该删的备份目录(纯函数,便于单测)。
// 目录名是 unix 秒且定宽 ⇒ 名字序 = 时间序;留下最后 keep 个,更早的删掉。
// 名字不是纯数字的目录一律不碰(用户自己往这里放别的东西时,删掉就是数据事故)。
func upgradeBackupsToRemove(root string, keep int) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil // 目录不存在/不可读:没有可清理的
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.ParseInt(e.Name(), 10, 64); err != nil {
			continue
		}
		dirs = append(dirs, filepath.Join(root, e.Name()))
	}
	sort.Strings(dirs)
	if len(dirs) <= keep {
		return nil
	}
	return dirs[:len(dirs)-keep]
}
