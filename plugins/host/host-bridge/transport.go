// transport.go:外部插件的进程传输层(stdio + net/rpc),替代 hashicorp/go-plugin。
//
// 为什么自建(SZ-1 体积债,2026-09-18):协议本来就走 go-plugin 的 **net/rpc 模式**
// (见 proto.go),但 go-plugin 把 gRPC/protobuf/yamux/hclog 整栈静态拉进**宿主与每个
// 外部插件**。实测同一批工具包:带桥 13.39 MiB / gz 5.04,不带桥 4.11 MiB / gz 1.75
// —— 每插件 9.28 MiB 全是死重量(4 个插件 ≈37 MiB 未压缩 + 宿主一份)。
// 传输层语义与旧协议逐条对齐,只是自己实现:
//   - 握手:宿主注入环境变量 GAH_PLUGIN=gah-external-tool(缺失 → 插件拒绝启动);
//     插件启动后**先向 stdout 写一行** `GAH-PLUGIN|<ver>|stdio`,随后 stdin/stdout 即
//     gob-RPC 双向流(net/rpc 默认 gob 编码)。服务名固定 "Plugin"(宿主调用
//     "Plugin.ExecuteNamed" 等,与旧协议逐字一致,方法面不变)。
//   - 崩溃隔离:插件仍是独立进程;组杀(proc_unix/proc_other)与 stderr 归因链路不变。
//
// 纪律:
//   - **插件除握手行外不得向 stdout 写任何东西**(gob 流会被污染)——与 go-plugin 时代
//     同一约束;插件日志一律走 stderr(bridge.pluginStderr 会带进错误上报)。
//   - 旧版(go-plugin 时代)插件二进制无法握手:报**可操作的显式错误**(见 classifyHandshake),
//     不静默降级。随包产物由 internal/embed 按 sha256 覆盖升级,不会出现新旧混用。
package hostbridge

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net/rpc"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 握手与协议常量(宿主与外部插件共用的单一事实源;两侧漂移 ⇒ 全部插件加载失败)。
const (
	// HandshakeKey/HandshakeValue 插件启动自检的环境键值(宿主注入)。
	HandshakeKey   = "GAH_PLUGIN"
	HandshakeValue = "gah-external-tool"

	// protoLinePrefix 握手行前缀:与 go-plugin 时代的 `<core>|<app>|tcp|…` 明确区分,
	// 使「旧版插件产物」可被判为具体原因而不是笼统的「握手失败」。
	protoLinePrefix = "GAH-PLUGIN|"

	// protoVersion 传输层协议版本。传输层变更(v1 = go-plugin/yamux/TLS → v2 = stdio)
	// 必须抬版本;RPC 方法面同代不变,插件只需重新编译(见 docs/PLUGIN_DEV.md)。
	protoVersion = 2

	// rpcServiceName net/rpc 服务名:宿主调用 "Plugin.Definitions"/"Plugin.ExecuteNamed"…
	// 必须与旧协议逐字一致(名字变了 = 方法面全错)。
	rpcServiceName = "Plugin"

	// handshakeTimeout 握手行等待上限(插件启动即写;超时 = 疑似卡在自身初始化)。
	handshakeTimeout = 30 * time.Second
	// stderrDrainTimeout 握手失败后等 stderr 转写落地的上限:cmd.Stderr 是 io.Writer 时
	// exec 内部起 copier goroutine,Wait 才等它结束;握手失败常在它收尾之前发生。
	stderrDrainTimeout = 300 * time.Millisecond
)

// handshakeLine 本版本插件应写出的握手行(含换行)。
func handshakeLine() string {
	return fmt.Sprintf("%s%d|stdio\n", protoLinePrefix, protoVersion)
}

// goPluginHandshakeRe 旧版(go-plugin)握手行形态:`<core>|<app>|tcp|127.0.0.1:port|…`。
var goPluginHandshakeRe = regexp.MustCompile(`^\d+\|\d+\|(tcp|unix)\|`)

// stdioConn 把「读端(插件 stdout)/ 写端(插件 stdin)」装成 net/rpc 需要的双向流。
// 读端必须是**握手行读过的同一个 bufio.Reader**:握手行之后缓冲里可能已躺着首个 gob
// 帧,换新 Reader 会把它吃掉(表现为「握手成功但首个 RPC 永远不返回」)。
type stdioConn struct {
	r      *bufio.Reader
	w      io.Writer
	closer []io.Closer
}

func (c *stdioConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return c.w.Write(p) }

