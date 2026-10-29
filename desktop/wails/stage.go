// stage:运行态外置 —— 让用户数据不在应用目录里。
//
// 问题(与 Rust 壳同一个,一个字没改):便携纪律规定数据根 = **二进制同级** `gah-data/`。
// 但桌面壳里 sidecar 位于 `gah.app/Contents/MacOS/gah`(mac)或 NSIS 安装目录(win),
// 于是「二进制同级」= 应用目录内 ⇒ 升级整包替换会带走数据、卸载(NSIS 删安装目录)会删数据。
//
// 解法:壳先把 sidecar **复制到用户数据目录**,再从那里 spawn。二进制落在用户目录 →
// 同级 `gah-data/` 自然在应用目录之外,而数据根解析规则一个字没改:
//
//	mac   ~/Library/Application Support/dev.gah.desktop/bin/{gah-<版本>-<指纹>, gah-data/}
//	win   %LOCALAPPDATA%\dev.gah.desktop\bin\{gah-<版本>-<指纹>.exe, gah-data\}
//
// 副本名 = `gah-<版本>-<内容指纹8>`(内容寻址),一次解决三件事:
//  1. **永不覆盖已存在的文件** —— Windows 不允许覆盖正在运行的 exe,固定名 + 覆盖必然
//     「拒绝访问」(v0.1.3 真机实证);
//  2. 同名 ⇒ 同版本 + 同大小 + 同 mtime ⇒ 内容一致,直接复用(省一次整包复制);
//  3. 不需要额外的「暂存态」标记文件(名字本身就是指纹),少一个会失同步的状态。
package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// portableMarker 便携模式标记文件名(与 Rust 壳同名,升级不打架)。
// 判定只看这个空文件,不用「目录是否可写」这类启发式(U 盘、只读域、企业策略都会误判),
// 也不用命令行参数(快捷方式/文件关联/任务栏固定都传不了)。
const portableMarker = "portable.marker"

// runtimePlan 本次运行落点(装订好后交给 boot)。
type runtimePlan struct {
	bin      string   // 实际要 spawn 的 sidecar
	dataRoot string   // 数据根(便于日志/诊断展示)
	external bool     // 是否外置到用户数据目录
	staged   bool     // 本次是否真的复制了
	notices  []string // 要提示用户的事实(便携模式说明等)
}

// isPortableAt 目录里是否有便携标记。
func isPortableAt(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, portableMarker))
	return err == nil && st.Mode().IsRegular()
}

// dataRoot 外置后的数据根 = 副本同级 `gah-data/`。
func dataRoot(home string) string { return filepath.Join(home, "bin", "gah-data") }

// fnv1a 32 位 FNV-1a(只用于把指纹文本压成 8 位十六进制,不做安全用途)。
func fnv1a(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// stagedName 外置副本的文件名:`gah-<版本>-<指纹8>[.exe]`。
func stagedName(version, fingerprint string) string {
	name := fmt.Sprintf("gah-%s-%08x", version, fnv1a(fingerprint))
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// fingerprint 副本指纹:版本 + 源大小 + 源 mtime(ns)。返回 (指纹文本, 源大小)。
func fingerprint(src, version string) (string, int64, error) {
	st, err := os.Stat(src)
	if err != nil {
		return "", 0, fmt.Errorf("读 %s 失败: %w", src, err)
	}
	mtime := int64(0)
	if t := st.ModTime(); !t.IsZero() {
		mtime = t.UnixNano()
	}
	return fmt.Sprintf("%s\n%d\n%d\n", version, st.Size(), mtime), st.Size(), nil
}

// stageSidecar 把 sidecar 复制到 `<home>/bin/`(必要时),返回要 spawn 的路径。
func stageSidecar(src, home, version string) (runtimePlan, error) {
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return runtimePlan{}, fmt.Errorf("建 %s 失败: %w", binDir, err)
	}
	fp, srcLen, err := fingerprint(src, version)
	if err != nil {
		return runtimePlan{}, err
	}
	dst := filepath.Join(binDir, stagedName(version, fp))
	pruneStale(binDir, dst)

	// 同名 + 同大小 ⇒ 就是我们要的那一份(rename 是原子的,不存在半成品)。
	if st, err := os.Stat(dst); err == nil && st.Size() == srcLen {
		return runtimePlan{bin: dst, dataRoot: dataRoot(home), external: true}, nil
	}
	tmp := dst + ".new"
	_ = os.Remove(tmp)
	if err := copyFile(src, tmp); err != nil {
		return runtimePlan{}, fmt.Errorf("复制 %s → %s 失败: %w", src, tmp, err)
	}
	if err := makeExecutable(tmp); err != nil {
		return runtimePlan{}, err
	}
	stripMarkOfTransfer(tmp)
	// 目标名按内容定,此刻必然不存在 ⇒ rename 不会撞上「正在运行旧副本」的文件锁。
	if err := os.Rename(tmp, dst); err != nil {
		return runtimePlan{}, fmt.Errorf("放下 %s 失败: %w", dst, err)
	}
	return runtimePlan{bin: dst, dataRoot: dataRoot(home), external: true, staged: true}, nil
}

// pruneStale 尽力清掉 bin/ 里的历史副本(旧版本、旧命名、未完成的 .new)。
// 按内容寻址后每次升级都落一个新文件,不清会一直堆(每个 30–45MB)。
// **删不掉一律忽略**:Windows 上「正在运行的旧副本」删不掉是正常的(持有文件锁),
// 下次启动它已经不在了。
func pruneStale(binDir, keep string) {
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(binDir, e.Name())
		if p == keep || e.IsDir() {
			continue // 目录一律不碰:同级 gah-data 是用户数据
		}
		name := e.Name()
		ours := name == "gah" || name == "gah.exe" ||
			strings.HasPrefix(name, "gah-") || strings.HasPrefix(name, ".gah-stage")
		if ours {
			_ = os.Remove(p)
		}
	}
}

