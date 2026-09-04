// Package hostbridge 提供 host-bridge 插件:外部进程插件桥(宿主侧,崩溃隔离)。
// 扫描外部插件目录,加载 tool-* 二进制(go-plugin/gRPC),注册为 sdk.Tool;
// P0:工具级超时(TimeOutMs 覆写全局 3s)+ 进程崩溃自动拉起(连接错误
// → 节流重建进程,下次调用走新实例);多工具协议(ExecuteNamed/Definitions,
// 旧单工具协议自动回退)。
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
		dir = filepath.Join(pluginHome(), "plugins") // P1:默认 home/plugins(方案B 释放目录)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	// 宿主回调通道(M6.8 外部化):外部进程经 GAH_CB_ADDR 请求宿主服务
	// (tools/jobs/fanout;jobs/fanout 未装配时对应回调返回显式错误)
	var jobs sdk.JobService
	var fanout sdk.FanoutService
	_ = c.Inject("ctx.jobs", &jobs)
	_ = c.Inject("ctx.fanout", &fanout)
	cbAddr, cbClose, err := serveCallback(NewCallback(tools, jobs, fanout))
	if err != nil {
		return nil, err
	}
	b := &Bridge{dir: dir, tools: tools, entries: map[string]*extEntry{}, cbAddr: cbAddr}
	if err := b.loadEntries(); err != nil {
		cbClose()
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
		cbClose()
	}, nil
}

// extEntry 一个外部插件进程条目(可承载多工具)。
type extEntry struct {
	tools     map[string]*toolRPCClient
	unreg     sdk.Disposer // 聚合注销(全部工具)
	kill      sdk.Disposer
	client    *rpc.Client
	proto     int // 0 未知 / 1 旧单工具协议 / 2 新多工具协议
	respawnAt time.Time
}

// Bridge 外部插件目录管理(扫描/重载/关闭/崩溃拉起)。
type Bridge struct {
	dir     string
	tools   sdk.ToolRegistry
	cbAddr  string // 宿主回调通道地址(GAH_CB_ADDR 注入外部进程)
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
		unreg := b.registerAll(e)
		b.mu.Lock()
		e.unreg = unreg
		b.entries[path] = e
		b.mu.Unlock()
		return nil
	})
}

// loadOne 启动外部插件进程并组装条目(定义枚举 + 协议探测)。
func (b *Bridge) loadOne(path string) (*extEntry, error) {
	cl, killFn, err := startPlugin(path, b.cbAddr)
	if err != nil {
		return nil, err
	}
	e := &extEntry{client: cl, kill: killFn, unreg: func() {}, tools: map[string]*toolRPCClient{}}
	// 协议探测:新协议(Definitions)优先,旧单工具协议回退
	raw := ""
	if cerr := cl.Call("Plugin.Definitions", struct{}{}, &raw); cerr == nil && raw != "" {
		var multi []defDTO
		if json.Unmarshal([]byte(raw), &multi) == nil && len(multi) > 0 {
			e.proto = 2
			for _, d := range multi {
				def := sdk.ToolDefinition{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema, TimeoutMs: d.TimeoutMs}
				e.tools[d.Name] = &toolRPCClient{br: b, path: path, name: d.Name, def: def}
			}
			return e, nil
		}
	}
	// 旧协议:单工具
	var defJSON string
	if cerr := cl.Call("Plugin.Definition", struct{}{}, &defJSON); cerr == nil {
		var def sdk.ToolDefinition
		if json.Unmarshal([]byte(defJSON), &def) == nil && def.Name != "" {
			e.proto = 1
			e.tools[def.Name] = &toolRPCClient{br: b, path: path, name: def.Name, def: def}
			return e, nil
		}
	}
	killFn()
	return nil, fmt.Errorf("host-bridge: 插件未按桥协议暴露工具 %s", path)
}

// registerAll 注册全部工具并返回聚合注销。
func (b *Bridge) registerAll(e *extEntry) sdk.Disposer {
	disposers := make([]sdk.Disposer, 0, len(e.tools))
	for _, t := range e.tools {
		disposers = append(disposers, b.tools.Register(t))
	}
	return func() {
		for _, d := range disposers {
			d()
		}
	}
}

// reload 二进制变更:dispose 旧进程并加载新实例(热重载接线)。
func (b *Bridge) reload(path string) {
	b.mu.Lock()
	entry, ok := b.entries[path]
	b.mu.Unlock()
	if !ok {
		if strings.HasPrefix(filepath.Base(path), "tool-") {
			e, err := b.loadOne(path)
			if err == nil {
				unreg := b.registerAll(e)
				b.mu.Lock()
				e.unreg = unreg
				b.entries[path] = e
				b.mu.Unlock()
			}
		}
		return
	}
	b.mu.Lock()
	entry.unreg()
	entry.kill()
	b.mu.Unlock()
	e, err := b.loadOne(path)
	if err != nil {
		b.mu.Lock()
		delete(b.entries, path)
		b.mu.Unlock()
		return
	}
	unreg := b.registerAll(e)
	b.mu.Lock()
	e.unreg = unreg
	b.entries[path] = e
	b.mu.Unlock()
}

// closeAll 关闭全部插件条目。
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
		return
	}
	b.mu.Lock()
	if old, ok := b.entries[path]; ok {
		old.unreg()
	}
	b.mu.Unlock()
	unreg := b.registerAll(e)
	b.mu.Lock()
	if old, ok := b.entries[path]; ok {
		old.kill()
	}
	e.unreg = unreg
	b.entries[path] = e
	b.mu.Unlock()
}

// startPlugin 启动外部插件进程,返回 rpc client 与 kill 函数(崩溃隔离:死进程快速失败)。
// 回调通道:宿主地址经 GAH_CB_ADDR 环境变量注入(外部进程 Dial 后请求宿主服务)。
func startPlugin(bin string, cbAddr string) (*rpc.Client, func(), error) {
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "GAH_CB_ADDR="+cbAddr)
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshake,
		Plugins: map[string]plugin.Plugin{
			pluginName: &toolPluginBridge{},
		},
		Cmd: cmd,
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

// defDTO 多工具协议的定义载荷。
type defDTO struct {
	Name        string         `json:"Name"`
	Description string         `json:"Description"`
	InputSchema map[string]any `json:"InputSchema"`
	TimeoutMs   int64          `json:"TimeoutMs"`
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
	name string
	def  sdk.ToolDefinition
}

func (t *toolRPCClient) Definition() sdk.ToolDefinition {
	return t.def
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
	call := func(reply *ExecReply) error {
		err := cl.Call("Plugin.ExecuteNamed", &ExecNamedArgs{Name: t.name, JSONArgs: args}, reply)
		if err != nil && isMethodMissing(err) {
			// 旧单工具协议回退
			return cl.Call("Plugin.Execute", &ExecArgs{JSONArgs: args}, reply)
		}
		return err
	}
	ch := make(chan rpcOut, 1)
	go func() {
		var reply ExecReply
		err := call(&reply)
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
		t.br.onDead(t.path)
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

// isMethodMissing 协议方法不存在(旧插件回退信号)。
func isMethodMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "can't find method")
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

// pluginHome 运行时 home(GAH_HOME 覆盖;默认 ~/.gah,与 boot 一致)。
func pluginHome() string {
	if h := os.Getenv("GAH_HOME"); h != "" {
		return h
	}
	if uh, err := os.UserHomeDir(); err == nil {
		return filepath.Join(uh, ".gah")
	}
	return os.TempDir()
}
