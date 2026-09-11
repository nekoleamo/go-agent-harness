// Package hostbridge 提供 host-bridge 插件:外部进程插件桥(宿主侧,崩溃隔离)。
// 扫描外部插件目录,加载 tool-* 二进制(go-plugin/gRPC),注册为 sdk.Tool;
// P0:工具级超时(TimeOutMs 覆写全局 3s)+ 进程崩溃自动拉起(连接错误
// → 节流重建进程,下次调用走新实例);多工具协议(ExecuteNamed/Definitions,
// 旧单工具协议自动回退)。
package hostbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	// M14 外部命令桥:可选注入 ctx.commands(未装配/无 TUI 场景 = 外部命令不注册,
	// 工具不受影响;对齐 host-jobs 可选注入模式)
	var cmds sdk.CommandRegistry
	_ = c.Inject("ctx.commands", &cmds)
	// 宿主回调通道(M6.8 外部化):外部进程经 GAH_CB_ADDR 请求宿主服务
	// (tools/jobs/fanout;jobs/fanout 未装配时对应回调返回显式错误)
	var jobs sdk.JobService
	var fanout sdk.FanoutService
	_ = c.Inject("ctx.jobs", &jobs)
	_ = c.Inject("ctx.fanout", &fanout)
	cbToken := randomToken() // M7 鉴权:本进程随机 token,经 GAH_CB_TOKEN 注入外部进程
	cbAddr, cbClose, err := serveCallback(NewCallback(tools, jobs, fanout, cbToken))
	if err != nil {
		return nil, err
	}
	b := &Bridge{dir: dir, tools: tools, cmds: cmds, entries: map[string]*extEntry{}, cbAddr: cbAddr, cbToken: cbToken, lg: c.Logger()}
	if err := b.loadEntries(); err != nil {
		cbClose()
		return nil, err
	}
	// 工作区切换事件:宿主 cwd 已 Chdir,重启全部外部工具进程使其继承新 cwd
	// (外部进程启动时 cwd = 宿主当前 cwd,重启后 shell/file 工具在新目录执行)。
	wsSub := c.Subscribe("cwd/workspace-switched", func(ctx context.Context, _ *sdk.Event) error {
		b.reloadAll()
		return nil
	})
	var watchClose func()
	if m != nil && m.Data != nil {
		if w, ok := m.Data["watch"].(bool); ok && w {
			// 目录不存在 = 空插件集(loadEntries 已降级):不监听、不报错——
			// 热更新仅对已存在目录有意义(防止默认开 watch 后空目录拖垮 boot)
			if _, serr := os.Stat(dir); serr == nil {
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
	}
	return func() {
		if watchClose != nil {
			watchClose()
		}
		wsSub()
		b.closeAll()
		cbClose()
	}, nil
}

// extEntry 一个外部插件进程条目(可承载多工具 + 多命令)。
type extEntry struct {
	tools     map[string]*toolRPCClient
	commands  map[string]*commandRPCClient // M14 外部命令(可空)
	unreg     sdk.Disposer                 // 聚合注销(全部工具+命令)
	kill      sdk.Disposer
	client    *rpc.Client
	proto     int // 0 未知 / 1 旧单工具协议 / 2 新多工具协议
	respawnAt time.Time
}

// Bridge 外部插件目录管理(扫描/重载/关闭/崩溃拉起)。
type Bridge struct {
	dir     string
	tools   sdk.ToolRegistry
	cmds    sdk.CommandRegistry // M14 可选(ctx.commands;nil = 外部命令不注册)
	cbAddr  string              // 宿主回调通道地址(GAH_CB_ADDR 注入外部进程)
	cbToken string              // M7 鉴权 token(GAH_CB_TOKEN 注入外部进程,回传校验)
	lg      *slog.Logger        // P3 软降级日志(sdk.Ctx.Logger();nil 时兜底 slog.Default)
	mu      sync.RWMutex
	entries map[string]*extEntry // bin 绝对路径 → 条目
}

// logErr 记录外部插件加载失败(P3 软降级:不拖垮 boot,但信息不丢失)。
func (b *Bridge) logErr(msg string, kv ...any) {
	lg := b.lg
	if lg == nil {
		lg = slog.Default()
	}
	lg.Error(msg, kv...)
}

// loadEntries 扫描目录并加载 tool-* 二进制。P3 软降级:
// - 目录不存在 = 空插件集(未安装/已卸载),WARN 跳过,boot 继续;
// - 目录存在但不可读 = 装配层错误,显式失败;
// - 单个外部插件加载失败(缺配置/崩溃/不兼容)记 ERROR 跳过、继续装配;
// 未装上的工具对模型不可见,调用侧已有显式提示,不复拖垮整体 boot。
func (b *Bridge) loadEntries() error {
	if _, err := os.Stat(b.dir); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("host-bridge: 外部插件目录不可访问 %s: %w", b.dir, err)
		}
		b.logErr("host-bridge: 外部插件目录不存在(空插件集),跳过扫描", "dir", b.dir)
		return nil
	}
	return filepath.WalkDir(b.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			b.logErr("host-bridge: 扫描路径失败,跳过", "path", path, "err", err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !isExternalPluginBin(d.Name()) {
			return nil
		}
		e, lerr := b.loadOne(path)
		if lerr != nil {
			b.logErr("host-bridge: 跳过加载失败的外部插件", "path", path, "err", lerr)
			return nil
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
	cl, killFn, err := startPlugin(path, b.cbAddr, b.cbToken)
	if err != nil {
		return nil, err
	}
	e := &extEntry{client: cl, kill: killFn, unreg: func() {}, tools: map[string]*toolRPCClient{}}
	// 协议探测:新协议(Definitions)优先,旧单工具协议回退;
	// 纯命令插件(cmd-*,无工具)可经 Commands 单独满足加载条件
	raw := ""
	protoOK := false
	if cerr := cl.Call("Plugin.Definitions", struct{}{}, &raw); cerr == nil && raw != "" {
		var multi []defDTO
		if json.Unmarshal([]byte(raw), &multi) == nil && len(multi) > 0 {
			e.proto = 2
			protoOK = true
			for _, d := range multi {
				def := sdk.ToolDefinition{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema, TimeoutMs: d.TimeoutMs}
				e.tools[d.Name] = &toolRPCClient{br: b, path: path, name: d.Name, def: def}
			}
		}
	}
	if !protoOK {
		// 旧协议:单工具
		var defJSON string
		if cerr := cl.Call("Plugin.Definition", struct{}{}, &defJSON); cerr == nil {
			var def sdk.ToolDefinition
			if json.Unmarshal([]byte(defJSON), &def) == nil && def.Name != "" {
				e.proto = 1
				protoOK = true
				e.tools[def.Name] = &toolRPCClient{br: b, path: path, name: def.Name, def: def}
			}
		}
	}
	// M14 命令声明(可选;旧插件无 Commands 方法 = 空命令集,行为不变)
	cmdOK := false
	cmdRaw := ""
	if cerr := cl.Call("Plugin.Commands", struct{}{}, &cmdRaw); cerr == nil && cmdRaw != "" {
		var cmds []CommandDTO
		if json.Unmarshal([]byte(cmdRaw), &cmds) == nil && len(cmds) > 0 {
			cmdOK = true
			e.commands = make(map[string]*commandRPCClient, len(cmds))
			for _, cd := range cmds {
				name := cd.Name
				e.commands[name] = &commandRPCClient{br: b, path: path, name: name, def: cd, timeoutMs: cd.TimeoutMs}
			}
		}
	}
	if !protoOK && !cmdOK {
		killFn()
		return nil, fmt.Errorf("host-bridge: 插件未按桥协议暴露工具或命令 %s", path)
	}
	return e, nil
}

// registerAll 注册全部工具+命令并返回聚合注销。
// 命令经 ctx.commands Register(同名冲突显式记警告并跳过该命令,插件继续加载——
// 对齐工具同名策略);ctx.commands 未装配时命令不注册,工具不受影响。
func (b *Bridge) registerAll(e *extEntry) sdk.Disposer {
	disposers := make([]sdk.Disposer, 0, len(e.tools)+len(e.commands))
	for _, t := range e.tools {
		disposers = append(disposers, b.tools.Register(t))
	}
	for _, cmd := range e.commands {
		if b.cmds == nil {
			continue
		}
		d, err := b.cmds.Register(cmd.spec())
		if err != nil {
			b.logErr("host-bridge: 外部命令注册冲突(跳过该命令)", "name", cmd.name, "err", err)
			continue
		}
		disposers = append(disposers, d)
	}
	return func() {
		for _, d := range disposers {
			d()
		}
	}
}

// reload 二进制变更:dispose 旧进程并加载新实例(热重载接线)。
// reloadAll 重启全部外部工具进程(工作区切换后:宿主 cwd 已变,新进程继承新 cwd)。
// 逐个 reload(先注销+kill 再 loadOne+注册);失败条目记日志跳过(软降级,同热更新)。
func (b *Bridge) reloadAll() {
	b.mu.RLock()
	paths := make([]string, 0, len(b.entries))
	for p := range b.entries {
		paths = append(paths, p)
	}
	b.mu.RUnlock()
	for _, p := range paths {
		b.reload(p)
	}
}

func (b *Bridge) reload(path string) {
	b.mu.Lock()
	entry, ok := b.entries[path]
	b.mu.Unlock()
	if !ok {
		if isExternalPluginBin(filepath.Base(path)) {
			e, err := b.loadOne(path)
			if err == nil {
				unreg := b.registerAll(e)
				b.mu.Lock()
				e.unreg = unreg
				b.entries[path] = e
				b.mu.Unlock()
			} else {
				b.logErr("host-bridge: 热重载加载新插件失败", "path", path, "err", err)
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
		b.logErr("host-bridge: 热重载更新失败,条目已撤销", "path", path, "err", err)
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
		b.logErr("host-bridge: 崩溃自动拉起失败(60s 节流内不再尝试)", "path", path, "err", err)
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
// externalEnvPass 显式放行的宿主 env 键(GAH_EXT_ENV_PASS,逗号分隔,大小写不敏感):
// 凭据默认不下传外部插件;个别外部插件确需某凭据时(如 EXA_API_KEY 经 env 而非配置文件),
// 由用户在 gah-data/env.sh 里点名放行——放行即视为用户明确把该凭据交给外部进程。
func externalEnvPass() []string {
	raw, ok := os.LookupEnv("GAH_EXT_ENV_PASS")
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if v, ok := os.LookupEnv(name); ok {
			out = append(out, name+"="+v)
		}
	}
	return out
}

func startPlugin(bin string, cbAddr, cbToken string) (*rpc.Client, func(), error) {
	cmd := exec.Command(bin)
	// 凭据隔离:外部插件进程不继承宿主凭据(滤除 *_API_KEY/*_TOKEN/AWS_* 等;GAH_* 宿主配置与
	// PATH/HOME 等基础键保留),回调通道凭据 GAH_CB_* 仅注入给插件本体,由 sdk.SanitizedEnv 拦在下游。
	// 确需凭据的插件由用户在 gah-data/env.sh 里经 GAH_EXT_ENV_PASS 显式点名放行。
	cmd.Env = sdk.SanitizedEnv(os.Environ())
	cmd.Env = append(cmd.Env, "GAH_CB_ADDR="+cbAddr, "GAH_CB_TOKEN="+cbToken)
	cmd.Env = append(cmd.Env, externalEnvPass()...)
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

// rpcTimeoutOf 超时计算:声明毫秒 >0 用之,否则全局默认(工具/命令共用)。
func rpcTimeoutOf(ms int64) time.Duration {
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return rpcTimeout
}

// rpcTimeoutFor 工具级超时覆写(P0-2):定义声明 timeout_ms 则用之,否则全局默认。
func rpcTimeoutFor(def sdk.ToolDefinition) time.Duration { return rpcTimeoutOf(def.TimeoutMs) }

// rpcCall 带超时/可取消的 RPC 调用(工具/命令共用;返回调用错误,连接类判定由调用方负责)。
// 用 net/rpc 异步 Go(自带 done channel)而非另起 goroutine 包同步 Call:
// 后者在超时路径会把 goroutine 永久阻塞在 channel 上(泄漏到插件进程退出)。
func rpcCall(cl *rpc.Client, method string, args any, reply any, timeout time.Duration) error {
	if cl == nil {
		return fmt.Errorf("rpc 调用 %s: 连接不存在", method)
	}
	call := cl.Go(method, args, reply, make(chan *rpc.Call, 1))
	if timeout <= 0 {
		<-call.Done
		return call.Error
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-call.Done:
		return call.Error
	case <-timer.C:
		return fmt.Errorf("rpc 调用 %s 超时(>%s)", method, timeout)
	}
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

// commandRPCClient M14:外部命令的宿主侧代理(经 RPC 转发执行/枚举选项;
// 连接错误触发自动拉起)。def 为注册时声明快照;timeoutMs 为命令声明的超时(0=全局默认)。
type commandRPCClient struct {
	br        *Bridge
	path      string
	name      string
	def       CommandDTO
	timeoutMs int64
}

// spec 构造 sdk.CommandSpec(DTO 级联转 ArgLevel:Enum 级→Options 经 RPC 求值,
// FreeArgs 级→静态参数名;TUI 提示/help/选择器零改动自动生效)。
func (c *commandRPCClient) spec() sdk.CommandSpec {
	return sdk.CommandSpec{
		Name:  c.def.Name,
		Usage: c.def.Usage,
		Desc:  c.def.Desc,
		Run:   c.run,
		Args:  c.args(),
	}
}

// run 命令执行(宿主 TUI 线程同步 + RPC 超时保护:死进程/慢命令不阻塞 UI;
// 输出文本+结构化错误,连接错误触发自动拉起)。
func (c *commandRPCClient) run(args []string) (string, error) {
	cl := c.br.clientFor(c.path)
	if cl == nil {
		return "", errors.New("外部命令插件重建中(崩溃自动拉起)")
	}
	timeout := rpcTimeoutOf(c.timeoutMs)
	var reply ExecReply
	err := rpcCall(cl, "Plugin.RunCommand", &RunCommandArgs{Name: c.name, Args: args}, &reply, timeout)
	if isConnErr(err) {
		c.br.onDead(c.path)
		return "", errors.New("外部命令插件不可达(进程崩溃,自动重建中): " + err.Error())
	}
	if err != nil {
		return "", errors.New("外部命令插件不可达: " + err.Error())
	}
	if reply.Error != "" {
		return reply.Content, errors.New(reply.Error)
	}
	return reply.Content, nil
}

// args 级联声明:DEF 每级 Enum→sdk.ArgLevel.Options(经 CommandOptions RPC);
// FreeArgs 非空→自由级;皆空→无定义级(直接执行)。
func (c *commandRPCClient) args() []sdk.ArgLevel {
	levels := make([]sdk.ArgLevel, 0, len(c.def.Args))
	for i, d := range c.def.Args {
		if d.Enum {
			levels = append(levels, sdk.ArgLevel{Options: func(picked []string) []sdk.Option {
				return c.enumOptions(i, picked)
			}})
			continue
		}
		if len(d.FreeArgs) > 0 {
			free := d.FreeArgs
			levels = append(levels, sdk.ArgLevel{FreeArgs: func([]string) []string { return free }})
			continue
		}
		levels = append(levels, sdk.ArgLevel{})
	}
	return levels
}

// enumOptions 枚举级选项(经 RPC 求值 + 超时保护;失败/连接断开/超时 = nil 无选项,
// 选择器直接执行,不阻塞 UI)。
func (c *commandRPCClient) enumOptions(level int, picked []string) []sdk.Option {
	cl := c.br.clientFor(c.path)
	if cl == nil {
		return nil
	}
	var raw string
	err := rpcCall(cl, "Plugin.CommandOptions", &CmdOptionsArgs{Name: c.name, Level: level, Picked: picked}, &raw, rpcTimeoutOf(c.timeoutMs))
	if isConnErr(err) {
		c.br.onDead(c.path)
		return nil
	}
	if err != nil || raw == "" {
		return nil
	}
	var opts []sdk.Option
	if json.Unmarshal([]byte(raw), &opts) != nil {
		return nil
	}
	return opts
}

// isExternalPluginBin 外部插件二进制文件名识别:tool-*(工具/工具+命令)
// 或 cmd-*(纯命令插件,M14 后)。
func isExternalPluginBin(name string) bool {
	return strings.HasPrefix(name, "tool-") || strings.HasPrefix(name, "cmd-")
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

// pluginHome 运行时数据根(GAH_HOME 恒设;空仅嵌入/单测 → TempDir,~/.gah 兜底已弃用)。
func pluginHome() string { return sdk.Home() }

// randomToken M7:回调通道握手 token(本进程随机,防本机任意进程连回调)。
func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("tok-%d", time.Now().UnixNano()) // 兜底:时间戳(非安全场景足够)
	}
	return hex.EncodeToString(b)
}
