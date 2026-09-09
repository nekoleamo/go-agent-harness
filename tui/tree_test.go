// B3 分支树渲染纯函数测试:根判定/子节点缩进/平铺回退/环保护/分支点列表。
package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestTreeRenderNodes(t *testing.T) {
	nodes := []sdk.ForkNode{
		{ID: "", Parent: ""},
		{ID: "fork-a", Parent: "", ParentSeq: 3},
		{ID: "clone-b", Parent: "fork-a", ParentSeq: 0},
	}
	disp := map[string]string{"": "主会话", "fork-a": "fork-a", "clone-b": "clone-b"}
	cur := map[string]bool{"fork-a": true}
	pts := func(id string) ([]sdk.ForkPoint, error) {
		if id == "" {
			return []sdk.ForkPoint{{Seq: 1, Text: "首问"}, {Seq: 3, Text: "三问"}}, nil
		}
		return nil, errors.New("n/a")
	}
	out := treeRenderNodes(nodes, disp, cur, pts)
	// 主会话根 + 分支点列表
	if !strings.Contains(out, "主会话(提问 2 个;") || !strings.Contains(out, "#1 首问") {
		t.Fatalf("主会话根+分支点缺失:\n%s", out)
	}
	// fork-a(fork 自主)挂主会话下仅一次;当前 ★ 落其行;clone-b 更深缩进
	if got := strings.Count(out, "fork-a"); got != 1 {
		t.Fatalf("fork-a 应单次渲染(当前: %d):\n%s", got, out)
	}
	if !strings.Contains(out, "★ fork-a") || !strings.Contains(out, "└─") {
		t.Fatalf("树线/当前标记缺失:\n%s", out)
	}
	if !strings.Contains(out, "clone-b") {
		t.Fatalf("clone-b 应渲染:\n%s", out)
	}
}

func TestTreeRenderNoRecords(t *testing.T) {
	out := treeRenderNodes(nil, map[string]string{"a": "会话a", "b": "会话b"}, nil, nil)
	if !strings.Contains(out, "会话a") || !strings.Contains(out, "会话b") {
		t.Fatalf("无记录应平铺全部:\n%s", out)
	}
	// 环保护:自环节点不无限递归
	nodes := []sdk.ForkNode{{ID: "x", Parent: "x"}}
	out = treeRenderNodes(nodes, map[string]string{"x": "x"}, nil, nil)
	if !strings.Contains(out, "x") {
		t.Fatalf("自环节点仍应渲染(深度保护):\n%s", out)
	}
}
