package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUpgradeBackupsToRemove 只留最近 keep 份;名字序 = 时间序;非数字目录一律不碰。
// 反向验证:把排序方向/切片改错(保留最旧),第一条断言必红。
func TestUpgradeBackupsToRemove(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"1700000000", "1700000100", "1700000200", "1700000300"} {
		writeFile(t, filepath.Join(root, name, "gah-data", "x"), "y")
	}
	// 用户自己放的东西:名字不是 unix 秒 → 不在清理范围内。
	writeFile(t, filepath.Join(root, "my-notes", "a.txt"), "keep me")
	writeFile(t, filepath.Join(root, "README.txt"), "keep me")

	got := upgradeBackupsToRemove(root, 2)
	if len(got) != 2 {
		t.Fatalf("应删 2 份,得到 %v", got)
	}
	if filepath.Base(got[0]) != "1700000000" || filepath.Base(got[1]) != "1700000100" {
		t.Fatalf("应删最旧的两份,得到 %v", got)
	}
	for _, p := range got {
		if p == filepath.Join(root, "my-notes") {
			t.Fatal("非数字目录不该被清理")
		}
	}
	if len(upgradeBackupsToRemove(root, 10)) != 0 {
		t.Fatal("份数未超上限时不该删任何东西")
	}
	if len(upgradeBackupsToRemove(filepath.Join(root, "nope"), 1)) != 0 {
		t.Fatal("目录不存在时应返回空")
	}
}

// TestBackupBeforeUpgrade 备份落用户主目录之外的独立目录,并保留最近 3 份。
// 反向验证:把 pruneUpgradeBackups 去掉,第 3 条断言(旧的被清)必红。
func TestBackupBeforeUpgrade(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataRoot := filepath.Join(t.TempDir(), "gah-data")
	writeFile(t, filepath.Join(dataRoot, "config", "web.yaml"), "addr: 127.0.0.1:1")
	writeFile(t, filepath.Join(dataRoot, "sessions", "s.jsonl"), "log")

	// 预置 3 份更早的备份,第 4 次备份应把最老的一份挤掉。
	for _, ts := range []string{"1600000000", "1600000001", "1600000002"} {
		writeFile(t, filepath.Join(home, "gah-upgrade-backup", ts, "gah-data", "old"), "x")
	}

	dst, err := backupBeforeUpgrade(dataRoot)
	if err != nil {
		t.Fatalf("备份失败: %v", err)
	}
	if !filepath.IsAbs(dst) || filepath.Dir(dst) != filepath.Join(home, "gah-upgrade-backup") {
		t.Fatalf("备份应落在 <home>/gah-upgrade-backup 下,得到 %q", dst)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "gah-data", "config", "web.yaml")); string(got) != "addr: 127.0.0.1:1" {
		t.Fatalf("备份内容不对: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "gah-upgrade-backup", "1600000000")); !isErrNotExist(err) {
		t.Fatal("最旧的一份备份应被清理(只留 3 份)")
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "config", "web.yaml")); err != nil {
		t.Fatal("备份不该动源数据")
	}

	// 无数据 ⇒ 空路径也不是错误。
	empty, err := backupBeforeUpgrade(filepath.Join(t.TempDir(), "nope"))
	if err != nil || empty != "" {
		t.Fatalf("无数据应静默成功: path=%q err=%v", empty, err)
	}
}

// TestReadTailDropsPartialFirstLine 截断时丢掉被切断的首行(面板里不该出现残句)。
func TestReadTailDropsPartialFirstLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	lines := "第一行完整\n第二行完整\n第三行完整\n"
	writeFile(t, path, lines)
	// 只读最后 20 字节:起点落在第二行中间 ⇒ 首行(残句)必须被丢弃,剩下最后一行。
	got := readTail(path, 20)
	if got == "" {
		t.Fatal("应读到内容")
	}
	// 每一行都应以「第一/第二/第三」开头;若首行是残句,这里就会出现「行完整」之类。
	for _, line := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if !strings.HasPrefix(line, "第") {
			t.Fatalf("出现残句: %q(整段 %q)", line, got)
		}
	}
	// 全量读:不丢任何行。
	if full := readTail(path, 1024); full != lines {
		t.Fatalf("全量读应原样返回: %q", full)
	}
	// 文件不存在 ⇒ 空串,不报错。
	if readTail(filepath.Join(t.TempDir(), "nope"), 10) != "" {
		t.Fatal("不存在应返回空串")
	}
}
