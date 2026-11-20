// sidecar:拉起 gah --profile web、把它的日志收进壳日志、并在壳退出时带走它。
//
// 与 Wails 壳同口径的三条不变量:
//  1. **不传 GAH_HOME**:数据根唯一 = sidecar 二进制同级 gah-data/,壳不替它做主;
//  2. GAH_WEB_PARENT_WATCH=1 + 一根常开的 stdin 管道:壳被强杀时写端关闭,sidecar 读到 EOF 自己退;
//  3. 端口由壳挑、经 GAH_WEB_ADDR 交给 sidecar。
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
	stdin  *os.File
	exited atomic.Bool
	once   sync.Once
}

// startSidecar 拉起 sidecar 并接管它的输出。
func startSidecar(bin, addr, token string) (*sidecarProc, error) {
	cmd := exec.Command(bin, "--profile", "web")
	cmd.Env = append(os.Environ(),
		"GAH_WEB_OPEN=0",         // 壳自己开窗口
		"GAH_WEB_ADDR="+addr,     // 本次私有端口
		"GAH_WEB_PARENT_WATCH=1", // 父死子死
	)
	if cwd := defaultWorkspace(); cwd != "" {
		cmd.Dir = cwd
	}
	_ = token // 验证分支未挂控制面;token 只在探活/停机时用

	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("建 stdin 管道失败: %w", err)
	}
	cmd.Stdin = inR

	pr, pw, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return nil, fmt.Errorf("建 stdout 管道失败: %w", err)
	}
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		for _, f := range []*os.File{inR, inW, pr, pw} {
			_ = f.Close()
		}
		return nil, fmt.Errorf("启动 sidecar 失败: %w", err)
	}
	_ = inR.Close()
	_ = pw.Close()

	sc := &sidecarProc{cmd: cmd, bin: bin, stdin: inW}
	go sc.pump(pr)
	// 必须 Wait:否则 sidecar 退成僵尸、壳里那份 ProcessState 永远拿不到。
	go func() { _ = sc.wait() }()
	return sc, nil
}

// pump 把 sidecar 输出按行搬进壳日志(滤掉 go-plugin 的 DEBUG 噪音)。
func (s *sidecarProc) pump(r io.ReadCloser) {
	defer func() { _ = r.Close() }()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.Contains(line, "[DEBUG]") || line == "" || !looksLikeLogLine(line) {
			continue
		}
		shellLogf("sidecar: %s", line)
	}
}

// looksLikeLogLine 粗判一行是不是「给人读的日志」。
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
	return bad*20 < len(s)
}

// stop 优雅停 sidecar:POST /api/shutdown → 等端口释放 → 超时强杀。幂等。
func (s *shell) stopSidecar() {
	if s == nil || s.sc == nil {
		return
	}
	s.sc.once.Do(func() {
		p := s.sc
		if p.cmd.Process == nil || p.exited.Load() {
			return
		}
		shellLogf("退出链: POST /api/shutdown")
		if err := postShutdown(s.addr, os.Getenv("GAH_WEB_TOKEN")); err != nil {
			shellLogf("退出链: shutdown 失败(%v),继续等端口释放", err)
		}
		if waitPortReleased(s.addr, 5*time.Second) {
			shellLogf("退出链: 端口已释放")
		} else {
			shellLogf("退出链: 5s 内端口未释放,强杀 sidecar")
			_ = p.cmd.Process.Kill()
		}
		_ = p.stdin.Close()
		p.exited.Store(true)
	})
}

// wait 等进程退出(阻塞)。
func (s *sidecarProc) wait() error {
	err := s.cmd.Wait()
	s.exited.Store(true)
	return err
}

// triple 当前平台产物名后缀(沿用 gen-desktop 的命名)。
func triple() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	if runtime.GOOS == "windows" {
		return arch + "-pc-windows-msvc"
	}
	return arch + "-apple-darwin"
}
