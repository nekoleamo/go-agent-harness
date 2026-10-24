// C4 写侧(2026-10-24):从分支切回祖先会话时,把分支的一句话带进目标会话的上下文。
//
// 为什么是"切回时"而不是"派生时":/fork 是从**父**派生**子**,那一刻子还没做事,
// 没什么可带;等到用户从子分支切回主线(或任一祖先),那一段工作才算"发生过"。
//
// 成本纪律:
//   - 文本只来自**已缓存**的概述 / 预览(切会话是高频动作,这里绝不调模型);
//   - 去重不看会话事件流(那要复制整份 Replay,大会话上就是一次几十 MB 的拷贝),
//     而是记在 meta.json 的指纹里(与摘要/置顶同一张按会话的表);
//   - 每个分支、同一句话只注入一次(否则来回切几次,上下文里就排满同一段话)。
package hostcwdsessions

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// branchNoteMaxRunes 注入文本的长度上限(一句概述,不是正文)。
// 太长会把「别的会话的对话」变成主线的一部分 —— 那比"不知道"更坏。
const branchNoteMaxRunes = 160

// branchNoteKeep 每个会话保留的去重指纹条数(只用来防重复,旧指纹丢掉无害:
// 真重复注入时文本指纹仍会与最近若干条之一相同;更早的重复由 keep 上限兜不住 ——
// 代价是极长会话里来回切几十次可能重复一条,远小于无限增长的风险)。
const branchNoteKeep = 20

// noteBranchReturn 在从 from 切到 to 之后调用:若 from 是 to 的**后代分支**,
// 就往 to(当前已 Load 的会话)追加一条 sdk.EventBranchNote。
func (s *Service) noteBranchReturn(from, to string) {
	if from == "" || from == to || s.sessions == nil {
		return
	}
	descendant, forkSeq := s.isBranchDescendant(to, from)
	if !descendant {
		return
	}
	// 一次读 meta + names(下面三处都要用),别各读一遍 —— 切会话是高频路径。
	toFile := filepath.Base(SessionPath(SessionsRoot(), s.Current(), to))
	fromFile := filepath.Base(SessionPath(SessionsRoot(), s.Current(), from))
	s.nmMu.Lock()
	meta := loadMeta(metaPath())
	names := loadNames(filepath.Join(SessionsRoot(), "names.json"))
	s.nmMu.Unlock()

	gist := branchGistOf(SessionsRoot(), s.Current(), from, forkSeq, meta)
	if gist == "" {
		return
	}
	note := sdk.BranchNote{From: from, FromName: metaName(meta, names, fromFile), Text: gist, At: time.Now().Unix()}
	fp := branchNoteFingerprint(from, gist)

	s.nmMu.Lock()
	me := meta[toFile]
	for _, old := range me.BranchNotes {
		if old == fp {
			s.nmMu.Unlock()
			return // 同一分支、同一句话已经注入过
		}
	}
	s.nmMu.Unlock()

	if err := s.sessions.Append(sdk.SessionEvent{Kind: sdk.EventBranchNote, Payload: note}); err != nil {
		if s.logger != nil {
			// 不阻断切换:会话已经切过去了,备注是"顺带补一条上下文",失败要说出来但别把用户卡住。
			s.logger.Warn("branch/note 注入失败(会话切换已生效)", "from", from, "to", to, "err", err)
		}
		return
	}

	// 事件写成功后才记指纹(反过来会出现"记了但没写进去"⇒ 该注入的永远不注)。
	s.nmMu.Lock()
	meta = loadMeta(metaPath())
	me = meta[toFile]
	me.BranchNotes = append(me.BranchNotes, fp)
	if len(me.BranchNotes) > branchNoteKeep {
		me.BranchNotes = me.BranchNotes[len(me.BranchNotes)-branchNoteKeep:]
	}
	meta[toFile] = me
	_ = saveMeta(metaPath(), meta)
	s.nmMu.Unlock()
}

