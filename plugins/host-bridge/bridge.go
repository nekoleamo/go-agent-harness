// 宿主侧桥:扫描外部插件目录,加载 tool-* 二进制,注册为 sdk.Tool。
// P0:工具级超时(def.TimeoutMs 覆写全局 3s)+ 进程崩溃自动拉起(连接错误
// → 节流重建进程,下次调用走新实例)。
package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/rpc"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-plugin"

	coreplugin "github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-bridge。requires ctx.tools;data.dir 指定外部插件目录。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-bridge" }

// Start 扫描目录并注册外部工具。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var dir string
	if m != nil && m.Data != nil {
		if d, ok := m.Data["dir"].(string); ok && d != "" {
			dir = d
		}
	}
	if dir == "" {
		dir = "extplugins"
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	b := &Bridge{dir: dir, tools: tools, entries: map[string]*extEntry{}}
	if err := b.loadEntries(); err != nil {
		return nil, err
	}
	var watchClose func()
	if m != nil && m.Data != nil {
		if w, ok := m.Data["watch"].(bool); ok && w {
			_, closeFn, werr := coreplugin.NewWatcher(dir, 300*time.Millisecond, func(path string) {
				b.reload(path)
			})
			if werr != nil {
				b.closeAll()
				return nil, fmt.Errorf("host-bridge: 监听 %s: %w", dir, werr)
			}
			watchClose = closeFn
		}
	}
	return func() {
		if watchClose != nil {
			watchClose()
		}
		b.closeAll()
	}, nil
}

// extEntry 一个外部插件条目。
type extEntry struct {
	tool      sdk.Tool
	unreg     sdk.Disposer
	kill      sdk.Disposer
	def       sdk.ToolDefinition
	client    *rpc.Client
	respawnAt time.Time // 崩溃重拉节流(60s 内不重复)
}

// Bridge 外部插件目录管理(扫描/重载/关闭/崩溃拉起)。
type Bridge struct {
	dir     string
	tools   sdk.ToolRegistry
	mu      sync.RWMutex
	entries map[string]*extEntry // bin 绝对路径 → 条目
}

// loadEntries 扫描目录并加载 tool-* 二进制。
func (b *Bridge) loadEntries() error {
	return filepath.WalkDir(b.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.HasPrefix(d.Name(), "tool-") {
			return nil
		}
		e, lerr := b.loadOne(path)
		if lerr != nil {
			return fmt.Errorf("host-bridge: 加载 %s: %w", path, lerr)
		}
		unreg := b.tools.Register(e.tool) // 锁外注册(tc 自带 def 快照)
		b.mu.Lock()
		e.unreg = unreg
		b.entries[path] = e
		b.mu.Unlock()
		return nil
	})
}

// loadOne 启动外部插件进程并组装条目(定义缓存 + client)。
func (b *Bridge) loadOne(path string) (*extEntry, error) {
	cl, killFn, err := startPlugin(path)
	if err != nil {
		return nil, err
	}
	// 定义缓存(一次 RPC;失败不阻塞注册,返回空定义)
	def := fetchDef(cl)
	t := &toolRPCClient{br: b, path: path, def: def} // def 快照随实例,Register 无需查 map
	e := &extEntry{tool: t, def: def, client: cl, kill: killFn, unreg: func() {}}
	return e, nil
}

// fetchDef 经 RPC 取工具定义(JSON 解包)。
func fetchDef(cl *rpc.Client) sdk.ToolDefinition {
	var raw string
	if err := cl.Call("Plugin.Definition", struct{}{}, &raw); err != nil {
		return sdk.ToolDefinition{}
	}
	var def sdk.ToolDefinition
	_ = json.Unmarshal([]byte(raw), &def)
	return def
}

// reload 二进制变更:dispose 旧进程并加载新实例(热重载接线)。
func (b *Bridge) reload(path string) {
	b.mu.Lock()
	entry, ok := b.entries[path]
	b.mu.Unlock()
	if !ok {
		// 新出现的二进制:直接加载
		if strings.HasPrefix(filepath.Base(path), "tool-") {
			e, err := b.loadOne(path)
			if err == nil {
				unreg := b.tools.Register(e.tool)
				b.mu.Lock()
				e.unreg = unreg
				b.entries[path] = e
				b.mu.Unlock()
			}
		}
		return
	}
	// 旧实例撤销:注销工具 + kill 进程
	b.mu.Lock()
	entry.unreg()
	entry.kill()
	b.mu.Unlock()
	e, err := b.loadOne(path)
	if err != nil {
		b.mu.Lock()
		delete(b.entries, path)
		b.mu.Unlock()
		return // 新二进制不可用:工具消失(下次变更再试)
	}
	unreg := b.tools.Register(e.tool)
	b.mu.Lock()
	e.unreg = unreg
	b.entries[path] = e
	b.mu.Unlock()
}

// closeAll 关闭全部插件条目(逆序)。
func (b *Bridge) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for p, e := range b.entries {
		e.unreg()
		e.kill()
		delete(b.entries, p)
	}
}

// clientFor 取当前活动 client(未加载/重建中返回 nil)。
func (b *Bridge) clientFor(path string) *rpc.Client {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if e, ok := b.entries[path]; ok {
		return e.client
	}
	return nil
}

// defFor 取工具定义(注册时缓存,免每次 RPC)。
func (b *Bridge) defFor(path string) sdk.ToolDefinition {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if e, ok := b.entries[path]; ok {
		return e.def
	}
	return sdk.ToolDefinition{}
}

