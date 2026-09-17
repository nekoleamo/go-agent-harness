// Package hostbridge 提供 host-bridge 插件:外部进程插件桥(宿主侧,崩溃隔离)。
// 扫描外部插件目录,加载 tool-* 二进制(go-plugin/gRPC),注册为 sdk.Tool;
// P0:工具级超时(TimeOutMs 覆写全局 3s)+ 进程崩溃自动拉起(连接错误
// → 节流重建进程,下次调用走新实例);多工具协议(ExecuteNamed/Definitions,
// 旧单工具协议自动回退);执行可中断(CallID + Plugin.Cancel,回合适时取消)。
package hostbridge

import (
	"bytes"
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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	// NOND-M1 第 3 步:宿主侧外部插件控制面(配置改完后重读,免重启)。
	// Provide 失败 = 装配层错误(键冲突),显式返回而非静默跳过。
	if err := c.Provide("ctx.extplugins", sdk.ExternalPlugins(b)); err != nil {
		cbClose()
		return nil, err
	}
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
	// reloadMu 串行化 reload(文件监听 / 工作区切换 / ctx.extplugins.Reload 三处入口并发时,
	// 同一路径不得双载:否则先载的实例被后载覆盖且永不回收 = 进程泄漏 + 工具双注册残留)。
	reloadMu sync.Mutex
	// closed 卸载标记:closeAll 之后不得再 respawn(否则崩溃重启与卸载撞车时,
	// 外部进程与工具注册永久残留 → 违反"卸载即撤销")。
	closed bool
}

// logErr 记录外部插件加载失败(P3 软降级:不拖垮 boot,但信息不丢失)。
func (b *Bridge) logErr(msg string, kv ...any) {
	lg := b.lg
	if lg == nil {
		lg = slog.Default()
	}
	lg.Error(msg, kv...)
}

// logInfo 记录非故障事件(插件自述空闲等)。与 logErr 分开:未使用某功能不是错误,
// 不该在用户看到的启动日志里呈现为失败。
func (b *Bridge) logInfo(msg string, kv ...any) {
	lg := b.lg
	if lg == nil {
		lg = slog.Default()
	}
	lg.Info(msg, kv...)
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
			if errors.Is(lerr, errPluginIdle) {
				// 插件自述空闲(如 tool-mcp 未配置任何 server):记 INFO 跳过。
				// 「没用到某功能」不是故障,启动日志不该为它报 ERROR。
				b.logInfo("host-bridge: 外部插件未参与(自述空闲)", "path", path, "reason", lerr)
				return nil
			}
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
	cl, killFn, se, err := startPlugin(path, b.cbAddr, b.cbToken)
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
				def := sdk.ToolDefinition{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema, TimeoutMs: d.TimeoutMs, PathParams: d.PathParams, ApprovalTargetParam: d.ApprovalTargetParam}
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
		// 带上插件自己的 stderr:插件在握手前就退出时,go-plugin 只会说
		// "Failed to read any lines from plugin's stdout",真正的原因在它 stderr 里。
		return nil, idleOr(decorateStderr(fmt.Errorf("host-bridge: 插件未按桥协议暴露工具或命令 %s", path), se), se)
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

// Reload 按外部插件名重启进程(实现 sdk.ExternalPlugins;名字 = 外部插件目录名)。
// 与文件监听热重载共用 reload 路径(同一把 reloadMu 串行化)。
// 未加载但目录存在 = 从磁盘补加载(用户刚加的第一个 MCP server 无需重启即可生效)。
func (b *Bridge) Reload(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("host-bridge: 缺少外部插件名")
	}
	path, err := b.pluginPath(name)
	if err != nil {
		return err
	}
	b.mu.RLock()
	_, loaded := b.entries[path]
	b.mu.RUnlock()
	if !loaded {
		// 未加载:目录里没有(pluginPath 已报错)或上次加载失败/自述空闲;
		// reload 对"不在 entries 里的路径"走 loadOne 补加载分支。
		// 记 INFO 而非 ERROR:用户刚加第一个 MCP server 走的就是这条路,不是故障。
		b.logInfo("host-bridge: 外部插件尚未加载,按补加载处理", "name", name, "path", path)
	}
	rerr := b.reload(path)
	if errors.Is(rerr, errPluginIdle) {
		// 自述空闲(未配置任何 server)不是重启失败:配置已保存,只是没有要加载的东西。
		return nil
	}
	b.mu.RLock()
	_, ok := b.entries[path]
	b.mu.RUnlock()
	if !ok {
		return fmt.Errorf("host-bridge: 重启 %s 失败(详见日志;多为进程启动失败或握手拒绝)", name)
	}
	return nil
}

