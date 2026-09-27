// pluginsandbox.go:外部插件**进程级**内核沙箱接线(2026-09-27 安全审计 A3 的后半)。
//
// 背景:A3 先把内核沙箱抽到 internal/kernelsandbox 并覆盖 MCP server;插件进程本身当时延后,
// 因为有两条结构性阻断(均已实证,见 DESIGN R10 ①):
//
//	① 嵌套不可能:tool-basic 就是 shell 提供者(import toolshell),宿主套它 → 它按调用再套
//	   `sandbox-exec` → `sandbox_apply: Operation not permitted`(整个插件不可用);
//	② 档位会在插件启动时刻被冻结:profile 是启动时的静态串,而档位/工作根运行期会变
//	   (`/sandbox` 切档、`/ws` 切工作区、审批联动)。
//
// 本文件是这两条的解法:
//
//	① → **能力自报**:插件用 ServeToolsWith(Capabilities{SandboxProvider:true}) 明说"我自己按调用施加",
//	   宿主据此不包装(而不是靠猜或靠名字表)。
//	② → **fail-closed 重载**:被包装的插件记住启动时的档位/根;每次调用前与当前有效值比较,
//	   不一致 → 触发重载 + 本次调用拒绝(不拿旧 profile 继续跑,免得"档位已切严但仍按旧档写")。
//
// 未被包装的插件(自报提供者、全权档、显式关闭、宿主自己就在内核沙箱内)沿各自原路径工作,行为不变。
package hostbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/kernelsandbox"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// CapsFlag 能力自报探测参数:宿主执行 `插件 --gah-caps`,插件打印能力 JSON 后立即退出。
//
// 为什么要"先探测再启动"(而不是启动后用 RPC 问):包装 argv 必须在 exec **之前**定下来,
// 而 RPC 只能在进程起来之后。探测是一个短命进程(不建 RPC、不注册工具、不调回调),
// 真身按探测结果一次性带包装启动 —— 避免"先无沙箱跑一遍再杀掉重来"的空窗。
const CapsFlag = "--gah-caps"

// capsProbeTimeout 探测超时:插件若不认这个参数而进入正常握手流程,会因缺 GAH_PLUGIN 而退出;
// 万一它卡住也不能拖住加载(超时 = 视为未声明 = 按普通插件包装,安全侧默认)。
const capsProbeTimeout = 3 * time.Second

// 插件进程内核沙箱的三个开关(命名对齐既有 GAH_EXT_* 一族;MCP server 用无 _PLUGIN_ 的那组区分)。
const (
	// pluginKernelSandboxEnv 显式关闭("0" = 关;关闭后插件进程回到"仅协作式控制")。
	pluginKernelSandboxEnv = "GAH_EXT_PLUGIN_SANDBOX"
	// pluginRWPathsEnv 额外可写路径(冒号分隔):插件要写自己的数据目录/DB 时由用户点名。
	pluginRWPathsEnv = "GAH_EXT_PLUGIN_RW_PATHS"
	// pluginCredReadDenyEnv 内核层凭据**读**拒绝("1" = 开;默认关 —— 读凭据是插件正当职责)。
	pluginCredReadDenyEnv = "GAH_EXT_PLUGIN_CRED_READ_DENY"
)

// dataRootReserved 数据根下**不得**被插件声明为可写的目录(见 validDataWrites)。
//
// config 放 provider/search 凭据,plugins/ui-plugins 放可执行产物 —— 三者可写 =
// 插件可给自己提权或让别的插件被替换(持久化),把 A3 的目的反过来用。
var dataRootReserved = map[string]bool{"config": true, "plugins": true, "ui-plugins": true}

// probeCapabilities 探测插件自报能力(见 CapsFlag)。第二个返回值 = 探测成功。
// 探测失败一律归一为"未声明"(零值):旧插件、非 ServeTools 插件、卡死插件都走这条路,
// 宿主据此按**普通插件**处理(包装),这是安全侧默认。
func probeCapabilities(bin string) (Capabilities, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), capsProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, CapsFlag)
	// 探测进程不继承宿主凭据(与真正启动同一纪律),也不给 GAH_CB_*/GAH_PLUGIN:
	// 插件若不认该参数会走进正常握手流程并因缺 GAH_PLUGIN 立刻退出(不会误报能力)。
	cmd.Env = sdk.SanitizedEnv(os.Environ())
	out, err := cmd.Output() // stderr 直接丢弃:探测失败的原因不影响结论
	if err != nil {
		return Capabilities{}, false
	}
	var c Capabilities
	if json.Unmarshal(bytes.TrimSpace(out), &c) != nil {
		return Capabilities{}, false
	}
	return c, true
}

