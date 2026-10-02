// Command tool-kit 外部工具进程(2026-10-02 瘦身:由四个二进制合并为一个)。
//
// 为什么合并(体积债,数字是实测的,darwin/arm64 原始产物):
//
//	tool-basic 8.74 + tool-workflow 7.10 + tool-mcp 8.58 + tool-subagent 6.35 = 30.77 MiB
//	合成一个 tool-kit                                                      =  9.62 MiB
//
// 省 69% 的原因很直白:四个二进制**各自静态链了一遍**Go 运行时 + host-bridge + sdk
// + 各自的工具集。合成后那段公共部分只出现一次。压缩后 embed 少约 7.9 MiB
// (门是 36 MiB,余量从 0.24 回到 8 MiB 上下)。
//
// **合成的是二进制,不是进程**:宿主仍按角色逐个起进程(`tool-kit tool-basic`、
// `tool-kit tool-mcp` …),所以:
//   - 崩溃隔离不丢(starlark 崩了不影响 MCP 连接);
//   - 配置文件 / 面板 / `/plugins off tool-basic` 里的 **id 一个都没变**;
//   - 每个进程只加载自己要用的那部分代码,不会因为「都在一个二进制里」而变慢。
//
// 角色自描述(`--roles`):宿主在扫到一个 `tool-*` 二进制时先问它「你提供哪些角色」,
// 拿到列表后按角色逐个起。**没有 `--roles`(或返回空)的二进制按老方式起一个无参进程**
// —— 第三方自己放进 plugins/ 的外部插件因此完全不受影响(向后兼容的唯一关键)。
package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// RolesFlag 角色自描述标志(宿主探测用;返回 JSON 字符串数组)。
const RolesFlag = "--roles"

// roles 本进程提供的角色(= 历史上的四个独立二进制名,也是配置文件里的插件 id)。
//
// **id 必须与历史一致**:面板显示、`/plugins off <id>`、配置文件条目都按它。
// 改名等于让所有用户的配置与开关失配。
var roles = map[string]func(){
	"tool-basic":    runBasic,
	"tool-workflow": runWorkflow,
	"tool-mcp":      runMCP,
	"tool-subagent": runSubagent,
}

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == RolesFlag {
		// 列表要**稳定**(宿主按它逐个起进程;顺序变了只是启动顺序变,但输出该可复现)
		out := []string{"tool-basic", "tool-mcp", "tool-subagent", "tool-workflow"}
		raw, err := json.Marshal(out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "tool-kit: 角色列表序列化失败:", err)
			os.Exit(1)
		}
		fmt.Println(string(raw))
		return
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "用法:tool-kit <角色> [参数...]\n"+
			"角色:%v\n(或 tool-kit %s 输出本进程提供的角色)\n", roleNames(), RolesFlag)
		os.Exit(2)
	}
	run, ok := roles[args[0]]
	if !ok {
		// 未知角色:**不静默跑成默认行为**。宿主传错角色时静默起一个「什么都干不了」的
		// 进程,症状是「工具全没了但日志干净」—— 显式退出更好查。
		fmt.Fprintf(os.Stderr, "tool-kit: 未知角色 %q(可用:%v)\n", args[0], roleNames())
		os.Exit(2)
	}
	// 角色函数内部自己读环境与配置;这里不吞参数(历史上的四个 main 也不读 os.Args[1:] 之外的)。
	run()
}

func roleNames() []string {
	out := make([]string, 0, len(roles))
	for k := range roles {
		out = append(out, k)
	}
	// 排序(免得每次输出顺序不同,让宿主日志看起来「随机」)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