// pluginPath 由外部插件名定位二进制(名字 = 文件基名去平台扩展名,如 "tool-mcp")。
// 两种落点都支持:
//   - 发布布局 plugins/<名>/<名>[.exe](internal/embed.pluginDst 释放);
//   - 扁平布局 plugins/<名>[.exe](测试/手工摆放)。
//
// 不经名称硬编码扩展名 —— Windows 产物是 tool-mcp.exe,目录名仍是 tool-mcp。
func (b *Bridge) pluginPath(name string) (string, error) {
	// 1) 已加载条目优先(任意目录布局都能命中;不必猜落点)
	b.mu.RLock()
	for path := range b.entries {
		if externalPluginName(path) == name {
			b.mu.RUnlock()
			return path, nil
		}
	}
	b.mu.RUnlock()

	// 2) 未加载:在两种落点里找(目录不存在/为空 = 未安装)
	var cands []string
	if ents, err := os.ReadDir(b.dir); err == nil {
		for _, e := range ents {
			if !e.IsDir() && isExternalPluginBin(e.Name()) && externalPluginName(e.Name()) == name {
				cands = append(cands, filepath.Join(b.dir, e.Name()))
			}
		}
	}
	sub := filepath.Join(b.dir, name)
	if ents, err := os.ReadDir(sub); err == nil {
		for _, e := range ents {
			if !e.IsDir() && isExternalPluginBin(e.Name()) {
				cands = append(cands, filepath.Join(sub, e.Name()))
			}
		}
	}
	switch len(cands) {
	case 0:
		return "", fmt.Errorf("host-bridge: 外部插件 %q 未安装(在 %s 下找不到 %s[.exe] 或 %s/%s)",
			name, b.dir, name, name, name)
	case 1:
		return cands[0], nil
	default:
		sort.Strings(cands)
		return "", fmt.Errorf("host-bridge: 外部插件 %q 有多个候选 %v,请只保留一个", name, cands)
	}
}

// externalPluginName 由二进制路径推外部插件名(文件基名去 .exe)。
func externalPluginName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".exe")
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

func (b *Bridge) reload(path string) error {
	b.reloadMu.Lock()
	defer b.reloadMu.Unlock()
	b.mu.Lock()
	entry, ok := b.entries[path]
	b.mu.Unlock()
	if !ok {
		if isExternalPluginBin(filepath.Base(path)) {
			e, err := b.loadOne(path)
			switch {
			case err == nil:
				unreg := b.registerAll(e)
				b.mu.Lock()
				e.unreg = unreg
				b.entries[path] = e
				b.mu.Unlock()
			case errors.Is(err, errPluginIdle):
				// 自述空闲(如 tool-mcp 未配置 server):没用到这个功能,不是故障。
				b.logInfo("host-bridge: 外部插件未参与(自述空闲)", "path", path, "reason", err)
				return errPluginIdle
			default:
				b.logErr("host-bridge: 热重载加载新插件失败", "path", path, "err", err)
				return err
			}
		}
		return nil
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
		if errors.Is(err, errPluginIdle) {
			// 配置被清空等 ⇒ 插件自述空闲:旧条目已正常撤销,只是不再参与(不是失败)。
			b.logInfo("host-bridge: 外部插件未参与(自述空闲)", "path", path, "reason", err)
			return errPluginIdle
		}
		b.logErr("host-bridge: 热重载更新失败,条目已撤销", "path", path, "err", err)
		return err
	}
	unreg := b.registerAll(e)
	b.mu.Lock()
	e.unreg = unreg
	b.entries[path] = e
	b.mu.Unlock()
	return nil
}

