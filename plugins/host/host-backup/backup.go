// Package hostbackup 提供 host-backup 插件(M18):GAH_HOME 单根数据整体备份/恢复。
// 落便携纪律:备份目录默认 $GAH_HOME/backups/(运行数据随单根迁移),支持外部 dest;
// 归档排除 backups/ 自身(防递归膨胀);tar.gz 确定性(源不变重跑无 diff),时间戳命名。
// 恢复为危险操作:恢复前先自动备份当前状态(安全默认),覆盖落盘,重启生效。
package hostbackup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 备份目录子目录名(排除自身)。
const backupsDir = "backups"

// 自动备份保留份数(backup_on_start 轮转)。
const defaultKeep = 5

// 选择器哨兵:立即备份(默认目录)、备份到自定义路径(二级自由输入)。
const (
	backupNowSentinel    = "__now__"
	backupCustomSentinel = "__custom__"
)

// Plugin 实现 host-backup。requires ctx.commands(可选:未装配跳过命令注册)。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-backup" }

// Start 提供 ctx.backup 服务并注册 /backup 命令。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	b := &Backup{home: home()}
	if m != nil && m.Data != nil {
		if k, ok := m.Data["keep"].(int); ok && k > 0 {
			b.keep = k
		}
	}
	if err := c.Provide("ctx.backup", b); err != nil {
		return nil, err
	}
	var disposers []sdk.Disposer
	var cmds sdk.CommandRegistry
	_ = c.Inject("ctx.commands", &cmds)
	if cmds != nil {
		d, err := cmds.Register(sdk.CommandSpec{
			Name:  "backup",
			Usage: "/backup now|[dest 路径]|list|restore <name>",
			Desc:  "整体备份/恢复(GAH_HOME 单根;restore 前自动先备份当前态)",
			Run:   func(args []string) (string, error) { return backupCmd(args, b) },
			// 逐级确认:一级 立即备份(默认目录)/list/restore/自定义路径哨兵;二级 restore 枚举备份、自定义路径自由输入。
			Args: []sdk.ArgLevel{
				{Options: func([]string) []sdk.Option {
					return []sdk.Option{
						{Value: backupNowSentinel, Desc: "立即备份到默认目录 " + backupsDir + "/"},
						{Value: "list", Desc: "列出已有备份"},
						{Value: "restore", Desc: "恢复备份(先自动备份当前态)"},
						{Value: backupCustomSentinel, Desc: "备份到自定义路径…"},
					}
				}},
				{
					Options: func(picked []string) []sdk.Option {
						if len(picked) < 2 || picked[1] != "restore" {
							return nil
						}
						opts := make([]sdk.Option, 0, 8)
						for _, bi := range b.List() {
							opts = append(opts, sdk.Option{Value: bi.Name, Desc: fmt.Sprintf("%s (%dKB)", time.Unix(bi.Time, 0).Format("01-02 15:04"), bi.Size/1024)})
						}
						return opts
					},
					FreeArgs: func(picked []string) []string {
						if len(picked) >= 2 && picked[1] == backupCustomSentinel {
							return []string{"目标路径"}
						}
						return nil
					},
				},
			},
		})
		if err == nil {
			disposers = append(disposers, d)
		}
	}
	// 可选启动自动备份(data.backup_on_start true):后台异步,不阻塞装配;轮转保留 keep 份
	if m != nil && m.Data != nil {
		if on, ok := m.Data["backup_on_start"].(bool); ok && on {
			go func() {
				if _, err := b.Backup(""); err != nil {
					_, _ = fmt.Fprintf(os.Stderr, "host-backup: 自动备份失败: %v\n", err)
					return
				}
				b.rotate()
			}()
		}
	}
	return func() {
		for i := len(disposers) - 1; i >= 0; i-- {
			disposers[i]()
		}
	}, nil
}

// Backup 实现 sdk.BackupService(host-backup)。
type Backup struct {
	home string // GAH_HOME(单根;空 = 无数据可备份,显式报错)
	keep int    // 自动备份轮转保留份数(0 = 默认)
}

