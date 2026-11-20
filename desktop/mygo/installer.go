// installer:把下载好的更新包换成程序本体(Windows exe 替换 / macOS .app 交换)。
//
// 为什么必须是一个独立进程:
//   - Windows 不允许覆盖**正在运行**的 exe;
//   - macOS 的 .app 也不能在自己运行时被整包替换(可执行文件就在被换的那棵树里)。
//
// 做法(与 Wails v3 / 旧 Rust 壳同一套思路):壳把已校验的产物解包到临时目录,再**复制自己**
// 到临时目录当安装器,带着一份 plan(等谁退出、换哪个目标、换完是否重启)起来;壳随后退出,
// 安装器等它退出后 rename 旧目标 → 放新目标 → 重启 → 清理。
//
// 安全前提:产物字节在壳里已经过 sha256 + ed25519 校验(见 updater.go 的 stageUpdatePayload),
// 安装器只负责搬运,不再联网、不再做信任判断 —— 它的输入只有那份 plan。
package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// installFlag 安装器模式开关(由壳自己再拉起一次时带上)。
const installFlag = "--install-update"

// installPlan 一次换文件任务的完整输入。
type installPlan struct {
	WaitPID    int    `json:"wait_pid"`    // 等这个进程退出后再动手
	Target     string `json:"target"`      // 要替换的目标(win: exe 路径;mac: .app 路径)
	PayloadTop string `json:"payload_top"` // 新目标的完整路径(从临时目录解出来的顶层条目)
	Relaunch   bool   `json:"relaunch"`    // 换完是否拉起新版本
}

// installerMode 当前进程是否以安装器身份启动(argv 里有 --install-update)。
func installerMode() (string, bool) {
	for i, a := range os.Args[1:] {
		if a == installFlag && len(os.Args) > i+2 {
			return os.Args[i+2], true
		}
		if strings.HasPrefix(a, installFlag+"=") {
			return strings.TrimPrefix(a, installFlag+"="), true
		}
	}
	return "", false
}

// installTarget 当前运行形态下要替换的目标路径。
//
// 开发态(不在 .app 里 / 从源码目录跑)一律拒绝:换错目标等于把用户的程序弄坏。
func installTarget() (string, error) {
	if runtime.GOOS == "darwin" {
		b := bundlePath()
		if b == "" {
			return "", fmt.Errorf("当前不是从 .app 包运行(开发态),不能自动安装更新")
		}
		return b, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("取程序路径失败: %w", err)
	}
	return exe, nil
}

// extractUpdate 解包到 dir/payload,返回唯一的顶层条目路径。
//
// 只接受 zip(与发布链一致),并且要求**恰好一个**顶层条目 —— 多条目意味着发布包结构变了,
// 静默挑一个会把「更新成了别的东西」变成事实。
func extractUpdate(archivePath, dir string) (string, error) {
	if !strings.HasSuffix(strings.ToLower(archivePath), ".zip") {
		return "", fmt.Errorf("不支持的更新包格式(只接受 zip):%s", filepath.Base(archivePath))
	}
	dest := filepath.Join(dir, "payload")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("建解包目录失败: %w", err)
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("打开更新包失败: %w", err)
	}
	defer func() { _ = zr.Close() }()

	tops := map[string]bool{}
	for _, f := range zr.File {
		clean := filepath.Clean(f.Name)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return "", fmt.Errorf("更新包里有非法路径: %q", f.Name)
		}
		top := strings.SplitN(clean, string(filepath.Separator), 2)[0]
		tops[top] = true
		out := filepath.Join(dest, clean)
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(out, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return "", err
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		// 权限位跟着包走(mac 的 .app 内层要可执行)。
		mode := f.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			_ = rc.Close()
			return "", err
		}
		_, cerr := io.Copy(w, rc)
		_ = rc.Close()
		if cerr == nil {
			cerr = w.Close()
		} else {
			_ = w.Close()
		}
		if cerr != nil {
			return "", cerr
		}
	}
	if len(tops) != 1 {
		return "", fmt.Errorf("更新包应只有一个顶层条目,实际有 %d 个(%v)", len(tops), keys(tops))
	}
	var top string
	for k := range tops {
		top = k
	}
	return filepath.Join(dest, top), nil
}

