// Command tool-caps 测试夹具:**能力自报链路**的最小外部插件。
//
// 为什么放在 testdata(而不是像 tool-echo 那样进 extplugins/):
// 它只服务于 host-bridge 的协议测试,不是发行产物 —— 放 extplugins/ 会被
// scripts/gen-extplugins.sh 打进 embed 与体积门。testdata 被 go 工具链忽略
// (`./...` 不含),同时又能被 `go build` 出真二进制来跑真链路(不 mock RPC)。
//
// 声明的具体内容经环境变量切换,同一夹具覆盖多条分支:
//
//	FIXTURE_CRED_READ_DENY=1   → 声明"本插件进程内会跑用户 shell 命令"(宿主默认开凭据读拒)
//	FIXTURE_DATA_WRITES=a,b     → 声明数据根可写子目录(宿主校验后进白名单)
package main

import (
	"context"
	"errors"
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

// capsWriteTool 在**插件进程自身**里写 FIXTURE_WRITE_PATH:用来证明外层包装真的落在进程上
// (不是靠 shell 子进程那一层 —— 它有自己的沙箱)。
type capsWriteTool struct{}

func (capsWriteTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "caps_write", Description: "内核沙箱探针:写 FIXTURE_WRITE_PATH", InputSchema: map[string]any{"type": "object"}}
}

func (capsWriteTool) Execute(context.Context, string) (any, error) {
	p := os.Getenv("FIXTURE_WRITE_PATH")
	if p == "" {
		return nil, errors.New("缺 FIXTURE_WRITE_PATH")
	}
	if err := os.WriteFile(p, []byte("probe"), 0o600); err != nil {
		return "err:" + err.Error(), nil
	}
	return "wrote:" + p, nil
}

func main() {
	caps := bridge.Capabilities{}
	if os.Getenv("FIXTURE_CRED_READ_DENY") == "1" {
		caps.CredentialReadDeny = true
	}
	if v := os.Getenv("FIXTURE_DATA_WRITES"); v != "" {
		caps.DataWrites = strings.Split(v, ",")
	}
	bridge.ServeToolsWith(map[string]sdk.Tool{"caps": capsTool{}, "caps_write": capsWriteTool{}}, caps)
}
