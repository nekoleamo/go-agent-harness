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
//
// ctlAddr/ctlToken 是壳自建控制面的入口(经 env 交给 sidecar,页面里的 /shell/* 由
// gah 的同源代理转发)。令牌走 env 而不是 URL —— 跑在页面里的 UI 插件 JS 拿不到它。
func startSidecar(bin, addr, token, ctlAddr, ctlToken string) (*sidecarProc, error) {
	cmd := exec.Command(bin, "--profile", "web")
	cmd.Env = append(os.Environ(),
		"GAH_WEB_OPEN=0",         // 壳自己开窗口
		"GAH_WEB_ADDR="+addr,     // 本次私有端口
		"GAH_WEB_PARENT_WATCH=1", // 父死子死
		// 控制面同源入口:页面里的 /shell/* 由 gah 转发到壳(见 web/shellbridge.go)。
		"GAH_SHELL_CTL_ADDR="+ctlAddr,
		"GAH_SHELL_CTL_TOKEN="+ctlToken,
	)
	if cwd := defaultWorkspace(); cwd != "" {
		cmd.Dir = cwd
	}

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

	// Windows:别让 sidecar 弹出控制台黑框。
	hideConsole(cmd)

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

// stop 优雅停 sidecar:先 POST /api/shutdown,等端口释放,超时强杀。幂等(once)。
//
// 为什么不是 Kill:sidecar 要回收外部插件进程、落盘会话/计划状态;强杀会留下孤儿插件
// 与半写的状态文件(旧壳真机踩过「升级后界面还是旧的」)。
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

// wait 等进程退出(阻塞)。
func (s *sidecarProc) wait() error {
	err := s.cmd.Wait()
	s.exited.Store(true)
	return err
}
