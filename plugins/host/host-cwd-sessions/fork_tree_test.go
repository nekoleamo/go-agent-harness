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