// closeAll 关闭全部插件条目:先摘条目(短锁),再锁外 unreg/kill——
// kill 是优雅等待(每插件最长 2s),持 b.mu 会让所有 clientFor 阻塞。
func (b *Bridge) closeAll() {
	b.mu.Lock()
	b.closed = true
	ents := make([]*extEntry, 0, len(b.entries))
	for p, e := range b.entries {
		ents = append(ents, e)
		delete(b.entries, p)
	}
	b.mu.Unlock()
	for _, e := range ents {
		e.unreg()
		e.kill()
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
	if b.closed {
		b.mu.Unlock()
		return
	}
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
	if b.closed { // 卸载与重建撞车:不留"复活"的进程与工具注册
		b.mu.Unlock()
		e.kill()
		return
	}
	old := b.entries[path]
	b.mu.Unlock()
	if old != nil {
		old.unreg()
	}
	unreg := b.registerAll(e)
	b.mu.Lock()
	if b.closed { // 注册期间卸载:立即撤销本次注册与进程
		b.mu.Unlock()
		unreg()
		e.kill()
		return
	}
	e.unreg = unreg
	b.entries[path] = e
	b.mu.Unlock()
	if old != nil {
		old.kill()
	}
}

// startPlugin 启动外部插件进程,返回 rpc client 与 kill 函数(崩溃隔离:死进程快速失败)。
// 回调通道:宿主地址经 GAH_CB_ADDR 环境变量注入(外部进程 Dial 后请求宿主服务)。
// externalEnvPass 显式放行的宿主 env 键(GAH_EXT_ENV_PASS,逗号分隔;键名大小写敏感,
// 与 os.LookupEnv 语义一致,写错大小写即静默不放行):
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

func startPlugin(bin string, cbAddr, cbToken string) (*rpc.Client, func(), *pluginStderr, error) {
	cmd := exec.Command(bin)
	// 凭据隔离:外部插件进程不继承宿主凭据(滤除 *_API_KEY/*_TOKEN/AWS_* 等;GAH_* 宿主配置与
	// PATH/HOME 等基础键保留),回调通道凭据 GAH_CB_* 仅注入给插件本体,由 sdk.SanitizedEnv 拦在下游。
	// 确需凭据的插件由用户在 gah-data/env.sh 里经 GAH_EXT_ENV_PASS 显式点名放行。
	cmd.Env = sdk.SanitizedEnv(os.Environ())
	cmd.Env = append(cmd.Env, "GAH_CB_ADDR="+cbAddr, "GAH_CB_TOKEN="+cbToken)
	cmd.Env = append(cmd.Env, externalEnvPass()...)
	cmd.SysProcAttr = pluginProcAttr() // 独立进程组:退出时可连插件派生的子进程一并回收
	// 插件子进程的 stderr 默认被 go-plugin 丢进 io.Discard(实测 client.go:402),
	// 于是插件自己说的原因(缺配置/端口占用/权限)全部丢失。这里接到环形缓冲上:
	// 成功时不影响任何输出,失败时拼进错误上报。
	se := &pluginStderr{}
	client := plugin.NewClient(&plugin.ClientConfig{
		HandshakeConfig: handshake,
		Plugins: map[string]plugin.Plugin{
			pluginName: &toolPluginBridge{},
		},
		Cmd:    cmd,
		Stderr: se,
		// SkipHostEnv 必须为 true:go-plugin 默认会 `cmd.Env = append(cmd.Env, os.Environ()...)`,
		// 即把我们过滤后的 SanitizedEnv **后面**再接一份完整宿主环境(同名后者胜)→ 凭据隔离被
		// 静默绕过(实测子进程能看到 EXA_API_KEY/*_TOKEN)。置 true 后仅用上面的 cmd.Env;
		// go-plugin 自己需要追加的 PLUGIN_* 握手键仍在之后单独 append,不受影响。
		SkipHostEnv: true,
	})
	proto, err := client.Client()
	if err != nil {
		client.Kill()
		return nil, nil, se, idleOr(decorateStderr(fmt.Errorf("host-bridge: 启动外部插件失败 %s", bin), se), se)
	}
	raw, err := proto.Dispense(pluginName)
	if err != nil {
		client.Kill()
		return nil, nil, se, idleOr(decorateStderr(fmt.Errorf("host-bridge: 插件握手失败 %s", bin), se), se)
	}
	tc, ok := raw.(*rpcClientOnly)
	if !ok {
		client.Kill()
		return nil, nil, se, fmt.Errorf("host-bridge: 意外的插件类型 %T", raw)
	}
	return tc.client, func() {
		proto.Close()
		client.Kill()
		killPluginGroup(cmd) // 组杀残余后代(MCP server 等):仅杀插件本体不够
	}, se, nil
}

// pluginStderrLines / pluginStderrBytes 保留插件 stderr 的尾部上限(行数 + 字符数)。
const (
	pluginStderrLines = 8
	pluginStderrBytes = 600
)

// pluginStderr 捕获外部插件子进程 stderr 的尾部(go-plugin 默认丢弃它)。
// 插件在 go-plugin 握手之前退出时,宿主只能看到「Failed to read any lines from
// plugin's stdout」这类谜语 —— 而原因(缺配置/端口占用/权限)正是它写在 stderr 的第一行。
// 并发写自查(go-plugin 单 goroutine 转写,仍然加锁以防未来变动)。
// 桌面版没有终端,这是插件类故障唯一的现场。
type pluginStderr struct {
	mu    sync.Mutex
	lines []string
	part  []byte // 未收行的残留(xattr:插件可能不换行就退出)
}

func (p *pluginStderr) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.part = append(p.part, b...)
	for {
		i := bytes.IndexByte(p.part, '\n')
		if i < 0 {
			break
		}
		p.push(string(p.part[:i]))
		p.part = p.part[i+1:]
	}
	if len(p.part) > pluginStderrBytes { // 超长不换行的输出不能无限涨
		p.push(string(p.part))
		p.part = nil
	}
	return len(b), nil
}