// copyFile 复制文件(标准库;保留权限位由 makeExecutable 统一处理)。
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// makeExecutable 给副本加可执行位(unix;windows 无此概念)。
func makeExecutable(p string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if err := os.Chmod(p, 0o755); err != nil {
		return fmt.Errorf("设置可执行位失败: %w", err)
	}
	return nil
}

// stripMarkOfTransfer 去掉「来自网络」标记,免得同一份已被用户放行的程序换个位置又被拦。
//   - mac:复制会带上 `com.apple.quarantine` → 用 xattr -d 清掉(失败忽略:GUI 还会再问)
//   - win:复制会带上备用数据流 Zone.Identifier → 删掉该流(失败忽略)
//
// 便携模式是**就地跑**,没有复制那一环,所以壳启动时自己清:从网上下载的 zip 解压出来的
// sidecar 带标记时,spawn 会静默失败(表现为「窗口开了但没反应」)。
func stripMarkOfTransfer(p string) {
	if runtime.GOOS == "darwin" {
		_ = exec.Command("/usr/bin/xattr", "-d", "com.apple.quarantine", p).Run()
		return
	}
	if runtime.GOOS == "windows" {
		_ = os.Remove(p + ":Zone.Identifier")
	}
}

// migrateFrom 一次性迁移:旧数据在应用目录内且新位置还没有数据时复制过去。
// **只复制不删除**(旧副本留在原地,最坏情况用户仍能找回)。
func migrateFrom(legacy, home string) (string, error) {
	if _, err := os.Stat(legacy); err != nil {
		return "", nil // 没有旧数据:不是错误
	}
	target := dataRoot(home)
	if _, err := os.Stat(target); err == nil {
		return "", nil // 新位置已有数据:绝不合并(覆盖等于丢数据)
	}
	if err := copyTree(legacy, target); err != nil {
		return "", fmt.Errorf("迁移 %s → %s 失败: %w", legacy, target, err)
	}
	return target, nil
}

// copyTree 递归复制目录(符号链接按文件复制,与 Rust 壳同口径)。
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		from := filepath.Join(src, e.Name())
		to := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyTree(from, to); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(from, to); err != nil {
			return err
		}
	}
	return nil
}

