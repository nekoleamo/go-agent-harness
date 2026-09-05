// Command tool-subagent 外部化子代理委派进程(M9.1):模型面向工具面,
// 引擎复用宿主 host-fanout——经宿主回调通道(GAH_CB_ADDR)请求 fanout.agent,
// 子代理在宿主独立上下文执行(仅结论回流父级),本进程仅转发委托(崩溃隔离)。
// 与 tool-workflow(starlark 程序化批量)共享同一 fanout seam;独立二进制,
// 不入 tool-basic(语义聚类 + 崩溃互不影响)。
package main

import (
	"fmt"
	"os"

	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-subagent"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func main() {
	if v, ok := os.LookupEnv("GAH_PLUGIN"); !ok || v != "gah-external-tool" {
		fmt.Fprintln(os.Stderr, "外部插件缺少握手标识 GAH_PLUGIN")
		os.Exit(1)
	}
	cbAddr := os.Getenv("GAH_CB_ADDR")
	if cbAddr == "" {
		fmt.Fprintln(os.Stderr, "tool-subagent: 缺少 GAH_CB_ADDR(宿主未开启回调通道)")
		os.Exit(1)
	}
	cc, err := bridge.DialCallback(cbAddr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "tool-subagent: 连接宿主回调失败:", err)
		os.Exit(1)
	}
	// delegate → 宿主 ctx.fanout.Agent(独立上下文,仅结论回流);CbFanout 封装回调。
	bridge.ServeTools(map[string]sdk.Tool{
		"subagent": toolsubagent.NewTool(bridge.CbFanout(cc)),
	})
}
