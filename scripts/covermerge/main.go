// Command covermerge 把子进程覆盖率 profile 并进主 profile(区块计数相加)。
//
// 用法:go run ./scripts/covermerge <主profile> <附加profile...> > <新profile>
//
//	(不给附加文件时原样输出主 profile;合并逻辑在 internal/testutil 里,这里只做壳)
//
// 为什么要有这个程序而不是在覆盖率门的 awk 里合并:合并要按键聚合求和,awk 写出来
// 既难读也没法单测;而覆盖率门只需要在聚合之前调它一次。
package main

import (
	"fmt"
	"os"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法:covermerge <主profile> [附加profile...]")
		os.Exit(2)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "covermerge: 读主 profile 失败:%v\n", err)
		os.Exit(1)
	}
	// mode: set 必须保留在第一行,先摘出来再合并。
	mode := ""
	rest := string(raw)
	if len(rest) > 5 && rest[:5] == "mode:" {
		if nl := indexByte(rest, '\n'); nl > 0 {
			mode = rest[:nl+1]
			rest = rest[nl+1:]
		}
	}
	out := []byte(rest)
	for _, extra := range os.Args[2:] {
		b, err := os.ReadFile(extra)
		if err != nil {
			fmt.Fprintf(os.Stderr, "covermerge: 读 %s 失败:%v\n", extra, err)
			os.Exit(1)
		}
		body := string(b)
		if len(body) > 5 && body[:5] == "mode:" {
			if nl := indexByte(body, '\n'); nl > 0 {
				body = body[nl+1:]
			}
		}
		out = testutil.MergeProfiles(out, []byte(body))
	}
	if mode != "" {
		os.Stdout.WriteString(mode)
	}
	os.Stdout.Write(out)
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