// resolveRuntime 决定本次从哪个 sidecar 跑、数据根在哪。
//
// 三种形态:
//  1. `GAH_SIDECAR` 显式指定(开发/CI):就地跑,数据根 = 该二进制同级 gah-data/;
//  2. **便携模式**(exe 同级有 `portable.marker`):就地跑 —— 外置是为**安装版**做的
//     (NSIS 删安装目录会连带删数据);便携用户的直觉恰恰是「删目录即删数据」,
//     复制反而让「数据在哪」不可预期;
//  3. 安装版:外置到用户数据目录(见文件头),并把旧位置的数据一次性迁过去。
func resolveRuntime(home, version string) (runtimePlan, error) {
	// 发行构建优先:内嵌 sidecar(见 embed_sidecar.go 的三条理由)。
	// 显式 GAH_SIDECAR 仍然最高优先 —— 开发/演练时要能指向任意 gah 产物。
	if len(sidecarGz) > 0 && os.Getenv("GAH_SIDECAR") == "" {
		return stageEmbedded(home, version)
	}
	if v := os.Getenv("GAH_SIDECAR"); v != "" {
		if _, err := os.Stat(v); err != nil {
			return runtimePlan{}, fmt.Errorf("GAH_SIDECAR 指向的文件不存在: %w", err)
		}
		return runtimePlan{
			bin:      v,
			dataRoot: filepath.Join(filepath.Dir(v), "gah-data"),
		}, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return runtimePlan{}, fmt.Errorf("取壳自身路径失败: %w", err)
	}
	dir := filepath.Dir(exe)
	// 随包 sidecar:发行布局是「与壳同目录」;开发布局是 binaries/gah-<triple>。
	src := filepath.Join(dir, sidecarName())
	if st, err := os.Stat(src); err != nil || st.IsDir() {
		// 开发布局:产物在 <壳工程>/binaries/gah-<triple>,壳在 <壳工程>/build/(同级上一层的 binaries),
		// 所以两种相对位置都试(发行布局仍是「与壳同目录」)。
		cands := []string{
			filepath.Join(dir, "binaries", sidecarName()+"-"+triple()),
			filepath.Join(dir, "..", "binaries", sidecarName()+"-"+triple()),
		}
		found := ""
		for _, c := range cands {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				found = c
				break
			}
		}
		if found == "" {
			return runtimePlan{}, fmt.Errorf("找不到 sidecar %s(试过 %s、%s;开发态先跑 bash scripts/gen-desktop-wails.sh,或设 GAH_SIDECAR)",
				sidecarName(), filepath.Join(dir, sidecarName()), strings.Join(cands, "、"))
		}
		src = found
	}

	if isPortableAt(dir) {
		// 就地跑 ⇒ 没有复制那一环,「来自网络」标记得自己清(否则 sidecar 静默起不来)。
		stripMarkOfTransfer(src)
		stripMarkOfTransfer(exe)
		root := filepath.Join(dir, "gah-data")
		plan := runtimePlan{bin: src, dataRoot: root}
		if !portableNoticeShown(root) {
			if err := markPortableNoticeShown(root); err != nil {
				// 标记写不下去只是退回「每次都提示」,不该拦住启动。
				shellLogf("便携提示标记写入失败(将每次启动都提示): %v", err)
			}
			plan.notices = []string{
				"便携模式:数据在本目录的 gah-data/(换机器请整个目录一起拷)",
				"便携版不自动更新(Windows 不允许覆盖正在运行的程序):请下载新包覆盖本目录",
			}
		}
		return plan, nil
	}

	plan, err := stageSidecar(src, home, version)
	if err != nil {
		return runtimePlan{}, err
	}
	// 旧位置(应用目录内)的数据一次性搬过来:只复制不删除。
	if moved, err := migrateFrom(filepath.Join(dir, "gah-data"), home); err != nil {
		// 迁移失败不拦住启动:退回外置目录继续跑,并留一条可查的痕迹。
		shellLogf("迁移旧数据失败(继续用 %s): %v", plan.dataRoot, err)
	} else if moved != "" {
		shellLogf("旧数据已迁移到 %s", moved)
	}
	return plan, nil
}

// portableNoticeShown 便携形态说明「一次讲清就够」:标记落在数据根里(便携纪律),
// 删掉它就会再提示一次 —— 这是有意的出口。
func portableNoticeShown(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".desktop-portable-notice"))
	return err == nil
}

func markPortableNoticeShown(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(root, ".desktop-portable-notice"),
		[]byte(time.Now().Format(time.RFC3339)+"\n"), 0o600)
}

// isErrNotExist 小工具(测试里区分「不存在」与「其他错误」)。
func isErrNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }

// stageEmbedded 把内嵌的 sidecar(gzip)解到 `<home>/bin/gah-<版本>-<内容指纹8>`,
// 返回可执行路径。已存在同指纹副本时直接复用(不重复解压)。
//
// 指纹用**内嵌字节的 sha256**,与磁盘 mtime 无关 —— 同一份壳二进制每次启动都得到同一个
// 名字,于是「解压一次、之后永久复用」成立(旧的文件外置靠源文件 mtime,重建就会换名字)。
func stageEmbedded(home, ver string) (runtimePlan, error) {
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return runtimePlan{}, fmt.Errorf("建 %s 失败: %w", binDir, err)
	}
	sum := sha256.Sum256(sidecarGz)
	dst := filepath.Join(binDir, stagedName(ver, hex.EncodeToString(sum[:])))
	pruneStale(binDir, dst)

	if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
		return runtimePlan{bin: dst, dataRoot: dataRoot(home), external: true}, nil
	}
	tmp := dst + ".new"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return runtimePlan{}, fmt.Errorf("写下 %s 失败: %w", tmp, err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(sidecarGz))
	if err != nil {
		_ = out.Close()
		return runtimePlan{}, fmt.Errorf("内嵌 sidecar 不是合法 gzip: %w", err)
	}
	if _, err := io.Copy(out, gz); err != nil {
		_ = out.Close()
		return runtimePlan{}, fmt.Errorf("解压内嵌 sidecar 失败: %w", err)
	}
	if err := out.Close(); err != nil {
		return runtimePlan{}, err
	}
	if err := makeExecutable(tmp); err != nil {
		return runtimePlan{}, err
	}
	stripMarkOfTransfer(tmp)
	if err := os.Rename(tmp, dst); err != nil {
		return runtimePlan{}, fmt.Errorf("放下 %s 失败: %w", dst, err)
	}
	return runtimePlan{bin: dst, dataRoot: dataRoot(home), external: true, staged: true}, nil
}
