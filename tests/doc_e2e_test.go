// 文档预览(D 组)集成测试:base 装配下 host-docview 提供 ctx.doc、
// /preview 命令注册并按意图发出 doc/open 事件(三端联动的单一事件源)。
package tests

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	corectx "github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildDocEnv 装配 base + host-commands/host-docview/host-cwd-sessions(ctx.doc 依赖命令表)。
func buildDocEnv(t *testing.T) (*corectx.Ctx, func()) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := corectx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "host-commands"},
		{ID: "llm-mock"},
		{ID: "host-agent-loop"},
		{ID: "host-docview"}, // ctx.sandbox 未装配(可选):resolver 不设限,专测命令/事件通路
		{ID: "tool-doc"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := base.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	return c, func() { reg.DisposeAll() }
}

func TestDocServiceProvidedByBaseBundle(t *testing.T) {
	c, cleanup := buildDocEnv(t)
	defer cleanup()

	var doc sdk.DocService
	if err := c.Inject("ctx.doc", &doc); err != nil {
		t.Fatalf("base 装配应提供 ctx.doc: %v", err)
	}
	dir := t.TempDir()
	md := filepath.Join(dir, "a.md")
	if err := os.WriteFile(md, []byte("# T\n\n段落\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := doc.Preview(context.Background(), sdk.DocRequest{Path: md})
	if err != nil {
		t.Fatal(err)
	}
	if v.Format != sdk.DocFormatMarkdown || len(v.Blocks) < 2 {
		t.Fatalf("预览结果异常: format=%q blocks=%d", v.Format, len(v.Blocks))
	}
	// 块模型 → 行号化文本(模型工具共用)
	tx, err := doc.Text(context.Background(), sdk.DocRequest{Path: md})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Lines) == 0 || tx.TotalLines == 0 {
		t.Fatalf("行号化文本为空: %+v", tx)
	}
	// 会话流渲染(markdown 文本 → 块模型)
	rv, err := doc.Render(context.Background(), "**粗**与 `码`\n\n- 项\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rv.Blocks) < 2 {
		t.Fatalf("Render 块数不足: %+v", rv.Blocks)
	}
	// 目录列举
	tree, err := doc.List(context.Background(), sdk.DocRequest{Path: dir}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 1 || tree.Entries[0].Name != "a.md" {
		t.Fatalf("目录列举异常: %+v", tree.Entries)
	}
}

// D5:tool-doc 三工具进入工具清单,read_document 经 ctx.doc 真读文件。
func TestToolDocRegisteredAndReads(t *testing.T) {
	c, cleanup := buildDocEnv(t)
	defer cleanup()

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read_document", "doc_open", "doc_list"} {
		if _, ok := tools.Get(name); !ok {
			t.Fatalf("工具清单缺少 %s(模型不可见)", name)
		}
	}
	dir := t.TempDir()
	md := filepath.Join(dir, "r.md")
	if err := os.WriteFile(md, []byte("# T\n\n正文\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "read_document", fmt.Sprintf(`{"file_path":%q}`, md))
	if err != nil {
		t.Fatal(err)
	}
	// 工具结果应含行号化文本(服务端块模型 → 行号)
	if !strings.Contains(res.Content, "正文") || !strings.Contains(res.Content, "1") {
		t.Fatalf("read_document 结果异常: %s", res.Content)
	}

	// doc_open 工具 → doc/open 事件(与 /preview 命令同源)
	var got []sdk.DocOpenEvent
	dis := c.Subscribe(sdk.EventDocOpen, func(_ context.Context, ev *sdk.Event) error {
		switch p := ev.Payload.(type) {
		case sdk.DocOpenEvent:
			got = append(got, p)
		case *sdk.DocOpenEvent:
			got = append(got, *p)
		}
		return nil
	})
	defer dis()
	if _, err := tools.Execute(context.Background(), "doc_open", fmt.Sprintf(`{"file_path":%q}`, md)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != md {
		t.Fatalf("doc_open 应发出 doc/open: %+v", got)
	}
}

func TestPreviewCommandEmitsDocOpen(t *testing.T) {
	c, cleanup := buildDocEnv(t)
	defer cleanup()

	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	spec, ok := cmds.Get("preview")
	if !ok {
		t.Fatal("/preview 应在命令注册表(自动进 TUI 提示与 Web 命令面板)")
	}

	dir := t.TempDir()
	md := filepath.Join(dir, "b.md")
	if err := os.WriteFile(md, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var got []sdk.DocOpenEvent
	dis := c.Subscribe(sdk.EventDocOpen, func(_ context.Context, ev *sdk.Event) error {
		switch p := ev.Payload.(type) {
		case sdk.DocOpenEvent:
			got = append(got, p)
		case *sdk.DocOpenEvent:
			got = append(got, *p)
		}
		return nil
	})
	defer dis()

	out, err := spec.Run([]string{md})
	if err != nil {
		t.Fatalf("命令执行失败: %v", err)
	}
	if !strings.Contains(out, "已请求预览") {
		t.Fatalf("命令输出异常: %q", out)
	}
	if len(got) != 1 || got[0].Path != md {
		t.Fatalf("应发出一次 doc/open: %+v", got)
	}

	// 无效意图(不存在 / 不支持格式)不得广播
	if _, err := spec.Run([]string{filepath.Join(dir, "nope.md")}); err == nil {
		t.Fatal("不存在的路径应失败")
	}
	legacy := filepath.Join(dir, "old.doc")
	if err := os.WriteFile(legacy, []byte("D0CF11E0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Run([]string{legacy}); err == nil {
		t.Fatal("不支持格式应失败")
	}
	if _, err := spec.Run(nil); err == nil {
		t.Fatal("缺参数应失败")
	}
	if len(got) != 1 {
		t.Fatalf("失败意图不应广播: %+v", got)
	}
}