// backupDir 默认备份目录(GAH_HOME 未设 = 空,表示不可用)。
func (b *Backup) backupDir() string {
	if b.home == "" {
		return ""
	}
	return filepath.Join(b.home, backupsDir)
}

// keepN 轮转保留份数(defaultKeep 兜底)。
func (b *Backup) keepN() int {
	if b.keep <= 0 {
		return defaultKeep
	}
	return b.keep
}

// Name / Backup / List / Restore 实现 sdk.BackupService。
func (b *Backup) Name() string { return "host-backup" }

// Backup 打包整个 GAH_HOME(排除 backups/ 自身与 dest 目标)为时间戳命名归档。
// dest 为空 = 默认备份目录;dest 为外部路径 → 归档写入该路径(相对名/绝对路径返回)。
// 返回:默认目录 → 相对归档名;外部 dest → 完整路径。
func (b *Backup) Backup(dest string) (string, error) {
	if b.home == "" {
		return "", fmt.Errorf("GAH_HOME 未设置,无数据可备份")
	}
	name := "gah-backup-" + time.Now().Format("20060102-150405") + ".tar.gz"
	out := dest
	if out == "" {
		if err := os.MkdirAll(b.backupDir(), 0o755); err != nil {
			return "", fmt.Errorf("备份目录创建失败: %w", err)
		}
		out = filepath.Join(b.backupDir(), name)
		// 同秒重跑防覆盖(restore 前自动备份等场景):追加序号 -1/-2…
		for i := 1; ; i++ {
			if _, err := os.Stat(out); os.IsNotExist(err) {
				break
			}
			name = fmt.Sprintf("gah-backup-%s-%d.tar.gz", time.Now().Format("20060102-150405"), i)
			out = filepath.Join(b.backupDir(), name)
		}
	}
	// 防自包:dest 落在 home 内且属于被备份根 → 从排除集合并(同 backups/)
	exclude := map[string]bool{backupsDir: true}
	if rel, err := filepath.Rel(b.home, out); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		exclude[rel] = true
	}
	if err := writeTarGz(out, b.home, exclude); err != nil {
		return "", err
	}
	if dest != "" {
		return out, nil
	}
	return name, nil
}

// List 默认备份目录中的归档(时间倒序,最新在前)。
func (b *Backup) List() []sdk.BackupInfo {
	dir := b.backupDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []sdk.BackupInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "gah-backup-") || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, sdk.BackupInfo{Name: e.Name(), Size: info.Size(), Time: info.ModTime().Unix()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out
}

// Restore 恢复指定归档:先自动备份当前状态(安全默认),再解压覆盖 home 内容。
// 覆盖运行态文件不中断(重启生效);调用方负责二次确认。
func (b *Backup) Restore(name string) error {
	if b.home == "" {
		return fmt.Errorf("GAH_HOME 未设置")
	}
	name = filepath.Base(name) // 防路径穿越:仅接受归档名
	arc := filepath.Join(b.backupDir(), name)
	if _, err := os.Stat(arc); err != nil {
		return fmt.Errorf("归档不存在: %s", name)
	}
	// 先自动备份当前状态(安全默认:恢复出错可回退)
	if _, err := b.Backup(""); err != nil {
		return fmt.Errorf("恢复前自动备份当前状态失败(已中止): %w", err)
	}
	if err := extractTarGz(arc, b.home); err != nil {
		return fmt.Errorf("恢复失败(恢复前快照已留于 backups/): %w", err)
	}
	return nil
}

// rotate 自动备份轮转:仅保留最近 keepN 份,删除更旧归档。
func (b *Backup) rotate() {
	list := b.List()
	for i := b.keepN(); i < len(list); i++ {
		_ = os.Remove(filepath.Join(b.backupDir(), list[i].Name))
	}
}

// home GAH_HOME(单根;与 cmd/gah homeDir 链一致:env 优先)。
func home() string {
	return os.Getenv("GAH_HOME")
}

