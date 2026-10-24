// P5.2-B3 分支树溯源:fork-tree.json(与 workspaces.json 同型覆写式 JSON)记录
// 派生会话的父系(id → 父会话 id + 父分支点 seq;clone 的 seq=0 表示全量复制)。
// 供 /tree 树形可视化(TUI cmdTree 用 ForkTree 组装树)与未来 UI。
package hostcwdsessions

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// forkTreePath 分支树衍生记录文件($GAH_HOME/sessions/fork-tree.json)。
func forkTreePath() string {
	return filepath.Join(SessionsRoot(), "fork-tree.json")
}

// readForkTree 读衍生记录(缺文件/坏 json = 空 map,容忍;与 workspaces/names 同款)。
func readForkTree(path string) map[string]sdk.ForkNode {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]sdk.ForkNode
	if err := json.Unmarshal(b, &m); err != nil {
		quarantineCorrupt(path)
		return nil
	}
	return m
}

// saveForkTree 覆写衍生记录(目录自动建;记录非关键路径,失败静默——fork 本身已生效)。
func saveForkTree(path string, m map[string]sdk.ForkNode) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeIndexAtomic(path, b, 0o600)
}

// recordFork 记录派生关系(锁内 upsert;失败静默,树形缺失不阻断 fork 主路径)。
func (s *Service) recordFork(id, parent string, seq uint64) {
	s.ftMu.Lock()
	defer s.ftMu.Unlock()
	m := readForkTree(forkTreePath())
	if m == nil {
		m = map[string]sdk.ForkNode{}
	}
	m[id] = sdk.ForkNode{ID: id, Parent: parent, ParentSeq: seq}
	_ = saveForkTree(forkTreePath(), m)
}

// ForkTree 项目会话分支树节点(含主会话节点,Parent 空 = 根;实现无记录 = 空表)。
// 返回值只读语义:调用方自行深拷贝如需修改。
//
// **按当前项目过滤**:fork-tree.json 的键是**裸会话 id**,而 id 只在项目内唯一
// (newSessionID/forkID 都只对本项目文件查重)。不过滤的话:
//   - 两个项目在同一秒各建一个分支 ⇒ 同 id ⇒ 树里只剩一条;
//   - `/tree` 会列出别的项目的节点(它们不在本项目 Sessions() 里 ⇒ 显示成裸 id 幽灵节点)。
//
// 旧记录(裸 id)保持可读 —— 判据是"这个 id 在本项目里有没有对应的会话文件"。
func (s *Service) ForkTree() ([]sdk.ForkNode, error) {
	s.ftMu.Lock()
	defer s.ftMu.Unlock()
	m := readForkTree(forkTreePath())
	out := make([]sdk.ForkNode, 0, len(m)+1)
	// 主会话恒为根(父空;未派生记录时仍在树顶)
	out = append(out, sdk.ForkNode{ID: "", Parent: ""})
	seen := map[string]bool{"": true}
	for _, n := range m {
		if seen[n.ID] || !s.sessionExists(n.ID) {
			continue
		}
		out = append(out, n)
		seen[n.ID] = true
	}
	return out, nil
}

// sessionExists 该会话 id 是否属于**当前项目**(主会话恒 true)。
// 与 Sessions() 的判定同源:主会话 = <key>.jsonl,切换会话 = <key>-<id>.jsonl。
func (s *Service) sessionExists(id string) bool {
	if id == "" {
		return true
	}
	return fileExists(SessionPath(SessionsRoot(), s.Current(), id))
}
