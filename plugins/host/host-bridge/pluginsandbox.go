// pluginsandbox.go:外部插件**进程级**内核沙箱接线(2026-09-27 安全审计 A3 的后半)。
//
// 背景:A3 先把内核沙箱抽到 internal/kernelsandbox 并覆盖 MCP server;插件进程本身当时延后,
// 因为有两条结构性阻断(均已实证,见 DESIGN R10 ①):
//
//	① 嵌套曾不可能:tool-basic 就是 shell 提供者(import toolshell),宿主套它 → 它按调用再套
//	   `sandbox-exec` → `sandbox_apply: Operation not permitted`(整个插件不可用);
//	② 档位会在插件启动时刻被冻结:profile 是启动时的静态串,而档位/工作根运行期会变
//	   (`/sandbox` 切档、`/ws` 切工作区、审批联动)。
//
// 本文件是这两条的解法:
//
//	① → **标记免嵌套**(A6):宿主给被包装进程打 `GAH_KERNEL_SANDBOXED`,插件进程内再调
//	   `kernelsandbox.Wrap` 时看见标记直接返回 nil ⇒ 包装与自施加可叠加,shell 提供者也被
//	   外层包装(其 in-process 直写因此进内核层)。A3b 当年的 `SandboxProvider:true`(宿主
//	   据此不包装)已撤除;插件自报项只影响**策略面**(数据根白名单、凭据读拒)。
//	② → **fail-closed 重载**:被包装的插件记住启动时的档位/根;每次调用前与当前有效值比较,
//	   不一致 → 触发重载 + 本次调用拒绝(不拿旧 profile 继续跑,免得"档位已切严但仍按旧档写")。
//
// 未被包装的插件(全权档、显式关闭、宿主自己就在内核沙箱内、平台不支持)沿各自原路径工作。
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

// capsProbeTimeout 能力探测超时:插件若不认这个参数而进入正常握手流程,会因缺 GAH_PLUGIN
// 而**快速退出**;万一它卡住也不能拖住加载(超时 = 视为未声明 = 按普通插件包装,安全侧默认)。
//
// 为什么**不像** rolesProbeTimeout 那样提到 10s(2026-10-03):本探测在 **loadOne 内逐角色**执行
// (`caps, capsKnown := probeCapabilities(path, role)`),而 tool-kit 一个二进制提供四个角色
// ⇒ 最坏 4 次。把单次提到 10s 就是最坏 40s 启动 —— 那才是真的回归。
// 两者的降级方向也不同(见 rolesProbeTimeout 的注释),共用一个常量本来就不该。
//
// 这里**保持 3s**,并靠 probeCapabilities 的「超时要说清丢了什么」来补偿(见该函数)。
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

