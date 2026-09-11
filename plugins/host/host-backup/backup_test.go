// host-backup 单测:备份内容完整性/排除 backups 自身/确定性/坏归档拒绝/恢复幂等/防穿越。
package hostbackup

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildHome 构造迷你 GAH_HOME(home 根 + config/plugins/sessions/env.sh + 旧备份)。返回 Backup。
func buildHome(t *testing.T) *Backup {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	for _, d := range []string{"config", "plugins", "sessions"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(home, "config", "provider.yaml"), "api_key: sk-test\n")
	mustWrite(t, filepath.Join(home, "sessions", "main.jsonl"), "{\"seq\":1}\n")
	mustWrite(t, filepath.Join(home, "env.sh"), "export X=1\n")
	// 旧备份:backups/ 目录不应被再次打包
	if err := os.MkdirAll(filepath.Join(home, "backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, "backups", "old-archive.tar.gz"), "junk")
	return &Backup{home: home}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBackupCmdTildeExpand /backup ~/<名> 的 ~ 展开与 /workspace 同款(不落字面 ~ 目录)。
func TestBackupCmdTildeExpand(t *testing.T) {
	b := buildHome(t)
	uh := t.TempDir()
	t.Setenv("HOME", uh)
	out, err := backupCmd([]string{"~/mybackup.tar.gz"}, b)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(uh, "mybackup.tar.gz")
	if !strings.Contains(out, want) {
		t.Fatalf("返回消息应含展开路径 %s,got: %s", want, out)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("归档应落于展开路径: %v", err)
	}
}

// TestBackupContents 备份内容完整:config(密钥)/sessions/env.sh 在内,backups/ 自身排除。
func TestBackupContents(t *testing.T) {
	b := buildHome(t)
	name, err := b.Backup("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "gah-backup-") || !strings.HasSuffix(name, ".tar.gz") {
		t.Fatalf("归档命名不符: %s", name)
	}
	arc := filepath.Join(b.backupDir(), name)
	names := tarEntryNames(t, arc)
	for _, want := range []string{"config/provider.yaml", "sessions/main.jsonl", "env.sh"} {
		if !contains(names, want) {
			t.Fatalf("备份缺少 %s;全部条目: %v", want, names)
		}
	}
	for _, bad := range names {
		if strings.HasPrefix(bad, "backups/") {
			t.Fatalf("备份不应包含 backups/ 自身: %s", bad)
		}
	}
}

// TestBackupDeterministic 确定性:相同源两次备份,归档字节一致(重跑无 diff)。
func TestBackupDeterministic(t *testing.T) {
	b := buildHome(t)
	n1, err := b.Backup("")
	if err != nil {
		t.Fatal(err)
	}
	n2, err := b.Backup("")
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(filepath.Join(b.backupDir(), n1))
	b2, _ := os.ReadFile(filepath.Join(b.backupDir(), n2))
	if string(b1) != string(b2) {
		t.Fatal("确定性备份应字节一致(重跑无 diff);时间戳命名不同但内容必须相同")
	}
}

// TestBackupExternalDest 外部 dest:归档写到指定路径(不进 HOME 备份目录)。
func TestBackupExternalDest(t *testing.T) {
	b := buildHome(t)
	dest := filepath.Join(t.TempDir(), "my-backup.tar.gz")
	out, err := b.Backup(dest)
	if err != nil {
		t.Fatal(err)
	}
	if out != dest {
		t.Fatalf("外部 dest 应返回完整路径: %s", out)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatal(err)
	}
}

// TestBackupNoHome GAH_HOME 未设:显式报错,不静默。
func TestBackupNoHome(t *testing.T) {
	t.Setenv("GAH_HOME", "")
	b := &Backup{home: ""}
	if _, err := b.Backup(""); err == nil {
		t.Fatal("无 GAH_HOME 应显式报错")
	}
}

// TestRestoreRoundtrip 恢复幂等:备份 → 内容变更 → 恢复 → 数据回滚到备份时点。
func TestRestoreRoundtrip(t *testing.T) {
	b := buildHome(t)
	name, err := b.Backup("")
	if err != nil {
		t.Fatal(err)
	}
	// 变更内容(模拟误改)
	mustWrite(t, filepath.Join(b.home, "config", "provider.yaml"), "api_key: sk-BAD\n")
	// 恢复
	if err := b.Restore(name); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(b.home, "config", "provider.yaml"))
	if string(got) != "api_key: sk-test\n" {
		t.Fatalf("恢复后内容应回滚: %q", got)
	}
	// 恢复前自动备份留于 backups/(安全默认)
	list := b.List()
	if len(list) < 2 {
		t.Fatalf("恢复前应自动备份当前态(现在至少 2 份,got %d)", len(list))
	}
}

// TestRestoreBadArchive 坏归档拒绝(显式错误,不半恢复)。
func TestRestoreBadArchive(t *testing.T) {
	b := buildHome(t)
	mustWrite(t, filepath.Join(b.backupDir(), "gah-backup-bad.tar.gz"), "not a gzip")
	if err := b.Restore("gah-backup-bad.tar.gz"); err == nil {
		t.Fatal("坏归档应拒绝")
	}
	// 防穿越:含 ../ 条目
	mustWrite(t, filepath.Join(b.backupDir(), "gah-backup-traversal.tar.gz"), "")
	// 直接构造恶意归档
	evil := filepath.Join(b.backupDir(), "gah-backup-evil.tar.gz")
	if err := writeEvilArchive(evil); err != nil {
		t.Fatal(err)
	}
	if err := b.Restore("gah-backup-evil.tar.gz"); err == nil {
		t.Fatal("越界条目应中止恢复")
	}
}

// tarEntryNames 列归档条目名(tar 读取)。
func tarEntryNames(t *testing.T, arc string) []string {
	t.Helper()
	f, err := os.Open(arc)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var out []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, hdr.Name)
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// writeEvilArchive 构造含 ../ 越界条目的归档。
func writeEvilArchive(arc string) error {
	f, err := os.Create(arc)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "../evil.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write([]byte("evil\n")); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// TestBackupViaService 经 sdk.BackupService 接口使用(装配断言)。
func TestBackupViaService(t *testing.T) {
	b := buildHome(t)
	var svc sdk.BackupService = b
	if _, err := svc.Backup(""); err != nil {
		t.Fatal(err)
	}
	if len(svc.List()) == 0 {
		t.Fatal("List 应非空")
	}
}

// writeEvilArchiveMiddle 构造中间段越界的归档条目("a/../../escape.txt"):只拦前缀
// "../" 的实现会放行(Join 归一化后落到 root 之外)。
func writeEvilArchiveMiddle(arc string) error {
	f, err := os.Create(arc)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: "a/../../escape.txt", Mode: 0o644, Size: 5, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write([]byte("evil\n")); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// TestRestoreRejectsMiddleTraversal 中间段 ".." 亦须拒绝(仅查前缀会漏)。
func TestRestoreRejectsMiddleTraversal(t *testing.T) {
	b := buildHome(t)
	arc := filepath.Join(b.backupDir(), "gah-backup-evil2.tar.gz")
	if err := writeEvilArchiveMiddle(arc); err != nil {
		t.Fatal(err)
	}
	if err := b.Restore("gah-backup-evil2.tar.gz"); err == nil {
		t.Fatal("中间段越界条目应中止恢复")
	}
	// 不得在 root 之外落文件
	outside := filepath.Join(filepath.Dir(b.home), "escape.txt")
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("越界文件被写出: %s", outside)
	}
}
