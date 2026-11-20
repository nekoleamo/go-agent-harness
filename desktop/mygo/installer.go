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

// keys map 的键(日志用;顺序固定为字典序,便于比对)。
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
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
	shellLogf("安装器: 等 pid=%d 退出 → 换 %s", plan.WaitPID, plan.Target)
	if !waitProcessExit(plan.WaitPID, 10*time.Minute) {
		return fmt.Errorf("等旧进程退出超时(10 分钟),放弃本次安装(目标未改动)")
	}
	old := plan.Target + ".old-" + strconv.FormatInt(time.Now().Unix(), 10)
	if err := os.Rename(plan.Target, old); err != nil {
		return fmt.Errorf("移开旧目标失败(未改动任何文件): %w", err)
	}
	if err := os.Rename(plan.PayloadTop, plan.Target); err != nil {
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