// reservedDataRootName 保留目录名判定:**大小写折叠**比较。
//
// 为何不直接用 map 精确查:默认卷在 macOS/Windows 上大小写**不敏感**,声明 "Config" 与保留的
// "config" 是同一个目录 —— 精确匹配等于给了一条绕过路(拿到凭据目录写权)。Linux 上会连带
// 拒掉真叫 Config 的独立目录;拿这个名字当插件数据目录属异常,宁可显式报错。
// 本仓其它保留判定同口径(如 policy-guard 的路径级大小写折叠)。
func reservedDataRootName(name string) bool {
	for k := range dataRootReserved {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

// probeCapabilities 探测插件自报能力(见 CapsFlag)。第二/三个返回值 = 探测成功 / 是否**超时**。
// 探测失败一律归一为"未声明"(零值):旧插件、非 ServeTools 插件、卡死插件都走这条路,
// 宿主据此按**普通插件**处理(包装),这是安全侧默认。
// role 非空 = 该二进制提供多角色,探测要走 `bin <role> <CapsFlag>`。
func probeCapabilities(bin, role string) (Capabilities, bool, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), capsProbeTimeout)
	defer cancel()
	argv := append([]string{bin}, roleArgs(role)...)
	cmd := exec.CommandContext(ctx, argv[0], append(argv[1:], CapsFlag)...)
	// 探测进程不继承宿主凭据(与真正启动同一纪律),也不给 GAH_CB_*/GAH_PLUGIN:
	// 插件若不认该参数会走进正常握手流程并因缺 GAH_PLUGIN 立刻退出(不会误报能力)。
	cmd.Env = sdk.SanitizedEnv(os.Environ())
	out, err := cmd.Output() // stderr 直接丢弃:探测失败的原因不影响结论
	if err != nil {
		// 第三个返回值 = **超时**(与「不认这个参数」严格区分)。
		//
		// 为什么必须区分:两种情形的**损失不同**。不认参数 ⇒ 插件压根没有这套声明,
		// 没有东西可丢;超时 ⇒ 插件**有**声明而我们**没测出来**,声明被静默丢弃 ——
		// 其中 `CredentialReadDeny: true` 关系到凭据目录读拒(tool-basic 正是靠它
		// 在被内核包装后仍然开读拒),丢它等于**安全声明无声消失**。
		return Capabilities{}, false, errors.Is(ctx.Err(), context.DeadlineExceeded)
	}
	var c Capabilities
	if json.Unmarshal(bytes.TrimSpace(out), &c) != nil {
		return Capabilities{}, false, false
	}
	return c, true, false
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
		case reservedDataRootName(d):
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
func (b *Bridge) pluginSandboxSpec(dataWrites []string, credReadDeny bool) kernelsandbox.Spec {
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
	// 读拒绝的两个来源:插件自报「进程内会跑用户 shell 命令」(CredentialReadDeny)一声明即
	// 默认开(与 shell 的 GAH_SHELL_CRED_READ_KERNEL 默认一致),或用户显式点开关。
	// 为何插件侧要有这一条:shell 提供者被外层包装后,它自己施加时会被 MarkerEnv 跳过 →
	// 没有这条就等于把 F1 的凭据读拒静默失效(A6 的开关语义决策)。
	if credReadDeny || os.Getenv(pluginCredReadDenyEnv) == "1" {
		spec.ReadDeny = sdk.CredentialDenyDirs()
	}
	return spec
}

// wrapPluginArgv 组装插件的启动 argv 与额外环境(纯函数便于断言)。
//
// 第二个返回值 = 若施加了内核沙箱,要追加给**插件进程**的环境(打 MarkerEnv);
// 第三个返回值 = 是否真的施加了(供 extEntry 记录,决定要不要做档位陈旧检查)。
//
// 不再有「自报内核沙箱提供者 ⇒ 不包装」这条分支(2026-09-27 审计 A6):免嵌套由 MarkerEnv
// 承担 —— 被包装的插件进程内再调 Wrap 时看到标记直接返回 nil(不施加),不会出现
// `sandbox_apply: Operation not permitted`。于是 shell 提供者(tool-basic)也能被外层
// 包装,它的 in-process 直写(memory/todos/file 工具)一并进内核层。
// 自报的读拒绝经 Caps 传入(caps 零值 = 探测失败/未声明 ⇒ 默认关)。
func (b *Bridge) wrapPluginArgv(bin, role string, caps Capabilities, capsKnown bool, dataWrites []string) ([]string, []string, bool) {
	pre := kernelsandbox.Wrap(b.pluginSandboxSpec(dataWrites, capsKnown && caps.CredentialReadDeny))
	full := append([]string{bin}, roleArgs(role)...)
	if len(pre) == 0 {
		return full, nil, false
	}
	// 标记已在内核沙箱内:插件再起的子进程(它内部的 MCP server、子命令)不得也不能重复施加。
	return kernelsandbox.PrefixedArgv(pre, full[0], full[1:]...), []string{kernelsandbox.MarkerEnv + "=1"}, true
}

// roleArgs 角色 → argv 片段(空角色 = 无参,合并前的行为)。
func roleArgs(role string) []string {
	if role == "" {
		return nil
	}
	return []string{role}
}

// sandboxStale 判定被包装插件的**启动档位/根**是否已过期(阻断 ②)。
//
// 返回非空消息 = 已尝试重建但仍不可安全执行,调用方必须 fail-closed 拒绝本次调用。
// 比较两侧都取 b.sandboxModeRoot()(不是调用方 ctx 的 hint):profile 就是用它构建的,
// 同源比较才是同构的;命令类调用拿不到 ctx hint,同源也让工具/命令两条路径共用一份实现。
//
// 语义(2026-09-27 审计 A6 修正为「先重建再执行」):A3b 当时是“一律拒一次 + 异步重载”,
// 方向对(绝不拿旧 profile 继续跑)但代价错:`tool-basic` 一被包装,`/ws` 切根后的
// 第一次 shell/file 调用就必失败(e2e `TestExternalFileChangeLandsInLedger` 抓到)。
// 现在改为：档位/根变了 ⇒ **同步重建**(持 reloadMu 重建,并发的另一路会串行等完)、
// 就绪后本次调用照常执行。等不到(重建失败/条目被撤)才拒绝 —— 安全性不降(执行的
// 始终是新 profile)。
func (b *Bridge) sandboxStale(path, role string) (string, bool) {
	mode, root := b.sandboxModeRoot()
	if !b.wrappedStale(path, mode, root) {
		return "", false
	}
	if err := b.reloadForSandbox(path, role, mode, root); err != nil && !errors.Is(err, errPluginIdle) {
		b.logErr("host-bridge: 档位/根变更后的重载失败(条目已撤销,下次加载重试)", "path", path, "err", err)
	}
	if !b.wrappedStale(path, mode, root) {
		return "", false // 已按新档位重建:本次调用照常执行
	}
	b.mu.RLock()
	e := b.entries[entryKey(path, role)]
	b.mu.RUnlock()
	if e == nil {
		return "外部插件重建中或已撤销:请重新调用", true
	}
	return fmt.Sprintf("外部插件沙箱上下文已变更(档位 %s→%s,根 %s→%s):重建未落地,请重新调用",
		sbDesc(e.wrapMode), sbDesc(mode), sbDesc(e.wrapRoot), sbDesc(root)), true
}

// wrappedStale 该条目是否被包装、且启动档位/根与当前有效值不一致。
func (b *Bridge) wrappedStale(path, mode, root string) bool {
	b.mu.RLock()
	e, ok := b.entries[path]
	b.mu.RUnlock()
	return ok && e.wrapped && (mode != e.wrapMode || root != e.wrapRoot)
}

// reloadForSandbox 为档位/根变更做一次重建(持 reloadMu,重检后决定,避免并发调用各自重启一遍)。
// role:多角色二进制里重建要指明角色(一个文件四个角色,重建错角色 = 工具集体失踪)。
func (b *Bridge) reloadForSandbox(path, role, mode, root string) error {
	b.reloadMu.Lock()
	defer b.reloadMu.Unlock()
	if !b.wrappedStale(path, mode, root) {
		return nil // 别的调用已经重建好了
	}
	return b.reloadLocked(path, role)
}

// sbDesc 档位/根的日志展示(空 = 未注入,别打印成空串让人误以为"没有档位")。
func sbDesc(s string) string {
	if strings.TrimSpace(s) == "" {
		return "未注入"
	}
	return s
}
