// sidecar: 把 gah --profile web 拉起来、把它的日志落进壳日志、并在壳退出时带走它。
//
// 与 Rust 壳同口径的三条不变量:
//  1. **不传 GAH_HOME**:数据根唯一 = sidecar 二进制同级 gah-data/,壳不替它做主;
//  2. `GAH_WEB_PARENT_WATCH=1` + 一根常开的 stdin 管道:壳被强杀/崩溃时写端关闭,
//     sidecar 读到 EOF 自己退,不留占着数据根和监听口的孤儿(那正是「升级后界面还是旧的」的成因);
//  3. 端口由壳挑、经 `GAH_WEB_ADDR` 交给 sidecar,固定 2233 只作兜底。
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// sidecarProc 壳对 sidecar 的全部掌控面。
type sidecarProc struct {
	cmd    *exec.Cmd
	bin    string
	stdin  *os.File // 写端:壳活着就一直开着(父死子死靠它)
	exited atomic.Bool
	once   sync.Once
}

// sidecarName sidecar 在磁盘上的文件名。
func sidecarName() string {
	if runtime.GOOS == "windows" {
		return "gah.exe"
	}
	return "gah"
}

// triple 当前平台的 Rust/Go 二进制品名后缀(沿用 gen-desktop.sh 的命名)。
func triple() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		// ARM 在 Rust target triple 里叫 aarch64(gen-desktop*.sh 的产物名同源)。
		// 写成 arm64 会让壳在 arm mac 上「找不到 sidecar」—— 实测踩过一次。
		arch = "aarch64"
	}
	switch runtime.GOOS {
	case "darwin":
		return arch + "-apple-darwin"
	case "windows":
		return arch + "-pc-windows-msvc"
	default:
		return arch + "-unknown-linux-gnu"
	}
}

// startSidecar 拉起 sidecar 并接管它的日志。
func startSidecar(bin, addr, token string) (*sidecarProc, error) {
	cmd := exec.Command(bin, "--profile", "web")
	cmd.Env = append(os.Environ(),
		"GAH_WEB_OPEN=0",         // 壳自己开窗口,不让 gah 再开浏览器
		"GAH_WEB_ADDR="+addr,     // 本次私有端口
		"GAH_WEB_PARENT_WATCH=1", // 父死子死(见文件头)
	)
	// 初始工作目录:真机上壳的 cwd 可能是 C:\WINDOWS\system32 或 /,让 gah 以它为
	// 「默认工作区」会莫名其妙。用一个可预期的家目录;显式 env 优先。
	cwd := os.Getenv("GAH_DESKTOP_CWD")
	if cwd == "" {
		if h, err := os.UserHomeDir(); err == nil {
			cwd = h
		}
	}
	if cwd != "" {
		if st, err := os.Stat(cwd); err == nil && st.IsDir() {
			cmd.Dir = cwd
		}
	}

	// stdin 管道:壳持有写端并**永不关闭**,直到进程退出(OS 自动关)。
	// sidecar 侧 io.Copy(io.Discard, os.Stdin) 读到 EOF 即知父已死。
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("建 stdin 管道失败: %w", err)
	}
	cmd.Stdin = inR
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, fmt.Errorf("建 stdout 管道失败: %w", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		return nil, fmt.Errorf("建 stderr 管道失败: %w", err)
	}
	cmd.Stdout = outW
	cmd.Stderr = errW

	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{inR, inW, outR, outW, errR, errW} {
			_ = f.Close()
		}
		return nil, fmt.Errorf("启动 sidecar 失败: %w", err)
	}
	// 父进程侧不需要这几个副本,立刻关掉(否则子进程永远读不到 EOF —— 连正常退出也读不到)。
	_ = inR.Close()
	_ = outW.Close()
	_ = errW.Close()

	sc := &sidecarProc{cmd: cmd, bin: bin, stdin: inW}
	go pump(outR, "sidecar(out)", true)
	go pump(errR, "sidecar", false)
	return sc, nil
}

// pump 把 sidecar 的一条输出管道按行搬进壳日志。
//
// stderr 一律保留(服务端错误如「端口占用」「附件不可用」只会出现在这里),
// 但滤掉 go-plugin 的 `[DEBUG]` 噪音(一次工作区切换能刷几十行,会把真正的原因挤出屏幕)。
// stdout 是 go-plugin 的**二进制 RPC 通道**,只能兜「看起来是给人读的日志行」,
// 且设 200 行上限 —— 协议帧倒进日志会把文件撑爆且不可读。
func pump(r io.ReadCloser, prefix string, textOnly bool) {
	defer func() { _ = r.Close() }()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	logged := 0
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.Contains(line, "[DEBUG]") || line == "" {
			continue
		}
		if textOnly && !looksLikeLogLine(line) {
			continue
		}
		if textOnly && logged >= 200 {
			continue
		}
		logged++
		shellLogf("%s: %s", prefix, line)
	}
}

// looksLikeLogLine 粗判一行是不是「给人读的日志」(可打印 ASCII/UTF-8 且不含控制字符)。
func looksLikeLogLine(s string) bool {
	if len(s) > 4096 {
		return false
	}
	bad := 0
	for _, r := range s {
		if r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			bad++
		}
	}
	return bad*20 < len(s) // 控制字符超过 5% 就当二进制帧
}

// stop 优雅停 sidecar:先 POST /api/shutdown,等端口释放,超时强杀。
// 幂等(once):退出链上可能被调用两次(OnShutdown + Run 返回后兜底)。
func (s *sidecarProc) stop(addr, token string) {
	if s == nil {
		return
	}
	s.once.Do(func() {
		if s.cmd.Process == nil || s.exited.Load() {
			return
		}
		shellLogf("退出链: POST /api/shutdown → %s", addr)
		if err := postShutdown(addr, token); err != nil {
			shellLogf("退出链: shutdown 请求失败(%v),继续等端口释放", err)
		}
		if waitPortReleased(addr, 5*time.Second) {
			shellLogf("退出链: 端口已释放")
		} else {
			shellLogf("退出链: 5s 内端口未释放,强杀 sidecar")
			_ = s.cmd.Process.Kill()
		}
		_ = s.stdin.Close()
	})
}

// wait 等进程退出(阻塞);日志留在壳日志里。
func (s *sidecarProc) wait() error {
	err := s.cmd.Wait()
	s.exited.Store(true)
	return err
}
