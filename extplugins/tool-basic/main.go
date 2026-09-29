// Command tool-basic 外部化基础工具进程(P1 方案 B):shell/files/web/memory
// 合一批二进制(共享运行时,体积与进程数最优),经桥协议(多工具)暴露。
// 宿主 host-bridge 扫描 tool-* 二进制 → Definitions 枚举 → 逐个注册。
package main

import (
	"fmt"
	"os"

	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-files"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-memory"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-session-search"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-shell"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-todo"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-web"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func main() {
	// pty 开关经环境变量透传(bundle data 无法透传子进程,外部化按需自配)
	pty := os.Getenv("GAH_TOOL_SHELL_PTY") == "1"
	tools := map[string]sdk.Tool{
		"shell": toolshell.NewTool(pty),
	}
	// 写盘审计回传(S-P1-1):外部进程没有宿主 Ctx,file/change 事件必须经桥交宿主落账,
	// 否则默认发行态(工具已外部化)的变更视图/`/diff` 恒为空。回调通道不可用时退回
	// 「无审计」并在 stderr 明示(不静默):工具本身照常可用。
	var rec sdk.FileChangeRecorder
	if addr := os.Getenv("GAH_CB_ADDR"); addr != "" {
		if cc, err := bridge.DialCallback(addr); err == nil {
			rec = bridge.CbChanges(cc)
		} else {
			fmt.Fprintln(os.Stderr, "tool-basic: 回调通道连接失败,文件改动不会入账:", err)
		}
	} else {
		fmt.Fprintln(os.Stderr, "tool-basic: 缺 GAH_CB_ADDR(宿主未开启回调通道),文件改动不会入账")
	}
	for name, t := range toolfiles.NewToolsWith(rec) {
		tools[name] = t
	}
	tools["web_fetch"] = toolweb.NewTool()
	// M6.14/第八十五批:provider 取生效配置(env GAH_SEARCH_PROVIDER > 搜索配置文件,缺省 exa);
	// 未知名不拖垮本插件(shell/文件/记忆/todo 在同进程)。
	tools["web_search"] = toolweb.NewSearchToolFromEnv(nil)
	tools["memory"] = toolmemory.NewTool()                // M10:跨会话操作记忆
	tools["todo"] = tooltodo.NewTool()                    // M8:任务清单(4 状态机/blockedBy)
	tools["session_search"] = toolsessionsearch.NewTool() // S-P2-5:跨会话检索(只读)
	// 能力自报(2026-09-27 审计 A3/A6):本插件是 **shell 提供者**。
	// 宿主会整体包装本进程(in-process 直写因此进内核层);本插件内部的 shell/pty 再施加时由
	// MarkerEnv 自动跳过(不可嵌套由标记解决,不再需要宿主豁免) —— 见 internal/kernelsandbox。
	// CredentialReadDeny 声明:shell 提供者被外层包装后自己那份凭据读拒不生效,靠这条延续
	// (默认关的 GAH_EXT_PLUGIN_CRED_READ_DENY 不管默认档)。
	// DataWrites 是数据根写声明:本插件进程内直写 $GAH_HOME/{memory,todos}
	// (file 工具另走协作层路径裁决)。
	// ConfigEnv(第八十五批):本插件带 web_search,它的 provider/key/endpoint 存在
	// $GAH_HOME/config/search.yaml 里 —— 而上面那条 CredentialReadDeny 使**本进程读不到**
	// config/(内核凭据读拒),于是宿主按本声明代读文件并注入进程 env(进程 env 是模型经
	// shell 拿不到的唯一通道:sdk.SanitizedEnv 会滤掉 *_API_KEY)。
	tools["web_search"] = toolweb.NewSearchToolFromEnv(nil)
	bridge.ServeToolsWith(tools, bridge.Capabilities{
		CredentialReadDeny: true,
		DataWrites:         []string{"memory", "todos"},
		ConfigEnv:          []string{searchfile.EnvAPIKey, searchfile.EnvProvider, searchfile.EnvEndpoint},
	})
}