func (c *stdioConn) Close() error {
	var first error
	for _, cl := range c.closer {
		if err := cl.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// ServeRPC 外部插件进程的服务端入口:写握手行 → 用 stdin/stdout 跑 gob-RPC。
// 阻塞直至宿主关闭连接(宿主退出 → stdin EOF → 返回),随后正常退出。
// 握手标识缺失即拒绝启动(防误跑:直接执行产物不会假装加载成功)。
func ServeRPC(svc any) {
	if v, ok := os.LookupEnv(HandshakeKey); !ok || v != HandshakeValue {
		log.Fatalf("外部插件缺少握手标识 %s(%s)", HandshakeKey, HandshakeValue)
	}
	srv := rpc.NewServer()
	if err := srv.RegisterName(rpcServiceName, svc); err != nil {
		log.Fatalf("外部插件注册 RPC 服务失败: %v", err)
	}
	// os.Stdout 无缓冲:写出的握手行立刻可见(宿主侧 ReadString 不会卡)。
	if _, err := os.Stdout.WriteString(handshakeLine()); err != nil {
		log.Fatalf("外部插件写握手行失败: %v", err)
	}
	srv.ServeConn(&stdioConn{r: bufio.NewReader(os.Stdin), w: os.Stdout})
	os.Exit(0) // 宿主断开 = 进程使命结束(go-plugin 同语义)
}

// startPluginRPC 启动插件进程并完成握手,返回 gob-RPC 客户端(宿主侧入口)。
// env 追加握手键(go-plugin 时代由 magic cookie 机制注入,现在由传输层自己负责)。
// stderr 由调用方接环形缓冲(插件故障归因的唯一现场)。
func startPluginRPC(cmd *exec.Cmd, stderr io.Writer) (*rpc.Client, error) {
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, HandshakeKey+"="+HandshakeValue)
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe() // 宿主写 → 插件读
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe() // 宿主读 ← 插件写(握手行 + gob 帧)
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	// 回收进程(防僵尸):宿主机是长命进程,插件退出后必须回收,否则 fs/proc 里不断
	// 累积 defunct。Wait 在进程退出后关闭管道——读取侧本来就会拿到 EOF/ErrClosed,
	// 对 rpc 客户端而言同样是「连接断开」。
	// waitDone 另外承担一个职责:cmd.Stderr 是 io.Writer 时 exec 内部起 copier goroutine,
	// 而 Wait 会等它结束 —— 下面 fail() 靠它等 stderr 落地。
	waitDone := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waitDone) }()
	// 握手不成功时插件进程可能还活着(卡在自身初始化/根本不说协议)——必须杀掉,
	// 否则失败重试会不断遗下孤儿进程(旧版由 go-plugin client.Kill() 兼顾)。
	fail := func(err error) (*rpc.Client, error) {
		killPluginGroup(cmd)
		// 等 stderr 转写落地再返回:握手失败常在 copier 收尾**之前**发生,此时插件写的
		// 原因(缺配置/端口占用/权限 —— 桌面版没有终端,这是唯一现场)尚未进环形缓冲,
		// 直接读会拿到空内容(2026-09-25 CI 实测偶发丢)。进程已被杀,Wait 很快返回;
		// 超时兵底防止极端情况下卡住。
		select {
		case <-waitDone:
		case <-time.After(stderrDrainTimeout):
		}
		return nil, err
	}
	br := bufio.NewReader(stdout)
	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		line, rerr := br.ReadString('\n')
		if rerr != nil {
			errCh <- rerr
			return
		}
		lineCh <- line
	}()
	select {
	case line := <-lineCh:
		if err := classifyHandshake(line); err != nil {
			return fail(err)
		}
	case err := <-errCh:
		// 插件未写握手行就退出/关闭 stdout:交给调用方拼 stderr 尾部
		return fail(fmt.Errorf("外部插件未发出握手行(%v)——插件可能在初始化阶段就退出", err))
	case <-time.After(handshakeTimeout):
		return fail(fmt.Errorf("外部插件握手超时(>%s)", handshakeTimeout))
	}
	return rpc.NewClient(&stdioConn{r: br, w: stdin, closer: []io.Closer{stdout, stdin}}), nil
}

// classifyHandshake 校验握手行;不匹配时给**可操作的**错误(而不是交给上层猜)。
func classifyHandshake(line string) error {
	line = strings.TrimRight(line, "\r\n")
	if strings.HasPrefix(line, protoLinePrefix) {
		rest := strings.TrimPrefix(line, protoLinePrefix)
		ver := rest
		if i := strings.Index(rest, "|"); i >= 0 {
			ver = rest[:i]
		}
		if n, err := strconv.Atoi(ver); err != nil || n != protoVersion {
			return fmt.Errorf("外部插件协议版本不匹配:插件=%s 宿主=%d;请删除该插件目录让 gah 重新释放随包产物(或按 docs/PLUGIN_DEV.md 重新编译)", ver, protoVersion)
		}
		return nil
	}
	if goPluginHandshakeRe.MatchString(line) {
		return errors.New("外部插件是旧版(go-plugin 传输)产物,与当前 stdio 协议不兼容;请删除该插件目录让 gah 重新释放随包产物,或按 docs/PLUGIN_DEV.md 重新编译")
	}
	if len(line) > 120 {
		line = line[:120] + "…"
	}
	return fmt.Errorf("外部插件握手行无法识别(%q);期望 %s", line, strings.TrimRight(handshakeLine(), "\n"))
}
