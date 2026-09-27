// Command tool-caps 测试夹具:**能力自报链路**的最小外部插件。
//
// 为什么放在 testdata(而不是像 tool-echo 那样进 extplugins/):
// 它只服务于 host-bridge 的协议测试,不是发行产物 —— 放 extplugins/ 会被
// scripts/gen-extplugins.sh 打进 embed 与体积门。testdata 被 go 工具链忽略
// (`./...` 不含),同时又能被 `go build` 出真二进制来跑真链路(不 mock RPC)。
//
// 声明的具体内容经环境变量切换,同一夹具覆盖多条分支:
//
//	FIXTURE_SANDBOX_PROVIDER=1  → 声明"我自己按调用施加内核沙箱"(宿主不得包裹)
//	FIXTURE_DATA_WRITES=a,b     → 声明数据根可写子目录(宿主校验后进白名单)
package main

import (
	"context"
	"os"
	"strings"

	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

type capsTool struct{}

func (capsTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "caps", Description: "能力自报夹具", InputSchema: map[string]any{"type": "object"}}
}

func (capsTool) Execute(ctx context.Context, args string) (any, error) {
	return "ok:" + args, nil
}

func main() {
	caps := bridge.Capabilities{}
	if os.Getenv("FIXTURE_SANDBOX_PROVIDER") == "1" {
		caps.SandboxProvider = true
	}
	if v := os.Getenv("FIXTURE_DATA_WRITES"); v != "" {
		caps.DataWrites = strings.Split(v, ",")
	}
	bridge.ServeToolsWith(map[string]sdk.Tool{"caps": capsTool{}}, caps)
}