// writeTarGz 确定性 tar.gz 打包(归档内容按源文件 mtime/权限;gzip -n 等价:头不写时间戳)。
func writeTarGz(out, root string, exclude map[string]bool) error {
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("归档创建失败: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewWriterLevel(f, gzip.DefaultCompression)
	if err != nil {
		return err
	}
	gz.Header.ModTime = time.Time{} // 确定性:gzip 头时间戳置零(对齐 -n)
	gz.Header.Name = ""             // 不写原文件名(对齐 -n)
	tw := tar.NewWriter(gz)
	err = walkTar(tw, root, filepath.Clean(root), exclude)
	if err == nil {
		err = tw.Close()
	}
	gzErr := gz.Close()
	if err == nil {
		err = gzErr
	}
	return err
}

// walkTar 递归写入 root 下内容(相对路径 + 顶层目录项;跳过 exclude 顶层)。
func walkTar(tw *tar.Writer, root, cleanRoot string, exclude map[string]bool) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(cleanRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil // 顶层目录本身不写(恢复时解压到 home 根)
		}
		rel = filepath.ToSlash(rel)
		top := strings.SplitN(rel, "/", 2)[0]
		if exclude[top] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.IsDir() {
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, src)
			src.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// extractTarGz 解压归档到 root 根(仅接受相对路径条目,防穿越)。
func extractTarGz(arc, root string) error {
	f, err := os.Open(arc)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("坏归档(gzip 解压失败): %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("坏归档(tar 读取失败): %w", err)
		}
		name := filepath.FromSlash(hdr.Name)
		dst := filepath.Join(root, name)
		// 越界防护:Join 会 Clean 掉中间 ".." 段("a/../../x"),只看前缀会漏;
		// 归一化后按 Rel 判定必须仍在 root 内(含绝对路径与 Windows 盘符情形)。
		if rel, rerr := filepath.Rel(root, dst); rerr != nil || filepath.IsAbs(name) ||
			rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("归档含越界条目,已中止: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			of, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			_, err = io.Copy(of, tr)
			of.Close()
			if err != nil {
				return err
			}
			// 其余类型(符号链接/设备等)跳过:备份数据文件为主,链接目标跨机不可靠
		}
	}
	return nil
}

// backupCmd /backup 命令执行:无参 = 备份到默认目录;list = 列出;restore <name> = 恢复。
func backupCmd(args []string, b *Backup) (string, error) {
	if len(args) == 0 || args[0] == backupNowSentinel {
		name, err := b.Backup("")
		if err != nil {
			return "", err
		}
		return "已整体备份 GAH_HOME → " + filepath.Join(b.backupDir(), name), nil
	}
	switch args[0] {
	case "list":
		list := b.List()
		if len(list) == 0 {
			return "备份: (无;运行 /backup 创建第一份)", nil
		}
		var sb strings.Builder
		sb.WriteString("备份(最新在前):")
		for _, bi := range list {
			fmt.Fprintf(&sb, "\n  %s  %s (%dKB)", time.Unix(bi.Time, 0).Format("01-02 15:04"), bi.Name, bi.Size/1024)
		}
		return sb.String(), nil
	case "restore":
		if len(args) < 2 {
			return "", fmt.Errorf("/backup restore <name>(/backup list 查看;恢复前自动先备份当前态)")
		}
		if err := b.Restore(args[1]); err != nil {
			return "", err
		}
		return "已从 " + args[1] + " 恢复(恢复前当前态已自动备份进 backups/;重启后完全生效)", nil
	default:
		// dest 路径备份(外部路径/目录);~/ 展开与 /workspace 同款(host-internal-commands)
		dest := strings.TrimSpace(strings.TrimPrefix(args[0], backupCustomSentinel))
		if dest == "" {
			return "", fmt.Errorf("/backup: 未给目标路径(如 /backup /tmp/x.tar.gz 或 /backup ~/Desktop)")
		}
		if dest == "~" || strings.HasPrefix(dest, "~/") {
			if uh, err := os.UserHomeDir(); err == nil {
				dest = filepath.Join(uh, strings.TrimPrefix(dest, "~"))
			}
		}
		name, err := b.Backup(dest)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(dest, "/") || strings.HasPrefix(dest, "~") {
			return "已整体备份 GAH_HOME → " + name, nil
		}
		return "已整体备份 GAH_HOME → " + filepath.Join(b.backupDir(), name), nil
	}
}