// push 只保留最近 pluginStderrLines 行(调用方持锁)。
func (p *pluginStderr) push(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	p.lines = append(p.lines, line)
	if len(p.lines) > pluginStderrLines {
		p.lines = p.lines[len(p.lines)-pluginStderrLines:]
	}
}

// tail 返回可拼进错误的尾部文本(含尚未换行的残留),超长则截断。
func (p *pluginStderr) tail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	lines := p.lines
	if s := strings.TrimSpace(string(p.part)); s != "" {
		lines = append(append([]string{}, lines...), s)
	}
	out := strings.Join(lines, " | ")
	if len(out) > pluginStderrBytes {
		out = "…" + out[len(out)-pluginStderrBytes:]
	}
	return out
}

// errPluginIdle 插件自述「本轮不参与」(未配置后端等)。不是故障:boot 不该为它记 ERROR。
var errPluginIdle = errors.New("外部插件未参与本次加载(自述空闲)")

// idleOr 插件 stderr 里出现 IdleMarker ⇒ 归类为 errPluginIdle(拿掉 go-plugin 那层谜语),
// 否则原样返回。调用方用 errors.Is 分流:空闲记 INFO,失败记 ERROR。
func idleOr(err error, se *pluginStderr) error {
	if se == nil {
		return err
	}
	t := se.tail()
	i := strings.Index(t, IdleMarker)
	if i < 0 {
		return err
	}
	reason := strings.TrimSpace(t[i+len(IdleMarker):])
	if j := strings.Index(reason, " | "); j >= 0 { // tail 用 " | " 连接多行:只取标记那一行
		reason = reason[:j]
	}
	return fmt.Errorf("%w: %s", errPluginIdle, reason)
}