// validDataWrites 校验插件自报的数据根可写子目录,返回绝对路径白名单。
//
// 为什么必须校验:声明来自**被约束方**。不校验就等于"插件可以自行申请写 $GAH_HOME/config
// (provider.yaml 里的密钥)或 ../../etc"—— 把沙箱反过来用。非法项**逐项丢弃并记 ERROR**
// (不整体拒绝加载:插件其余功能仍可用,只是那条白名单不给),这是"能被解释的失败"。
func (b *Bridge) validDataWrites(path string, decl []string) []string {
	const maxItems, maxLen = 16, 64
	var out []string
	for i, raw := range decl {
		if i >= maxItems {
			b.logErr("host-bridge: 插件自报数据目录过多,超出部分已忽略", "path", path, "max", maxItems)
			break
		}
		d := strings.TrimSpace(raw)
		switch {
		case d == "":
			continue // 空项静默跳过(不制造噪音)
		case len(d) > maxLen:
			b.logErr("host-bridge: 插件自报数据目录名过长,已忽略", "path", path, "dir", raw, "max", maxLen)
			continue
		case filepath.IsAbs(d), strings.ContainsAny(d, `/\`), d == ".", d == "..":
			b.logErr("host-bridge: 插件自报数据目录必须是数据根下的直接子目录名,已忽略", "path", path, "dir", raw)
			continue
		case dataRootReserved[d]:
			b.logErr("host-bridge: 插件自报数据目录落在保留集(凭据/插件产物),已忽略", "path", path, "dir", d)
			continue
		}
		out = append(out, filepath.Join(sdk.Home(), d))
	}
	return out
}

// pluginSandboxSpec 组装外部插件进程的内核沙箱规格。
//
// 档位来源:宿主自己的 ctx.sandbox(**现取**,不缓存 —— 装配顺序无保证,插件可能先于 sandbox 加载;
// 档位为空 = 无沙箱宿主/未联动 → Mode 空 → Wrap 不施加且不告警)。
func (b *Bridge) pluginSandboxSpec(dataWrites []string) kernelsandbox.Spec {
	mode, root := b.sandboxModeRoot()
	rw := append([]string{}, kernelsandbox.DefaultRWPaths()...)
	rw = append(rw, kernelsandbox.RWPathsFromEnv(pluginRWPathsEnv)...)
	rw = append(rw, dataWrites...) // 插件自报(已校验)的数据根子目录
	spec := kernelsandbox.Spec{
		Mode:           sdk.SandboxMode(mode),
		Root:           root,
		Jail:           kernelsandbox.EnsureJailDir(),
		RW:             rw,
		Switch:         pluginKernelSandboxEnv,
		Label:          "外部插件",
		ReadDenySwitch: pluginCredReadDenyEnv,
	}
	// 刻意**不**接 GAH_SHELL_JAIL:那个开关的语义是 "shell 的临时区重定向关掉",
	// 与"插件进程白名单锚点是否可用"不是同一件事(锚点目录仍在,只是 shell 不往里写)。
	// 插件侧的关闭口径只有 GAH_EXT_PLUGIN_SANDBOX=0。
	if os.Getenv(pluginCredReadDenyEnv) == "1" {
		spec.ReadDeny = sdk.CredentialDenyDirs()
	}
	return spec
}

// wrapPluginArgv 组装插件的启动 argv 与额外环境(纯函数便于断言)。
//
// 第二个返回值 = 若施加了内核沙箱,要追加给**插件进程**的环境(打 MarkerEnv);
// 第三个返回值 = 是否真的施加了(供 extEntry 记录,决定要不要做档位陈旧检查)。
func (b *Bridge) wrapPluginArgv(bin string, caps Capabilities, capsKnown bool, dataWrites []string) ([]string, []string, bool) {
	if capsKnown && caps.SandboxProvider {
		// 自报内核沙箱提供者(如 tool-basic:它的 shell 每次调用自己套)。套上会**嵌套失败**,
		// 让整个插件不可用(阻断 ①)—— 这是"自报"而不是宿主按名字猜的原因。
		return []string{bin}, nil, false
	}
	pre := kernelsandbox.Wrap(b.pluginSandboxSpec(dataWrites))
	if len(pre) == 0 {
		return []string{bin}, nil, false
	}
	// 标记已在内核沙箱内:插件再起的子进程(它内部的 MCP server、子命令)不得也不能重复施加。
	return kernelsandbox.PrefixedArgv(pre, bin), []string{kernelsandbox.MarkerEnv + "=1"}, true
}

// sandboxStale 判定被包装插件的**启动档位/根**是否已过期(阻断 ②)。
//
// 返回非空消息 = 已触发异步重载,调用方必须 fail-closed 拒绝本次调用。
// 比较两侧都取 b.sandboxModeRoot()(不是调用方 ctx 的 hint):profile 就是用它构建的,
// 同源比较才是同构的;命令类调用拿不到 ctx hint,同源也让工具/命令两条路径共用一份实现。
//
// 语义取舍:不拿旧 profile 继续跑 —— 档位从宽切严时,"先按旧档执行完再重载"就是一次真实的越权写;
// 从严切宽同理会让插件按旧档失败(表现为莫名其妙的功能坏),统一重载最可解释。
func (b *Bridge) sandboxStale(path string) (string, bool) {
	b.mu.RLock()
	e, ok := b.entries[path]
	b.mu.RUnlock()
	if !ok || !e.wrapped {
		return "", false
	}
	mode, root := b.sandboxModeRoot()
	if mode == e.wrapMode && root == e.wrapRoot {
		return "", false
	}
	b.requestReload(path)
	return fmt.Sprintf("外部插件沙箱上下文已变更(档位 %s→%s,根 %s→%s):已按新档位重建,请重新调用",
		sbDesc(e.wrapMode), sbDesc(mode), sbDesc(e.wrapRoot), sbDesc(root)), true
}

// requestReload 触发一次异步重载(节流:重载完成前连续调用不重复建进程)。
func (b *Bridge) requestReload(path string) {
	b.mu.Lock()
	e, ok := b.entries[path]
	if !ok || time.Now().Before(e.reloadAt) {
		b.mu.Unlock()
		return
	}
	e.reloadAt = time.Now().Add(5 * time.Second)
	b.mu.Unlock()
	go func() {
		if err := b.reload(path); err != nil && !errors.Is(err, errPluginIdle) {
			b.logErr("host-bridge: 档位变更后的重载失败(条目已撤销,下次加载重试)", "path", path, "err", err)
		}
	}()
}

// sbDesc 档位/根的日志展示(空 = 未注入,别打印成空串让人误以为"没有档位")。
func sbDesc(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未注入"
	}
	return s
}
