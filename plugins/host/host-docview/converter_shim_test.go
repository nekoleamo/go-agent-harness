package hostdocview

// 假外部转换器(shim):真 exec 契约(PATH 探测 → 真进程 → argv → 退出码 → 产物发现)必须在
// **跨平台同一份实现**下覆盖。此前用 `#!/bin/sh` 脚本当假 soffice,Windows 上只能 skip:
// ① `exec.LookPath` 按 PATHEXT 解析,无扩展名脚本永远命中不了;② 换 cmd 版就得再写一套参数
// 解析与日志格式,断言失去同构意义。
//
// 现在改为「本测试二进制复制成 soffice」:测试二进制的 TestMain 先看自己是不是被当成 shim
// 执行(按文件名判定),是就直接扮演转换器并退出,不跑任何测试。行为(日志落点/退出码/stderr/
// 是否产出)写在**二进制同目录的 shim.json** —— 不走环境变量,因为 `runExternal` 用
// `sdk.SanitizedChildEnv()` 清洗子进程环境,env 根本传不进被 exec 的 shim。

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const shimSpecName = "shim.json"

// shimSpec 假 soffice 的一次性行为(由 shim 从自己所在目录读取)。
type shimSpec struct {
	Log         string `json:"log"`          // argv 日志落点(参数空格连接 + 换行)
	Exit        int    `json:"exit"`         // 退出码
	Stderr      string `json:"stderr"`       // 非空则写进 stderr(测失败原因透出)
	SkipProduce bool   `json:"skip_produce"` // true = 不产出 PDF(测"退出 0 但无产物")
}

// TestMain 双身份:正常跑测试;被当作假 soffice/pdftoppm exec 时扮演转换器。
func TestMain(m *testing.M) {
	if exe, err := os.Executable(); err == nil && isShimName(filepath.Base(exe)) {
		os.Exit(shimMain(exe))
	}
	os.Exit(m.Run())
}

// isShimName 判定"当前二进制是不是被当假转换器跑"(只看文件名:复制品叫 soffice/soffice.exe)。
func isShimName(base string) bool {
	return strings.TrimSuffix(base, ".exe") == "soffice"
}

// shimMain 假 soffice 的行为:写 argv 日志 → 按 `--outdir` 与末尾参数产出同名 PDF → 退出。
// 任何异常一律非 0 退出 —— **绝不落回 m.Run()**,否则复制品会把整个测试包再跑一遍(递归)。
func shimMain(exe string) int {
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(exe), shimSpecName))
	if err != nil {
		return 9
	}
	var sp shimSpec
	if err := json.Unmarshal(raw, &sp); err != nil {
		return 9
	}
	args := os.Args[1:] // argv[0] 是 shim 自身路径
	if sp.Log != "" {
		if f, err := os.OpenFile(sp.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = fmt.Fprintln(f, strings.Join(args, " "))
			_ = f.Close()
		}
	}
	if !sp.SkipProduce {
		out, in := "", ""
		for i := 0; i < len(args); i++ {
			if args[i] == "--outdir" && i+1 < len(args) {
				out = args[i+1]
			}
			in = args[i]
		}
		if out != "" && in != "" {
			base := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in)) + ".pdf"
			_ = os.WriteFile(filepath.Join(out, base), []byte("%PDF-1.4\n"), 0o644)
		}
	}
	if sp.Stderr != "" {
		_, _ = fmt.Fprint(os.Stderr, sp.Stderr)
	}
	return sp.Exit
}

// installSofficeShim 把本测试二进制复制成 `dir/soffice`(`.exe` on Windows)。
// 与真实外部进程同构:真 exec、真 argv、真退出码、真产物,且 Windows 上不需要另写脚本语义。
func installSofficeShim(t *testing.T, dir string) {
	t.Helper()
	src, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "soffice"
	if runtime.GOOS == "windows" {
		name += ".exe" // LookPath 按 PATHEXT 解析:没有这个后缀就永远命中不了
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeShimSpec 更新假 soffice 的行为(同一二进制读同目录 shim.json;换行为只改数据,不改二进制)。
func writeShimSpec(t *testing.T, dir string, sp shimSpec) {
	t.Helper()
	raw, err := json.Marshal(sp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, shimSpecName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