// decorateStderr 把插件自身 stderr 的尾部拼进宿主错误;没有输出则原样返回。
func decorateStderr(err error, se *pluginStderr) error {
	if se == nil {
		return err
	}
	t := se.tail()
	if t == "" {
		return err
	}
	return fmt.Errorf("%w;插件自身输出: %s", err, t)
}

// defDTO 外部工具定义载荷(两侧共用:serve.go 序列化、bridge.go 反序列化——
// 单一类型避免字段漂移;旧单工具协议则由 sdk.ToolDefinition 直接反序列化)。
type defDTO struct {
	Name        string          `json:"Name"`
	Description string          `json:"Description"`
	InputSchema map[string]any  `json:"InputSchema"`
	TimeoutMs   int64           `json:"TimeoutMs"`
	PathParams  []sdk.PathParam `json:"PathParams,omitempty"` // 路径参数能力声明(透传给宿主裁决)
	// ApprovalTargetParam 代理工具的「真实目标参数名」(透传给宿主审批;NOND-M1-3b)。
	ApprovalTargetParam string `json:"ApprovalTargetParam,omitempty"`
}

// rpcTimeout 默认外部 RPC 调用超时(崩溃隔离:死进程快速失败而非死等)。
const rpcTimeout = 3 * time.Second

// cancelRPCTimeout 取消 RPC 自身的超时(取消是尽力而为:插件无响应也不能拖住调用方)。
const cancelRPCTimeout = 2 * time.Second

// callSeq 外部调用序号(CallID 组成部分;同插件内唯一即可,无需全局唯一)。
var callSeq atomic.Uint64

// nextCallID 生成本次调用标识:插件名 + 序号(插件侧登记/取消都只按字符串匹配)。
func nextCallID(path string) string {
	return fmt.Sprintf("%s#%d", filepath.Base(path), callSeq.Add(1))
}

// pluginSideTimeoutMs 传给插件侧的执行超时:取宿主超时的 ~80%,让插件在正常超时下
// 先自行结束并回明确错误;宿主侧 100% 计时 + Cancel 兜底(处理忽略 ctx 的工具)。
func pluginSideTimeoutMs(host time.Duration) int64 {
	return int64(host/time.Millisecond) * 8 / 10
}

// cancelCall 尽力中断插件侧运行中的调用(旧插件无 Cancel 方法 → 方法缺失,静默忽略)。
func cancelCall(cl *rpc.Client, callID string) {
	if cl == nil || callID == "" {
		return
	}
	var ok bool
	_ = rpcCall(cl, "Plugin.Cancel", &CancelArgs{CallID: callID}, &ok, cancelRPCTimeout)
}

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

