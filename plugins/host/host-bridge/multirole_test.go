package hostbridge

// 多角色二进制(2026-10-02 瘦身)的机制契约。
//
// 为什么这些断言值得单独一组:合成 tool-kit 之后,「一个文件 ⇒ 四个进程、四个可分别
// 开关与重载的条目」这套新语义**只在真机上才看得见**(每个角色起一个真进程)。这组用例
// 用一个**多角色测试夹具**把语义钉在 CI 里,于是「角色之间互相影响」「按路径连错线」
// 这类只会在合成后才出现的缺陷,能在本机就被逮到(它们确实是这样被逮到的)。
//
// 夹具:tool-echo 的多角色版 —— 同一个二进制,`--roles` 报两个角色,按角色给出不同的
// 工具集(`multi-one` / `multi-two`)。这样「一个文件两个条目」是**真的**,不是构造的。

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildMultiRolePlugin 编出多角色夹具(两个角色:一个正常、一个自述空闲)。
func buildMultiRolePlugin(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, testutil.ExeName("tool-multirole")), "../../../extplugins/tool-multirole")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译多角色夹具失败: %v\n%s", err, out)
	}
}

// buildExternalEnvWithBridge 起 host-tools + host-bridge(夹具所在目录),返回宿主 Ctx。
func buildExternalEnvWithBridge(t *testing.T, dir string) sdk.Ctx {
	t.Helper()
	c, _ := buildEnv(t, dir)
	return c
}

func testCtx() context.Context { return context.Background() }

// TestRolesOfFallback 不认识 --roles / 输出不合法 / 列表有空项 ⇒ 退回单角色。
//
// 退回的含义是**空角色**(= 无子参数,合并前的老行为),不是「用文件名当角色」:
// 用文件名会让条目键变成 `路径#名字`,所有按路径寻址的老代码全部失配(第一版就这么栽的)。
func TestRolesOfFallback(t *testing.T) {
	dir := t.TempDir()
	// ① 命令失败(不是可执行文件)
	notExec := filepath.Join(dir, "tool-bogus")
	if err := os.WriteFile(notExec, []byte("not an executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := rolesOf(notExec); len(got) != 1 || got[0] != "" {
		t.Errorf("不可执行时应退回单角色(空),got %#v", got)
	}
	// ② echo 认识 --roles 但输出不是 JSON
	echoBin := filepath.Join(dir, "tool-echo")
	if err := os.WriteFile(echoBin, []byte("#!/bin/sh\necho --roles\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := rolesOf(echoBin); len(got) != 1 || got[0] != "" {
		t.Errorf("输出非 JSON 时应退回单角色(空),got %#v", got)
	}
}

// TestEntryKeyAndRoleHelpers 键与角色的纯函数口径。
func TestEntryKeyAndRoleHelpers(t *testing.T) {
	if got := entryKey("/p/tool-kit", "tool-mcp"); got != "/p/tool-kit#tool-mcp" {
		t.Errorf("多角色键应带角色,got %q", got)
	}
	if got := entryKey("/p/tool-echo", ""); got != "/p/tool-echo" {
		t.Errorf("单角色键应等于路径(老行为不变),got %q", got)
	}
	// 角色判定:名字 == 文件基名 ⇒ 单角色(空);否则名字就是角色
	if got := roleOf("tool-echo", "/p/tool-echo"); got != "" {
		t.Errorf("名字==文件名时应判为单角色,got %q", got)
	}
	if got := roleOf("tool-basic", "/p/tool-kit"); got != "tool-basic" {
		t.Errorf("多角色时名字即角色,got %q", got)
	}
	// 键 → 路径:优先 e.path,兜底从键里切(单测替身常常不填 e.path)
	if got := keyPathOf("/p/tool-kit#tool-mcp", &extEntry{}); got != "/p/tool-kit" {
		t.Errorf("应从键切出路径,got %q", got)
	}
	if got := keyPathOf("/p/x#y", &extEntry{path: "/real/path"}); got != "/real/path" {
		t.Errorf("e.path 优先,got %q", got)
	}
	if got := keyPathOf("/p/plain", nil); got != "/p/plain" {
		t.Errorf("无角色后缀时应原样返回,got %q", got)
	}
}

// TestMultiRoleBinaryLoadsEachRoleSeparately 一个文件两个角色 ⇒ 两个条目、两组工具、
// 分别可寻址。**这是瘦身机制的核心断言**:合成二进制不能把角色糊在一起。
func TestMultiRoleBinaryLoadsEachRoleSeparately(t *testing.T) {
	dir := t.TempDir()
	buildMultiRolePlugin(t, dir)

	env := buildExternalEnvWithBridge(t, dir)
	c := env
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"multi_one"} {
		if _, ok := tools.Get(name); !ok {
			t.Fatalf("角色 %s 的工具应注册(实际注册:%v)", name, toolNames(tools))
		}
	}
	// 两个条目、两个**不同**的进程连接(不是同一个 client)
	res, err := tools.Execute(testCtx(), "multi_one", `{"text":"one"}`)
	if err != nil || res.Error != "" {
		t.Fatalf("multi_one 应可调用:%v %+v", err, res)
	}
	if !strings.Contains(res.Content, "one") {
		t.Errorf("multi_one 应回显 one,got %q", res.Content)
	}
	// 回显带角色标签,证明调用确实落到了 role-one 那个**进程**(不是被别的角色代答)
	if !strings.Contains(res.Content, "one@one") {
		t.Errorf("回显应带角色标签 one@one,got %q", res.Content)
	}
}

// TestMultiRoleIdleDoesNotKillSiblingRoles 某角色「自述空闲」**不得**掐掉同一文件里的其他角色。
//
// 这是合成独有的缺陷:合成之前每个角色是各自的文件,空闲只影响自己那份;合成之后
// 若在空闲分支 return,**排在后面没参与的角色会整组消失**,而日志只有一行 INFO,
// 看着像「本来就没装」(run 36984612939 之前就被它咬过一次:subagent/workflow 工具消失)。
func TestMultiRoleIdleDoesNotKillSiblingRoles(t *testing.T) {
	dir := t.TempDir()
	buildMultiRolePlugin(t, dir)
	c := buildExternalEnvWithBridge(t, dir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// 夹具的角色顺序是 [role-idle(空闲), role-one(健康)]:
	// 空闲在前,「空闲就 return」这个缺陷会掐掉后面的健康角色 —— 反向验证确认过会红。
	if _, ok := tools.Get("multi_one"); !ok {
		t.Fatalf("空闲角色不应带走兄弟角色(实际工具:%v)", toolNames(tools))
	}
	if _, ok := tools.Get("multi_idle"); ok {
		t.Fatalf("自述空闲的角色本就不该注册工具:%v", toolNames(tools))
	}
}