// branchNoteFingerprint 去重指纹:哪个分支 + 哪句话(文本取哈希,不必存全文)。
func branchNoteFingerprint(from, text string) string {
	sum := sha1.Sum([]byte(text))
	return from + "|" + hex.EncodeToString(sum[:6])
}

// isBranchDescendant 判断 child 是否是 ancestor 的后代(沿 fork-tree.json 上溯),
// 并返回 child 自己的派生点(ParentSeq;0 = 全量复制 clone)。
// ancestor 空 = 主会话(所有派生链的根)。
func (s *Service) isBranchDescendant(ancestor, child string) (bool, uint64) {
	s.ftMu.Lock()
	tree := readForkTree(forkTreePath())
	s.ftMu.Unlock()
	if n, ok := tree[child]; ok && n.Parent == ancestor {
		return true, n.ParentSeq
	}
	cur := child
	for cur != "" {
		n, ok := tree[cur]
		if !ok {
			return false, 0 // 没派生记录:不是从本会话分出去的(手工拷文件/外部写入)
		}
		if n.Parent == ancestor {
			// 深层链(祖先 → A → B):B 的 forkSeq 仍是 B 自己的派生点,
			// 用它之后的用户消息才能回答"B 这条支做过什么"。
			if own, ok := tree[child]; ok {
				return true, own.ParentSeq
			}
			return true, n.ParentSeq
		}
		cur = n.Parent
	}
	return false, 0
}

// branchGistOf 分支的一句话:优先已缓存的概述;没有概述时退回到**该分支自己在派生点之后**
// 的第一条用户消息,再没有就不注入(返回空)。
//
// ⚠️ 不能拿"文件里首条用户消息"当退路:`ForkAt/CloneCurrent` 会把父会话的前缀事件原样
// 复制进分支文件,所以子文件的第一句永远是**主线的开场白** —— 用它会让主线收到一条
// 「分支做过:我自己刚问的那句」(把父的内容说成分支的产出)。派生点之后的才真是它的工作。
func branchGistOf(root, key, id string, forkSeq uint64, meta map[string]sessionMeta) string {
	path := SessionPath(root, key, id)
	if me, ok := meta[filepath.Base(path)]; ok && me.Summary != nil {
		if t := oneLine(me.Summary.Text); t != "" {
			return t
		}
	}
	return firstUserLineAfter(path, forkSeq)
}

// firstUserLineAfter 读会话文件,取 Seq > forkSeq 的第一条 user/message 正文(压成一行)。
// 只为一个可能的回退读文件:切会话时通常已有概述缓存,走不到这里。
func firstUserLineAfter(path string, forkSeq uint64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Seq     uint64
			Kind    string
			Payload struct{ Content string }
		}
		if json.Unmarshal(line, &ev) != nil {
			continue // 坏行容忍(与其它读侧同口径)
		}
		if ev.Kind != "user/message" || ev.Seq <= forkSeq {
			continue
		}
		if t := oneLine(ev.Payload.Content); t != "" {
			return t
		}
	}
	return ""
}

// oneLine 压成一行并截断(注入的是"一句",不是正文)。
func oneLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(strings.Join(strings.Fields(ln), " "))
		if t == "" {
			continue
		}
		r := []rune(t)
		if len(r) > branchNoteMaxRunes {
			return string(r[:branchNoteMaxRunes]) + "…"
		}
		return t
	}
	return ""
}

// branchNotePayload 取备注载荷(内存里是具体类型;Load 后由 internal/sessionevents 还原,
// 这里仍兼容未还原的 map —— 测试与诊断工具会从事件流里挑备注)。
func branchNotePayload(v any) (sdk.BranchNote, bool) {
	switch t := v.(type) {
	case sdk.BranchNote:
		return t, true
	case map[string]any:
		n := sdk.BranchNote{}
		if s, ok := t["from"].(string); ok {
			n.From = s
		}
		if s, ok := t["from_name"].(string); ok {
			n.FromName = s
		}
		if s, ok := t["text"].(string); ok {
			n.Text = s
		}
		return n, true
	}
	return sdk.BranchNote{}, false
}