// rpcCallCtx rpcCall 的可取消版本(⑥):ctx 取消 / 超时 → 先发 Cancel RPC 中断插件侧执行,
// 再把取消/超时错误回传调用方。不等待插件回复(响应写入带缓冲的 done channel,不泄漏
// goroutine——同 rpcCall 纪律);cancelFn 为 nil 时退化为纯超时(旧路径语义)。
func rpcCallCtx(ctx context.Context, cl *rpc.Client, method string, args, reply any, timeout time.Duration, cancelFn func()) error {
	if cl == nil {
		return fmt.Errorf("rpc 调用 %s: 连接不存在", method)
	}
	abort := func(err error) error {
		if cancelFn != nil {
			cancelFn()
		}
		return err
	}
	if timeout <= 0 {
		call := cl.Go(method, args, reply, make(chan *rpc.Call, 1))
		select {
		case <-call.Done:
			return call.Error
		case <-ctx.Done():
			return abort(ctx.Err())
		}
	}
	call := cl.Go(method, args, reply, make(chan *rpc.Call, 1))
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-call.Done:
		return call.Error
	case <-timer.C:
		return abort(fmt.Errorf("rpc 调用 %s 超时(>%s)", method, timeout))
	case <-ctx.Done():
		return abort(ctx.Err())
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
	// 调用方 ctx 的截止时间收紧超时(不另起 goroutine 包装同步 Call:超时路径会
	// 永久泄漏 goroutine——与 rpcCall 注释同一纪律)。
	if dl, ok := ctx.Deadline(); ok {
		if until := time.Until(dl); until > 0 && until < timeout {
			timeout = until
		}
	}
	if err := ctx.Err(); err != nil {
		return map[string]any{"error": "外部插件调用取消 " + err.Error()}, nil
	}
	var reply ExecReply
	callID := nextCallID(t.path)
	cancelFn := func() { cancelCall(cl, callID) }
	// 有效档位随调用下传(见 sdk.SandboxHint):外部插件进程拿不到 ctx.sandbox 服务,
	// 档位联动对它不可见 —— 无此字段则插件只能退回协作式控制。
	sbMode, sbRoot := sandboxHintFields(ctx)
	err := rpcCallCtx(ctx, cl, "Plugin.ExecuteNamed",
		&ExecNamedArgs{Name: t.name, JSONArgs: args, CallID: callID, TimeoutMs: pluginSideTimeoutMs(timeout), SandboxMode: sbMode, WorkspaceRoot: sbRoot},
		&reply, timeout, cancelFn)
	if err != nil && isMethodMissing(err) {
		// 旧单工具协议回退(CallID 对旧插件无效:字段被忽略,行为不变)
		err = rpcCallCtx(ctx, cl, "Plugin.Execute",
			&ExecArgs{JSONArgs: args, CallID: callID, TimeoutMs: pluginSideTimeoutMs(timeout), SandboxMode: sbMode, WorkspaceRoot: sbRoot},
			&reply, timeout, cancelFn)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return map[string]any{"error": "外部插件调用已取消(context canceled): " + t.name}, nil
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return map[string]any{"error": fmt.Sprintf("外部插件调用超时(>%s): %s", timeout, t.name)}, nil
		}
		if isTimeoutErr(err) {
			return map[string]any{"error": fmt.Sprintf(
				"外部插件调用超时(>%s;长耗时工具应声明 timeout_ms): %s", timeout, t.name)}, nil
		}
		if isConnErr(err) {
			t.br.onDead(t.path)
			return map[string]any{"error": "外部插件不可达(进程崩溃,自动重建中): " + err.Error()}, nil
		}
		return map[string]any{"error": "外部插件不可达: " + err.Error()}, nil
	}
	if reply.Error != "" {
		return map[string]any{"error": reply.Error}, nil
	}
	// 结果尺寸上限:gob 解码不受限,超大结果会全量进宿主内存(jobs 侧已有 1 MiB 先例)。
	if len(reply.Content) > maxBridgeResult {
		return map[string]any{"error": fmt.Sprintf(
			"外部插件结果超限(%d 字节 > %d):请缩小返回内容", len(reply.Content), maxBridgeResult)}, nil
	}
	var val any
	if err := json.Unmarshal([]byte(reply.Content), &val); err == nil {
		return val, nil
	}
	return reply.Content, nil
}

// sandboxHintFields 把 ctx 上的**有效**沙箱档位转成协议字段(见 sdk.SandboxHint)。
// 未注入 / 档位为空 → 两字段留空:对端(插件)按「未注入」处理,不得猜测档位。
func sandboxHintFields(ctx context.Context) (mode, root string) {
	h, ok := sdk.SandboxHintOf(ctx)
	if !ok || h.Mode == "" {
		return "", ""
	}
	return string(h.Mode), h.Root
}

// maxBridgeResult 外部插件单次执行结果的字节上限(超出结构化报错,不进内存)。
const maxBridgeResult = 8 << 20

// isTimeoutErr 判定 rpcCall 的超时错误(与连接错误区分:前者不该触发崩溃重建)。
func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return strings.Contains(err.Error(), "超时")
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
