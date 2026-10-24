// P5.2-B3 分支树溯源测试:ForkAt/CloneCurrent 后 ForkTree 记录父系(id→parent+seq);
// clone seq=0;主会话恒为根;无记录空表。
package hostcwdsessions

import (
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestForkTreeRecords(t *testing.T) {
	svc, fs := forkSetup(t)
	fs.Load("")
	// /fork 从 seq 2 派生
	id, err := svc.ForkAt(2)
	if err != nil {
		t.Fatal(err)
	}
	tree, _ := svc.ForkTree()
	node, ok := findForkNode(tree, id)
	if !ok || node.Parent != "" || node.ParentSeq != 2 {
		t.Fatalf("fork 记录应 parent=主(空)+seq2: %+v ok=%v", node, ok)
	}
	// /clone 当前(fork 会话)→ seq0
	cid, err := svc.CloneCurrent()
	if err != nil {
		t.Fatal(err)
	}
	tree, _ = svc.ForkTree()
	node, ok = findForkNode(tree, cid)
	if !ok || node.Parent != id || node.ParentSeq != 0 {
		t.Fatalf("clone 记录应 parent=%s+seq0: %+v", id, node)
	}
	// 主会话节点恒存在且为根
	if _, ok := findForkNode(tree, ""); !ok {
		t.Fatalf("主会话根节点缺失: %+v", tree)
	}
}

func findForkNode(nodes []sdk.ForkNode, id string) (sdk.ForkNode, bool) {
	for _, n := range nodes {
		if n.ID == id {
			return n, true
		}
	}
	return sdk.ForkNode{}, false
}

// 别的项目的派生节点不得出现在本项目的树里(裸 id 只在项目内唯一)。
// **反向验证**:去掉 ForkTree 的 sessionExists 过滤 ⇒ 本用例红。
func TestForkTreeFiltersOtherProjects(t *testing.T) {
	withGahHome(t, t.TempDir())
	// 另一个项目的会话文件
	other := SessionPath(SessionsRoot(), "other-key", "20260101-000000")
	if err := writeEvents(other, []sdk.SessionEvent{{Seq: 1, Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	// 本项目的派生记录 + 一条指向别项目的记录
	if err := saveForkTree(forkTreePath(), map[string]sdk.ForkNode{
		"20260102-000000": {ID: "20260102-000000", Parent: ""},
		"20260101-000000": {ID: "20260101-000000", Parent: ""}, // 属于 other-key
	}); err != nil {
		t.Fatal(err)
	}
	// 本项目确实有第一条的会话文件
	if err := writeEvents(SessionPath(SessionsRoot(), "k", "20260102-000000"), []sdk.SessionEvent{{Seq: 1, Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "y"}}}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{key: "k"}
	nodes, err := svc.ForkTree()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.ID == "20260101-000000" {
			t.Fatal("别的项目的会话不应出现在本项目的分支树里(/tree 会显示成幽灵节点)")
		}
	}
	found := false
	for _, n := range nodes {
		if n.ID == "20260102-000000" {
			found = true
		}
	}
	if !found {
		t.Fatal("本项目的分支节点不应被过滤掉")
	}
}
