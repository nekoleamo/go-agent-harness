// Command tool-multirole **多角色外部插件测试夹具**(2026-10-02 瘦身配套)。
//
// 它模拟 tool-kit 的协议面:同一个二进制用 `--roles` 自报两个角色,按角色给出不同的
// 工具集。为什么要有它:合并二进制之后,「一个文件 ⇒ 多个进程、多个可分别开关的条目」
// 这套新语义原本**只在真机上看得见**(每个角色一个真进程);有了这个夹具,那套语义
// 能在 CI 里被钉住 —— 「角色之间互相影响」「按路径连错线」这两类只在合成后才出现的
// 缺陷,都是被它逮到的。
//
// 「自述空闲」也模拟了,而且**排在角色列表的第一个**(role-idle):只有空闲在前,
// 「空闲就 return」这个缺陷才会掐掉后面的健康角色 —— 用例才真钉得住(第一版把空闲
// 放最后,反向验证时用例根本没红,等于没钉)。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

const rolesFlag = "--roles"

func main() {
	args := os.Args[1:]
	// --roles **先于**握手检查:宿主的自描述探测不带 GAH_PLUGIN/凭据(与能力自报探测同纪律),
	// 若先查握手就会探测失败、退回单角色 —— 那正是本夹具要验的机制被自己弄坏。
	if len(args) > 0 && args[0] == rolesFlag {
		raw, _ := json.Marshal([]string{"role-idle", "role-one"})
		fmt.Println(string(raw))
		return
	}
	if v, ok := os.LookupEnv("GAH_PLUGIN"); !ok || v != "gah-external-tool" {
		fmt.Fprintln(os.Stderr, "外部插件缺少握手标识 GAH_PLUGIN")
		os.Exit(1)
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法:tool-multirole <角色>")
		os.Exit(2)
	}
	switch args[0] {
	case "role-idle":
		// 第一个角色:恒自述空闲(验「空闲不牵连后面的兄弟角色」)。
		// 判据用 GAH_MULTI_IDLE 这个**宿主不注入**的开关:不能用 GAH_CB_ADDR ——
		// 宿主会给每个角色都注入它,拿它当空闲判据的话这条分支永远不成立
		// (第一版就栽在这,症状是「空闲用例莫名其妙红了」)。
		if os.Getenv("GAH_MULTI_IDLE") == "" {
			fmt.Fprintln(os.Stderr, bridge.IdleMarker+"本角色需要回调通道,跳过")
			os.Exit(0)
		}
		bridge.ServeTools(map[string]sdk.Tool{"multi_idle": &echoTool{tag: "idle"}})
	case "role-one":
		bridge.ServeTools(map[string]sdk.Tool{"multi_one": &echoTool{tag: "one"}})
	case "role-two":
		bridge.ServeTools(map[string]sdk.Tool{"multi_two": &echoTool{tag: "two"}})
	default:
		fmt.Fprintf(os.Stderr, "tool-multirole: 未知角色 %q\n", args[0])
		os.Exit(2)
	}
}

// echoTool 回显 text 并带上自己的角色标签(用来证明调用确实落到了对应角色的进程)。
type echoTool struct{ tag string }

func (e *echoTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "multi_" + e.tag,
		Description: "多角色夹具工具(" + e.tag + ")",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
	}
}

func (e *echoTool) Execute(_ context.Context, raw string) (any, error) {
	var a struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(raw), &a)
	return map[string]any{"text": strings.TrimSpace(a.Text + "@" + e.tag)}, nil
}
