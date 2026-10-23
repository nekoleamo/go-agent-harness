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
	out := treeRenderNodes(nodes, disp, cur, pts, nil)
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
	out := treeRenderNodes(nil, map[string]string{"a": "会话a", "b": "会话b"}, nil, nil, nil)
	if !strings.Contains(out, "会话a") || !strings.Contains(out, "会话b") {
		t.Fatalf("无记录应平铺全部:\n%s", out)
	}
	// 环保护:自环节点不无限递归
	nodes := []sdk.ForkNode{{ID: "x", Parent: "x"}}
	out = treeRenderNodes(nodes, map[string]string{"x": "x"}, nil, nil, nil)
	if !strings.Contains(out, "x") {
		t.Fatalf("自环节点仍应渲染(深度保护):\n%s", out)
	}
}

// TestTreeBranchGist 分支行补一句"这条支在干什么"(C4「弃支一句话」的只读那半)。
//
// 为什么要:一堆 `fork-3` / `fork-7` 只能靠名字猜当初为什么开了这条支;而会话概述
// (host-session-summary)**早就生成了**,只是 /tree 从来没读过它。
//
// 纪律:这里**只读缓存**,绝不调模型 —— /tree 是查看命令,查看触发模型是要命的成本形态。
// 反向验证:去掉 treeRenderNodes 里的 depth>0 那一段 ⇒ 本用例红(拿不到 gist)。
func TestTreeBranchGist(t *testing.T) {
	nodes := []sdk.ForkNode{{ID: "", Parent: ""}, {ID: "fork-a", Parent: "", ParentSeq: 3}}
	gists := map[string]string{"": "主线的这一句不该出现", "fork-a": "试了用 A 方案导出,后来放弃"}
	out := treeRenderNodes(nodes, map[string]string{"": "主会话", "fork-a": "fork-a"}, nil, nil, gists)
	if !strings.Contains(out, "试了用 A 方案导出") {
		t.Fatalf("分支行应显示它的一句话概述:\n%s", out)
	}
	if strings.Contains(out, "主线的这一句不该出现") {
		t.Fatalf("根(主线)不该再写一遍概述(噪音):\n%s", out)
	}
	// 没有概述的分支:不摆空行(空串会让树多出一行空白)
	out2 := treeRenderNodes(nodes, map[string]string{"": "主会话", "fork-a": "fork-a"}, nil, nil, map[string]string{"fork-a": ""})
	if strings.Count(out2, "\n\n") > 0 {
		t.Fatalf("没有 gist 时不该留空行:\n%q", out2)
	}
}

// TestBranchGistsPrefersSummary 概述优先,没概述时退回首条用户消息(Preview)。
func TestBranchGistsPrefersSummary(t *testing.T) {
	infos := []sdk.SessionInfo{
		{ID: "a", Summary: "第一行\n第二行不该出现", Preview: "预览不该被用"},
		{ID: "b", Preview: "首条用户消息"},
		{ID: "c"},
	}
	g := branchGists(infos)
	if g["a"] != "第一行" {
		t.Fatalf("概述应优先且只取首行,got %q", g["a"])
	}
	if g["b"] != "首条用户消息" {
		t.Fatalf("无概述应退回 Preview,got %q", g["b"])
	}
	if _, ok := g["c"]; ok {
		t.Fatalf("两者都空时不该有词条(否则树里多空行),got %q", g["c"])
	}
	// 超长截断(概述可能是整段;列表里一句就够)
	long := strings.Repeat("字", 80)
	if got := firstLine(long); []rune(got) == nil || len([]rune(got)) != 61 {
		t.Fatalf("超长应截到 60 字 + 省略号,got %d 字", len([]rune(got)))
	}
}
