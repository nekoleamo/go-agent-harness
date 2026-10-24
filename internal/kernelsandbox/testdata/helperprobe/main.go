// Command helperprobe 覆盖记账探针(只给 internal/kernelsandbox 的 Linux 测试用)。
//
// 它做一件很小的事:按参数把目标命令包进内核沙箱后跑起来。
// 为什么要一个**独立的小二进制**(而不是直接在测试里起命令):
// `runtime/coverage.WriteCountersDir` 在 `go test` 生成的测试二进制里**恒失败**
// (报 `-covermode=<invalid>`),所以 helper(被 `syscall.Exec` 换掉的那个进程)只有在
// 它是 `go build -cover -covermode=atomic` **产物**时才写得进计数器 —— 这个探针就是
// 那个产物(`testutil.BuildCovered` 构建)。没有这一步,记账修没修好永远无从验证。
//
// 用法:helperprobe <mode> <root> <jail> <script>
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/nekoleamo/go-agent-harness/internal/kernelsandbox"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func main() {
	if len(os.Args) < 5 {
		fmt.Fprintln(os.Stderr, "用法:helperprobe <mode> <root> <jail> <script>")
		os.Exit(2)
	}
	mode, root, jail, script := os.Args[1], os.Args[2], os.Args[3], os.Args[4]
	pre := kernelsandbox.Wrap(kernelsandbox.Spec{
		Mode:  sdk.SandboxMode(mode),
		Root:  root,
		Jail:  jail,
		Label: "helperprobe",
	})
	if len(pre) == 0 {
		fmt.Println("unwrapped") // 平台无能力/档位不适用:让测试据此 Skip 而不是误判
		return
	}
	argv := kernelsandbox.PrefixedArgv(pre, "/bin/sh", "-c", script)
	// 用显式 Cmd(而不是 exec.Command):argv 已经是完整包装后的形式,Command 会再拼一次。
	cmd := &exec.Cmd{Path: argv[0], Args: argv, Env: os.Environ()}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("child-exit:%v\n", err)
		os.Exit(1)
	}
	fmt.Println("child-ok")
}
