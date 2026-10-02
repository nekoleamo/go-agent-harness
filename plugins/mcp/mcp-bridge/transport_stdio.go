// transport_stdio.go:stdio 传输(NOND-M1 起的现状形态,代码自 mcp.go 原样搬移)。
//
// **逐行 JSON-RPC**:请求写一行到子进程 stdin,响应从 stdout 逐行读、按 id 匹配。
// 搬移的理由:让「传输」变成可替换的一项,而不是两套并行实现;语义**零变化**
// (包括单行上限、读错误、中途取消后按 id 跳过)。
package mcpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/nekoleamo/go-agent-harness/internal/kernelsandbox"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// linesCap 读线程投递缓冲(取消后无人消费时读线程最多阻塞在写入上,不无限占用内存)。
const linesCap = 256

// maxMCPLine 单行输出上限(安全审计 C5,2026-09-27)。外部 MCP server 的 stdout 是**不可信输入**:
// 旧实现用 bufio.ReadBytes('\n') 逐行读 —— 没有换行符就是无限长,一个卡住(或恶意)的 server
// 能让宿主一路分配到 OOM。同类闸在别处都有(cappedBuffer 1 MiB / web maxBody 1 MiB),此处原本缺。
const maxMCPLine = 8 << 20

// stdioTransport 本地子进程 + 管道。
type stdioTransport struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader
	lines chan []byte
	// readErr 读线程侧的致命错误(单行超上限);由 RoundTrip 在通道关闭后读出。
	readErr error
	// mu 整段串行:stdio 的「写请求 + 等响应」必须串行(响应是**流**上的,没有 id 之外
	// 的配对信息;并发写会让两行的响应交错)。
	mu     sync.Mutex
	closed bool
}

func (s *stdioTransport) Kind() string { return "stdio" }

// Redacted 可安全进日志的描述:argv 不含凭据(mcp-bridge 对 server 进程做凭据隔离),
// 但仍不打印完整 argv —— 参数里可能有内网地址/项目名。
func (s *stdioTransport) Redacted() string {
	return fmt.Sprintf("stdio %s(%d 参数)", s.cmd.Path, len(s.cmd.Args)-1)
}

// spawnStdio 起一个 MCP server 子进程并接上管道(内核沙箱 + 凭据隔离同原实现)。
func spawnStdio(command string, args []string) (*stdioTransport, error) {
	argv, wrapped := mcpArgv(command, args)
	cmd := exec.Command(argv[0], argv[1:]...)
	// 凭据隔离:第三方 MCP server 不继承宿主凭据(滤除 *_API_KEY/*_TOKEN/AWS_* 等),
	// 也不继承 GAH_CB_*(宿主回调地址/token)。需要额外 env 的 server 请经启动命令显式配置。
	cmd.Env = sdk.SanitizedEnv(os.Environ())
	if wrapped {
		// 标记已在内核沙箱内:server 再起的子进程(包装脚本调子命令)不必也**不能**重复施加
		// (seatbelt/Landlock 不可嵌套)。
		cmd.Env = append(cmd.Env, kernelsandbox.MarkerEnv+"=1")
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := &stdioTransport{cmd: cmd, stdin: stdin, out: bufio.NewReader(stdout), lines: make(chan []byte, linesCap)}
	s.startReader()
	return s, nil
}

// startReader 后台逐行读 stdout:read 侧永不在持锁路径阻塞;单行超上限即止并留错误(显式失败,不 OOM)。
func (s *stdioTransport) startReader() {
	go func() {
		sc := bufio.NewScanner(s.out)
		sc.Buffer(make([]byte, 0, 64<<10), maxMCPLine)
		for sc.Scan() {
			// Bytes() 缓冲会被复用:必须先拷贝再投递
			s.lines <- append([]byte(nil), sc.Bytes()...)
		}
		if err := sc.Err(); err != nil {
			s.readErr = fmt.Errorf("mcp server 单行输出超上限 %d 字节: %w", maxMCPLine, err)
		}
		close(s.lines)
	}()
}

// waitTransportProcess 等子进程退出(阻塞);holder 的崩溃看护靠它感知「进程没了」。
// 非 stdio 传输直接返回(没有进程)。
func waitTransportProcess(tr transport) {
	if s, ok := tr.(*stdioTransport); ok {
		_ = s.cmd.Wait()
	}
}

// newStdioClient 起一个 stdio 传输并接成 mcpClient。
func newStdioClient(command string, args []string) (*mcpClient, error) {
	s, err := spawnStdio(command, args)
	if err != nil {
		return nil, err
	}
	return &mcpClient{tr: s}, nil
}

// notify 直接写通知行(mcpClient.Notify 走 transport 接口)。
func (s *stdioTransport) Notify(ctx context.Context, method string, params any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(rpcReq{JSONRPC: "2.0", Method: method, Params: params})
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

// RoundTrip 发请求并等对应 id 的响应(逐行读;id 不匹配跳过)。
func (s *stdioTransport) RoundTrip(ctx context.Context, req rpcReq) (rpcResp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(req)
	if err != nil {
		return rpcResp{}, err
	}
	if _, err := s.stdin.Write(append(b, '\n')); err != nil {
		return rpcResp{}, err
	}
	for {
		var line []byte
		select {
		case <-ctx.Done():
			// 中途取消:未读响应由后续调用按 id 跳过(JSON-RPC 有 id,不会错配)。
			return rpcResp{}, ctx.Err()
		case l, ok := <-s.lines:
			if !ok {
				if s.readErr != nil {
					return rpcResp{}, s.readErr
				}
				return rpcResp{}, io.EOF
			}
			line = l
		}
		var resp rpcResp
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		if resp.ID != req.ID {
			continue
		}
		return resp, nil
	}
}

// Close 收尾:先关 stdin(让 server 看到 EOF 正常退出),再杀进程并回收。
func (s *stdioTransport) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
	return nil
}

// closeKill 只杀进程不 Wait(holder.supervise 独占回收)。
func (s *stdioTransport) closeKill() {
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}
