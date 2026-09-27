// A3(2026-09-27 安全审计)单测:MCP server 的内核级沙箱包装。
//
// 为什么必须有:MCP server 是**第三方代码**,而协作层的路径裁决只看得到经 mcp_* 工具传入的参数
// —— server 自己选定的写落点(DB/缓存/临时文件)完全在裁决面之外。这里钉的是"包装是否按
// 档位/开关/标记正确组装",真正的"内核拦得住"由 spike 与 tests/ 的 e2e 覆盖。
package mcpbridge

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/kernelsandbox"
)

// sandboxEnv 准备一个"宿主注入了 workspace 档位"的插件进程环境(模拟 host-bridge 的注入)。
func sandboxEnv(t *testing.T, mode, root string) {
	t.Helper()
	t.Setenv("GAH_EXT_SANDBOX_MODE", mode)
	t.Setenv("GAH_EXT_SANDBOX_ROOT", root)
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv(kernelsandbox.MarkerEnv, "")
}

func TestMCPArgvWrapsServerBySandboxMode(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("仅 macOS(seatbelt)/Linux(Landlock)有内核级能力")
	}
	sandboxEnv(t, "workspace-write", t.TempDir())

	argv, wrapped := mcpArgv("npx", []string{"-y", "@scope/server"})
	if !wrapped {
		t.Fatalf("workspace 档下应施加内核沙箱: %v", argv)
	}
	if runtime.GOOS == "darwin" {
		if !strings.HasSuffix(argv[0], "sandbox-exec") || argv[1] != "-p" {
			t.Fatalf("darwin 应以 seatbelt 前端包装: %v", argv)
		}
		prof := argv[2]
		if !strings.Contains(prof, "(deny file-write*)") || !strings.Contains(prof, "(allow default)") {
			t.Fatalf("profile 语义不符(读放行 + 写默认拒): %s", prof)
		}
		// 额外可写白名单:包管理器缓存必须有,否则 npx 类 server 直接起不来(spike 实证)
		if !strings.Contains(prof, "/.npm") {
			t.Fatalf("profile 应放行包管理器缓存: %s", prof)
		}
		// 凭据读拒绝默认**关**(读凭据是 server 的正当职责)
		if strings.Contains(prof, "deny file-read*") {
			t.Fatalf("默认不应带凭据读拒绝: %s", prof)
		}
	} else {
		if len(argv) < 7 || argv[1] != "--gah-landlock-exec" {
			t.Fatalf("linux 应以自举 helper 包装: %v", argv)
		}
	}
	// 真实命令仍在末尾(包装只前置)
	tail := argv[len(argv)-3:]
	if tail[0] != "npx" || tail[1] != "-y" || tail[2] != "@scope/server" {
		t.Fatalf("真实命令应保持原样: %v", argv)
	}
}

func TestMCPArgvRespectsGates(t *testing.T) {
	root := t.TempDir()
	cmd := "echo"

	// ① 档位未知(旧宿主/未注入):不施加
	sandboxEnv(t, "", root)
	if argv, wrapped := mcpArgv(cmd, nil); wrapped || argv[0] != cmd {
		t.Fatalf("未注入档位不应施加: %v", argv)
	}
	// ② full-access:不施加
	sandboxEnv(t, "full-access", root)
	if _, wrapped := mcpArgv(cmd, nil); wrapped {
		t.Fatal("全权档不应施加")
	}
	// ③ 显式关闭开关:不施加
	sandboxEnv(t, "workspace-write", root)
	t.Setenv(extKernelSandboxEnv, "0")
	if _, wrapped := mcpArgv(cmd, nil); wrapped {
		t.Fatal("GAH_EXT_KERNEL_SANDBOX=0 应不施加")
	}
	t.Setenv(extKernelSandboxEnv, "1")
	// ④ 已在内核沙箱内(祖先施加):不重复施加 —— seatbelt/Landlock 不可嵌套
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		t.Setenv(kernelsandbox.MarkerEnv, "1")
		if _, wrapped := mcpArgv(cmd, nil); wrapped {
			t.Fatal("已标记时不应重复施加(不可嵌套)")
		}
		t.Setenv(kernelsandbox.MarkerEnv, "")
	}
	// ⑤ 凭据读拒绝:显式开时才带上
	if runtime.GOOS == "darwin" {
		t.Setenv(extCredReadDenyEnv, "1")
		argv, wrapped := mcpArgv(cmd, nil)
		if !wrapped || !strings.Contains(argv[2], "deny file-read*") {
			t.Fatalf("GAH_EXT_CRED_READ_DENY=1 应带凭据读拒绝: %v", argv)
		}
	}
}

func TestKernelSpecRWPathsFromEnv(t *testing.T) {
	extra := t.TempDir()
	sandboxEnv(t, "read-only", t.TempDir())
	t.Setenv(extRWPathsEnv, extra+":"+t.TempDir())
	spec := kernelSpec()
	joined := strings.Join(spec.RW, "\n")
	if !strings.Contains(joined, extra) {
		t.Fatalf("GAH_EXT_RW_PATHS 应进白名单: %v", spec.RW)
	}
	if spec.Jail != os.Getenv("GAH_HOME")+"/jail" {
		t.Fatalf("白名单锚点应是数据根下的 jail: %s", spec.Jail)
	}
}
