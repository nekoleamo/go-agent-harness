// Command tool-basic 外部化基础工具进程(P1 方案 B):shell/files/web/memory
// 合一批二进制(共享运行时,体积与进程数最优),经桥协议(多工具)暴露。
// 宿主 host-bridge 扫描 tool-* 二进制 → Definitions 枚举 → 逐个注册。
package main

import (
	"fmt"
	"os"

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
	tools["web_search"] = toolweb.NewSearchTool(toolweb.NewExaProvider(nil)) // M6.14:默认 exa,EXA_API_KEY
	tools["memory"] = toolmemory.NewTool()                                   // M10:跨会话操作记忆
	tools["todo"] = tooltodo.NewTool()                                       // M8:任务清单(4 状态机/blockedBy)
	tools["session_search"] = toolsessionsearch.NewTool()                    // S-P2-5:跨会话检索(只读)
	bridge.ServeTools(tools)
}