// keys map 的键(日志用;排序成字典序,便于两次日志直接 diff)。
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// spawnInstaller 把**自己**复制成临时目录里的安装器并拉起,返回 plan 路径。
//
// 复制自己而不是内置一个小 helper 二进制:helper 也要随包构建/签名/版本对齐,
// 而「当前这个 exe 本身」天然与壳同版本,且它换完自己就会被删掉。
func spawnInstaller(plan installPlan, workDir string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("取壳自身路径失败: %w", err)
	}
	inst := filepath.Join(workDir, "gah-installer")
	if runtime.GOOS == "windows" {
		inst += ".exe"
	}
	src, err := os.ReadFile(self)
	if err != nil {
		return fmt.Errorf("读壳本体失败: %w", err)
	}
	if err := os.WriteFile(inst, src, 0o755); err != nil {
		return fmt.Errorf("写安装器失败: %w", err)
	}
	planPath := filepath.Join(workDir, "install.json")
	b, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	if err := os.WriteFile(planPath, b, 0o600); err != nil {
		return fmt.Errorf("写安装计划失败: %w", err)
	}
	cmd := exec.Command(inst, installFlag, planPath)
	cmd.Dir = workDir
	detach(cmd) // 脱离壳的进程组:壳退出不能把它一起收掉
	hideConsole(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("拉起安装器失败: %w", err)
	}
	go func() { _ = cmd.Wait() }() // 收尸,免得留僵尸
	shellLogf("安装器已接手: pid=%d plan=%s", cmd.Process.Pid, planPath)
	return nil
}

// installerMain 安装器进程的入口(壳不会再走到这里)。
//
// 顺序固定:等旧进程退出 → rename 旧目标 → 放新目标 → 重启 → 清理。
// 任何一步失败都写壳日志并**保留旧目标**(宁可更新没做成,不能留下一个空目录)。
func installerMain(planPath string) error {
	b, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("读安装计划失败: %w", err)
	}
	var plan installPlan
	if err := json.Unmarshal(b, &plan); err != nil {
		return fmt.Errorf("安装计划解析失败: %w", err)
	}
	if err := validatePlan(planPath, plan); err != nil {
		return err
	}
	shellLogf("安装器: 等 pid=%d 退出 → 换 %s", plan.WaitPID, plan.Target)
	if !waitProcessExit(plan.WaitPID, 10*time.Minute) {
		return fmt.Errorf("等旧进程退出超时(10 分钟),放弃本次安装(目标未改动)")
	}
	old := plan.Target + ".old-" + strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.Rename(plan.Target, old); err != nil {
		return fmt.Errorf("移开旧目标失败(未改动任何文件): %w", err)
	}
	if err := moveInto(plan.PayloadTop, plan.Target); err != nil {
		// 放不进去就把旧的搬回来 —— 用户必须还有一个能启动的程序。
		if rerr := os.Rename(old, plan.Target); rerr != nil {
			return fmt.Errorf("放新目标失败(%v),且回滚也失败(%v):请手动重装", err, rerr)
		}
		return fmt.Errorf("放新目标失败(已回滚到旧版本): %w", err)
	}
	shellLogf("安装器: 已换上新版本 %s", plan.Target)

	if plan.Relaunch {
		if err := relaunchTarget(plan.Target); err != nil {
			shellLogf("安装器: 拉起新版本失败(请手动打开): %v", err)
		}
	}
	// 清理:Windows 上旧 exe 可能被占用,删不掉就留给下次启动的 pruneOldTargets。
	if err := os.RemoveAll(old); err != nil {
		shellLogf("安装器: 清理旧版本失败(下次启动再清): %v", err)
	}
	// 临时目录(内含安装器自己):尽力删,删不掉不影响用户。
	_ = os.RemoveAll(filepath.Dir(planPath))
	return nil
}

