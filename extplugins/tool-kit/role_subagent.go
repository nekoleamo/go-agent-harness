// 本文件:toolkit 二进制里的 tool-subagent 角色。
//
// 2026-10-02 瘦身:原来 tool-basic / tool-workflow / tool-mcp / tool-subagent 是**四个**
// 独立二进制,各自静态链一遍运行时 + host-bridge + sdk —— 实测四份合计 30.8 MiB,
// 而合成一个只要 9.6 MiB(省 69%)。合成的是**二进制**,不是**进程**:宿主仍按角色逐个
// 起进程(崩溃隔离不丢),配置文件里的插件 id 一个都没变。
//
// 自合并以来本文件内容**逐字未改**,只把 main() 改成由 toolkit 分派调用。
package main

import (
	"fmt"
	"os"

	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/plugins/tool/tool-subagent"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 由 tool-kit 的分派调用(见 main.go)。
func runSubagent() {
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
