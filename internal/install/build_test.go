// 构建收口的三条单测(2026-10-03):默认不 tidy、失败才 tidy、构建环境不含凭据。
//
// 这三条钉的都是**静默失效**:旧默认无条件 `go mod tidy`(悄悄改 go.mod、悄悄扩供应链面)、
// 旧实现不设 cmd.Env(插件的构建脚本能把你的 API key 读走)。
package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// —— ① 默认不 tidy,失败才 tidy ——

// TestDefaultBuildCmdIsReadonly 默认构建命令必须显式 readonly,且**不含** tidy。
func TestDefaultBuildCmdIsReadonly(t *testing.T) {
	cmd := defaultBuildCmd("tool-demo")
	if !strings.Contains(cmd, "-mod=readonly") {
		t.Fatalf("默认构建必须是 readonly(不写 go.mod/go.sum): %q", cmd)
	}
	if strings.Contains(cmd, "tidy") {
		t.Fatalf("默认构建不该含 tidy(它在需要时才补跑): %q", cmd)
	}
}

// TestBuildPluginDefaultNoTidyWhenComplete go.mod/go.sum 完整时**一次 tidy 都不该跑**。
//
// 用一个假的 `go` 桩来判:"tidy 被调用过"就写一个标记文件。
func TestBuildPluginDefaultNoTidyWhenComplete(t *testing.T) {
	dir := t.TempDir()
	stub := stubGo(t, dir, true) // 桩:true = 立刻构建成功
	res, err := buildPlugin(dir, defaultBuildCmd("tool-demo"), "tool-demo")
	if err != nil {
		t.Fatalf("构建应成功: %v", err)
	}
	if res.Tidied {
		t.Fatalf("go.mod 完整时不该补依赖: %+v", res)
	}
	if stub.called("tidy") {
		t.Fatal("默认路径不该执行 go mod tidy")
	}
}

// TestBuildPluginTidiesOnlyWhenNeeded go.mod 不完整时:补一次依赖再重试,并如实标记。
//
// 标记是重点 —— 补依赖意味着「装这个插件往供应链面里加了仓库没声明的模块」,
// 而旧实现在默认命令里悄悄做了这件事,没人知道。
func TestBuildPluginTidiesOnlyWhenNeeded(t *testing.T) {
	dir := t.TempDir()
	stub := stubGo(t, dir, false) // 桩:false = 第一次构建失败(缺依赖),tidy 后成功
	res, err := buildPlugin(dir, defaultBuildCmd("tool-demo"), "tool-demo")
	if err != nil {
		t.Fatalf("补依赖后应构建成功: %v", err)
	}
	if !res.Tidied {
		t.Fatal("补过依赖必须被标记(它意味着引入了仓库未声明的模块)")
	}
	if !stub.called("tidy") {
		t.Fatal("缺依赖时应该补跑一次 tidy")
	}
	if !strings.Contains(res.Cmd, "tidy") {
		t.Fatalf("回执里要看得见补过依赖: %q", res.Cmd)
	}
}

// TestBuildPluginAuthorCommandNotInterfered 作者自定义了 build:失败就是失败,不由我们插手。
//
// 插手会做出作者没要求的事(改他的 go.mod),而且报错会指向一个他没写的命令。
func TestBuildPluginAuthorCommandNotInterfered(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "plugin.yaml"), "id: demo\nbuild: exit 7\n")
	stub := stubGo(t, dir, false)
	_, err := buildPlugin(dir, "exit 7", "tool-demo")
	if err == nil {
		t.Fatal("作者命令失败应原样报错")
	}
	if stub.called("tidy") {
		t.Fatal("作者自定义 build 时不该插手跑 tidy")
	}
}

// —— ② 构建环境清洗 ——

// TestBuildEnvDropsCredentials 构建环境里不能有凭据类键。
func TestBuildEnvDropsCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	t.Setenv("GITHUB_TOKEN", "gh-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-secret")
	t.Setenv("GAH_CB_TOKEN", "cb-secret")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/home/x")

	has := map[string]bool{}
	for _, kv := range buildEnv() {
		k, _, _ := strings.Cut(kv, "=")
		has[strings.ToUpper(k)] = true
	}
	for _, k := range []string{"OPENAI_API_KEY", "GITHUB_TOKEN", "AWS_SECRET_ACCESS_KEY", "GAH_CB_TOKEN"} {
		if has[k] {
			t.Fatalf("%s 不应下发到构建进程(插件的构建脚本能读走它)", k)
		}
	}
	// 基础键保留:构建要 PATH/HOME
	for _, k := range []string{"PATH", "HOME"} {
		if !has[k] {
			t.Fatalf("基础键 %s 应保留,否则构建跑不起来", k)
		}
	}
}

// TestBuildEnvOverridesHostSwitches 宿主的 GOFLAGS/GO111MODULE 不得改变这次安装的构建语义。
//
// 用户机器上恰好有 GOFLAGS=-mod=vendor 时,一次"只读构建"的结果会完全不同,
// 而用户完全不知情 —— 那是宿主的意外设置决定了产物。
func TestBuildEnvOverridesHostSwitches(t *testing.T) {
	t.Setenv("GOFLAGS", "-mod=vendor")
	t.Setenv("GO111MODULE", "off")
	t.Setenv("PATH", "/usr/bin")

	env := buildEnv("GOFLAGS=-mod=readonly")
	var flags []string
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == "GOFLAGS" {
			flags = append(flags, v)
		}
	}
	if len(flags) != 1 || flags[0] != "-mod=readonly" {
		t.Fatalf("GOFLAGS 应只留我们设定的那一个,实际 %v", flags)
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "GO111MODULE=") {
			t.Fatalf("GO111MODULE 应被去掉: %q", kv)
		}
	}
}

// stubGo 造一个假的 `go` 桩放进 PATH,记录它被以什么子命令调用过。
// okFirst=false 时:第一次 build 返回非零(模拟缺依赖),tidy 之后成功。
//
// 注意 `go mod tidy` 传给 go 的是 **`$1=mod` `$2=tidy`**(不是 `$1=tidy`)——
// 这个细节曾经让桩的 case 分支永远不匹配,表现为"tidy 明明跑了但标记没建"。
func stubGo(t *testing.T, dir string, okFirst bool) *goStub {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("桩脚本是 sh,Windows 上不跑(该路径的真实构建由真机验)")
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "go-calls")
	done := filepath.Join(dir, "tidy-done")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + marker + "\n" +
		"case \"$1\" in\n" +
		"  mod) [ \"$2\" = tidy ] && { touch " + done + "; exit 0; }; exit 1 ;;\n" +
		"  build) if [ -f " + done + " ]; then exit 0; fi\n" +
		"         if [ '" + boolStr(okFirst) + "' = 1 ]; then exit 0; fi\n" +
		"         echo 'no required module provides package x; to add it: go mod tidy' >&2; exit 1 ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &goStub{marker: marker}
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

type goStub struct{ marker string }

func (g *goStub) called(sub string) bool {
	raw, err := os.ReadFile(g.marker)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}