// moveInto 把解包好的新目标搬到安装位置。
//
// 优先 rename(同一卷上原子、瞬时);跨卷时(临时目录在另一块盘 —— /var/folders 与
// 外接盘上的安装目录、或用户把 .app 放在另一个 APFS 卷上)rename 会返回 EXDEV,
// 那时**退回复制**:更新慢一点,但不会因为"放不进去"把用户停在旧版本上。
func moveInto(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyPath(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

// copyPath 复制文件或目录树(保留权限位;安装目标里的可执行位必须跟着走)。
func copyPath(src, dst string) error {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return copyOne(src, dst, st.Mode().Perm())
	}
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyOne(p, out, info.Mode().Perm())
	})
}

// copyOne 复制单个文件。
func copyOne(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	if cerr == nil {
		cerr = out.Close()
	} else {
		_ = out.Close()
	}
	return cerr
}

// validatePlan 安装计划的输入校验。
//
// 威胁模型写清楚:安装器**不做信任判断**(产物字节在壳里已验过 sha256 + ed25519),它只搬运。
// 但它能被命令行直接拉起(`gah-desktop --install-update <plan>`),所以至少要挡住
// 「用别的目录里的计划去 rename 任意文件」这条路:计划必须与安装器同处临时目录,
// 目标与载荷都必须写绝对路径。攻击者若已能以用户身份跑任意程序,本来就能直接动文件 ——
// 这层是防误用与防"被诱导执行一份奇怪的计划",不是防本地提权。
func validatePlan(planPath string, plan installPlan) error {
	if plan.Target == "" || !filepath.IsAbs(plan.Target) {
		return fmt.Errorf("安装计划里的目标不是绝对路径(%q),拒绝执行", plan.Target)
	}
	if plan.PayloadTop == "" || !filepath.IsAbs(plan.PayloadTop) {
		return fmt.Errorf("安装计划里的载荷不是绝对路径(%q),拒绝执行", plan.PayloadTop)
	}
	if plan.WaitPID < 0 {
		return fmt.Errorf("安装计划里的 pid 非法(%d)", plan.WaitPID)
	}
	dir := filepath.Dir(planPath)
	tmp := filepath.Clean(os.TempDir())
	clean := filepath.Clean(dir)
	if clean != tmp && !strings.HasPrefix(clean, tmp+string(filepath.Separator)) {
		return fmt.Errorf("安装计划不在临时目录内(%s),拒绝执行", clean)
	}
	if st, err := os.Stat(plan.PayloadTop); err != nil || (!st.IsDir() && st.Size() == 0) {
		return fmt.Errorf("安装载荷不存在或为空(%s),拒绝执行", plan.PayloadTop)
	}
	return nil
}

// relaunchTarget 拉起换好的新版本。
func relaunchTarget(target string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" && strings.HasSuffix(target, ".app") {
		// mac 走 LaunchServices:直接 exec 内层二进制不会作为正规应用启动。
		cmd = exec.Command("open", "-n", target)
	} else {
		cmd = exec.Command(target)
	}
	detach(cmd)
	hideConsole(cmd)
	return cmd.Start()
}

// pruneOldTargets 启动时清理上次没删掉的旧版本(Windows 上被占用的 exe 会留到这时)。
func pruneOldTargets() {
	target, err := installTarget()
	if err != nil {
		return
	}
	dir := filepath.Dir(target)
	base := filepath.Base(target)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), base+".old-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			shellLogf("清理旧版本失败(不影响使用): %v", err)
			continue
		}
		shellLogf("已清理旧版本: %s", e.Name())
	}
}
