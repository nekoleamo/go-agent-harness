// A 组新增工具(F1 文件操作 / F4 图像)的**端到端写目标裁决**。
//
// 为什么单测不够、必须有这一层:
//   - `tool-files`/`tool-image` 的包内单测覆盖的是**工具自己**的 resolve();
//   - `policy-guard` 的用例覆盖的是**裁决表**(CheckPathArgs / builtinPathParams);
//   - 两者之间还有一段**胶水**:host-tools 的 pre-execute 要拿到工具的
//     `ToolDefinition`(含 PathParams)并交给裁决层。
//     这段胶水断了,两侧用例都还是绿的,而"写目标裁决"这条红线就形同虚设。
//
// 这里走的是真实装配(注册表 + host-tools + policy-guard)与真实执行入口(tools.Execute),
// 断的是"工作区外的写目标被拦、工作区内的放行"。
//
// 它同时是**纵深防御的探针**(2026-10-11 实测):只把宿主侧 CheckToolCallAt 改成直接 return nil,
// 本用例**仍然绿** —— 说明工具自身那次 resolve() 真的在拦(两层都有效);把两层都关掉才转红。
package tests

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildNewToolsEnv 装配 base bundle + 启用 F1/F4 两个工具(workspace-write 档)。
// 工作区根 = 进程 cwd(policy-guard 的 workspaceRoot()),故调用方先 t.Chdir(ws)。
func buildNewToolsEnv(t *testing.T) sdk.Ctx {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-tools"},
		{ID: "host-llm"},
		{ID: "policy-guard", Data: map[string]any{"approval": "open", "sandbox": "workspace-write", "sync": false}},
		{ID: "tool-files"},
		{ID: "tool-image"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c
}

// execNewTool 走宿主执行入口(含 pre-execute 裁决),返回"这次调用是否被拦下"的理由。
//
// **不能只看 res.Error**:批量工具(file_delete / image_ops)是"整体成功 + 逐条 error",
// 被拒的那一条错误在 JSON 体内,顶层 Error 是空的 —— 只看它会把"逐条拒绝"读成"没拦",
// 于是用例把**真的拦住了**判成失败(第一版如此),反过来也可能把真漏网判成通过。
func execNewTool(t *testing.T, c sdk.Ctx, name string, args map[string]any) (string, string) {
	t.Helper()
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(t.Context(), name, string(raw))
	if err != nil {
		t.Fatalf("%s 的 Execute 不该返回传输错误(业务失败在 res.Error 里):%v", name, err)
	}
	if res.Error != "" {
		return res.Error, res.Content
	}
	return firstToolError(res.Content), res.Content
}

// firstToolError 从结果 JSON 里取逐条错误(顶层 error 或 results[].error)。
func firstToolError(content string) string {
	var top map[string]any
	if json.Unmarshal([]byte(content), &top) != nil {
		return ""
	}
	if s, ok := top["error"].(string); ok && s != "" {
		return s
	}
	rs, _ := top["results"].([]any)
	for _, item := range rs {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := m["error"].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// outsideDir 造一个**真的在区外**的目录。
//
// 为什么不能用 t.TempDir():它位于 `$TMPDIR`,而 `$TMPDIR` 在**内核层写入白名单**里
// (见 internal/kernelsandbox.DefaultRWPaths —— 协作层刻意与内核对齐,内核允许的落点
// 协作层不更严地拒)。于是"把文件写到 t.TempDir()"在 workspace-write 档下**是被允许的**,
// 拿它当"区外"会让用例恒绿 —— 第一版就这么错的(实测:越界写"未被拦下"的假警报)。
// 家目录本身不在白名单(只有 ~/.npm、~/.cache 等子目录在),故用它当锚点。
func outsideDir(t *testing.T) string {
	t.Helper()
	home := sdk.UserHome()
	if home == "" {
		t.Skip("取不到家目录,无法构造区外路径")
	}
	d, err := os.MkdirTemp(home, "gah-e2e-outside-")
	if err != nil {
		t.Skipf("在家目录下建临时目录失败(权限?):%v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func TestNewToolsWriteTargetsAdjudicatedE2E(t *testing.T) {
	ws := t.TempDir()
	outside := outsideDir(t)
	t.Chdir(ws)

	// 工作区里放一张真图(供 image_ops 读)
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, color.RGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	srcPath := filepath.Join(ws, "a.png")
	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	f.Close()

	c := buildNewToolsEnv(t)
	outAbs := filepath.ToSlash(outside)

	// ── 应当被拦:写目标在工作区之外(每一条都是"漏了就是越界写")──────────
	rejects := []struct {
		tool string
		args map[string]any
	}{
		{"file_mkdir", map[string]any{"path": outAbs + "/d"}},
		{"file_move", map[string]any{"from": "a.png", "to": outAbs + "/a.png"}},
		{"file_copy", map[string]any{"from": "a.png", "to": outAbs + "/a.png"}},
		{"file_rename", map[string]any{"path": outAbs + "/a.png", "name": "b.png"}},
		{"file_delete", map[string]any{"paths": []any{outAbs + "/a.png"}}},
		{"image_ops", map[string]any{"action": "convert", "src": "a.png", "format": "png", "dst": outAbs + "/x.png"}},
		// 源在区外同样要拦(读侧也受限:防读外泄)
		{"image_ops", map[string]any{"action": "info", "src": outAbs + "/outside.png"}},
	}
	for _, tc := range rejects {
		msg, _ := execNewTool(t, c, tc.tool, tc.args)
		if msg == "" {
			t.Errorf("%s 的越界写目标未被拦下:%v", tc.tool, tc.args)
			continue
		}
		if !strings.Contains(msg, "workspace") && !strings.Contains(msg, "sandbox") &&
			!strings.Contains(msg, "workspace-write") && !strings.Contains(msg, "无法裁决") {
			t.Errorf("%s 的拒绝理由应让模型看得懂(workspace/sandbox 之类),得 %q", tc.tool, msg)
		}
	}

	// ── 应当放行:工作区内的写目标 ────────────────────────────────────
	allows := []struct {
		tool string
		args map[string]any
	}{
		{"file_mkdir", map[string]any{"path": "sub"}},
		{"file_copy", map[string]any{"from": "a.png", "to": "b.png"}},
		{"image_ops", map[string]any{"action": "convert", "src": "a.png", "format": "jpeg", "dst": "a.jpg"}},
		{"image_ops", map[string]any{"action": "info", "src": "a.png"}},
	}
	for _, tc := range allows {
		msg, content := execNewTool(t, c, tc.tool, tc.args)
		if msg != "" {
			t.Errorf("%s 的区内调用应放行,得拒绝:%q(结果 %s)", tc.tool, msg, content)
		}
	}

	// 越界那一侧不该真的落盘(拦下 ≠ 只是报了个错)
	for _, name := range []string{"a.png", "x.png"} {
		if _, err := os.Stat(filepath.Join(outside, name)); err == nil {
			t.Errorf("被拒的调用不该在区外留下文件:%s", name)
		}
	}
}

// TestDeleteGoesToTrashAndRestoresE2E 删除 → 回收站可查 → 恢复,这条链路是
// "敢把删除交给模型"的前提(端到端,而不是只看工具返回的 JSON)。
func TestDeleteGoesToTrashAndRestoresE2E(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Chdir(ws)
	c := buildNewToolsEnv(t)

	if err := os.WriteFile(filepath.Join(ws, "victim.txt"), []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg, _ := execNewTool(t, c, "file_delete", map[string]any{"paths": []any{"victim.txt"}}); msg != "" {
		t.Fatalf("删除应成功:%s", msg)
	}
	if _, err := os.Stat(filepath.Join(ws, "victim.txt")); !os.IsNotExist(err) {
		t.Fatalf("原文件应已移走:%v", err)
	}
	// 回收站里能查到,且记录了原路径
	msg, content := execNewTool(t, c, "file_trash_list", map[string]any{})
	if msg != "" {
		t.Fatalf("列回收站应成功:%s", msg)
	}
	var list struct {
		Count   int `json:"count"`
		Entries []struct {
			Entry    string `json:"entry"`
			Original string `json:"original"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(content), &list); err != nil {
		t.Fatalf("回收站结果应为 JSON:%v(%s)", err, content)
	}
	if list.Count != 1 || len(list.Entries) != 1 {
		t.Fatalf("回收站应有 1 条:%s", content)
	}
	if list.Entries[0].Original != filepath.Join(ws, "victim.txt") {
		t.Fatalf("应记录原路径,得 %q", list.Entries[0].Original)
	}
	// 恢复回原路径
	if msg, _ := execNewTool(t, c, "file_restore", map[string]any{"entry": list.Entries[0].Entry}); msg != "" {
		t.Fatalf("恢复应成功:%s", msg)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "victim.txt")); err != nil || string(b) != "keep me" {
		t.Fatalf("恢复后内容应回到原处:%q(%v)", b, err)
	}
}