// onDead 连接错误 → 标记并异步重建进程(60s 节流,防崩溃循环)。
func (b *Bridge) onDead(path string) {
	b.mu.Lock()
	e, ok := b.entries[path]
	if !ok || !time.Now().After(e.respawnAt) {
		b.mu.Unlock()
		return
	}
	e.respawnAt = time.Now().Add(60 * time.Second)
	b.mu.Unlock()
	go b.respawn(path)
}

// respawn 重建进程并替换条目(先注销旧工具腾名,再注册新工具,最后替换 map)。
func (b *Bridge) respawn(path string) {
	e, err := b.loadOne(path)
	if err != nil {
		return // 重建失败:节流期内不再反复尝试(下次连接错误再触发)
	}
	b.mu.Lock()
	if old, ok := b.entries[path]; ok {
		old.unreg() // 先注销旧工具(重名注册会非静默忽略)
	}
	b.mu.Unlock()
	unreg := b.tools.Register(e.tool)
	b.mu.Lock()
	if old, ok := b.entries[path]; ok {
		old.kill()
	}
	e.unreg = unreg
	b.entries[path] = e
	b.mu.Unlock()
}

// startPlugin 启动外部插件进程,返回 rpc client 与 kill 函数(崩溃隔离:死进程快速失败)。
func startPlugin(bin string) (*rpc.Client, func(), error) {
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshake,
		Plugins: map[string]plugin.Plugin{
			pluginName: &toolPluginBridge{},
		},
		Cmd: exec.Command(bin),
	})
	proto, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, err
	}
	raw, err := proto.Dispense(pluginName)
	if err != nil {
		client.Kill()
		return nil, nil, err
	}
	tc, ok := raw.(*rpcClientOnly)
	if !ok {
		client.Kill()
		return nil, nil, fmt.Errorf("host-bridge: 意外的插件类型 %T", raw)
	}
	return tc.client, func() {
		proto.Close()
		client.Kill()
	}, nil
}

// rpcTimeout 默认外部 RPC 调用超时(崩溃隔离:死进程快速失败而非死等)。
const rpcTimeout = 3 * time.Second

// rpcTimeoutFor 工具级超时覆写(P0-2):定义声明 timeout_ms 则用之,否则全局默认。
func rpcTimeoutFor(def sdk.ToolDefinition) time.Duration {
	if def.TimeoutMs > 0 {
		return time.Duration(def.TimeoutMs) * time.Millisecond
	}
	return rpcTimeout
}

// respawnCooldown 崩溃自动拉起的节流窗口。
const respawnCooldown = 60 * time.Second

// toolPluginBridge 桥插件:连接 net/rpc,Client() 返回 gob 转发客户端。
type toolPluginBridge struct{}

func (p *toolPluginBridge) Server(*plugin.MuxBroker) (any, error) {
	return nil, fmt.Errorf("server 侧由外部插件提供")
}
func (p *toolPluginBridge) Client(b *plugin.MuxBroker, c *rpc.Client) (any, error) {
	return &rpcClientOnly{client: c}, nil
}

// rpcClientOnly 占位(go-plugin Client() 钩子:宿主侧取回 *rpc.Client)。
type rpcClientOnly struct{ client *rpc.Client }

// toolRPCClient 实现 sdk.Tool(经 RPC 转发;连接错误触发自动拉起)。def 为注册时快照。
type toolRPCClient struct {
	br   *Bridge
	path string
	def  sdk.ToolDefinition
}

func (t *toolRPCClient) Definition() sdk.ToolDefinition {
	if t.def.Name != "" {
		return t.def
	}
	return t.br.defFor(t.path) // 兜底(理论不触发)
}

func (t *toolRPCClient) Execute(ctx context.Context, args string) (any, error) {
	cl := t.br.clientFor(t.path)
	if cl == nil {
		return map[string]any{"error": "外部插件重建中(崩溃自动拉起)"}, nil
	}
	timeout := rpcTimeoutFor(t.def)
	type rpcOut struct {
		reply ExecReply
		err   error
	}
	ch := make(chan rpcOut, 1)
	go func() {
		var reply ExecReply
		err := cl.Call("Plugin.Execute", &ExecArgs{JSONArgs: args}, &reply)
		ch <- rpcOut{reply, err}
	}()
	var out rpcOut
	select {
	case out = <-ch:
	case <-ctx.Done():
		return map[string]any{"error": "外部插件调用取消 " + ctx.Err().Error()}, nil
	case <-time.After(timeout):
		return map[string]any{"error": "外部插件不可达(超时)"}, nil
	}
	if isConnErr(out.err) {
		t.br.onDead(t.path) // 进程死亡:异步重建,下次调用走新实例
		return map[string]any{"error": "外部插件不可达(进程崩溃,自动重建中): " + out.err.Error()}, nil
	}
	if out.err != nil {
		return map[string]any{"error": "外部插件不可达: " + out.err.Error()}, nil
	}
	if out.reply.Error != "" {
		return map[string]any{"error": out.reply.Error}, nil
	}
	var val any
	if err := json.Unmarshal([]byte(out.reply.Content), &val); err == nil {
		return val, nil
	}
	return out.reply.Content, nil
}

// isConnErr 判定连接类错误(进程死亡/连接关闭/EOF),非插件业务错误。
func isConnErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, rpc.ErrShutdown) || errors.Is(err, io.EOF) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection") || strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "shut down")
}
