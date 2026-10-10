// 会话元数据(F 组 F0/F2/F3):$GAH_HOME/sessions/meta.json。
//
// 设计(docs/SESSION_UX_PLAN.md §3.2):
//   - 与旧的 names.json(**仅显示名**)双写兼容:读以 meta.json 为准,缺失条目回退 names.json;
//     写时两者同写(旧二进制/降级场景仍能读到名字);
//   - 坏文件/坏字段容忍(同 loadNames:缺文件/坏 JSON = 空 map,不抛);
//   - 写盘 0600 + 原子(临时文件 + rename),便携纪律:路径经 SessionsRoot() 派生。
package hostcwdsessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// pinnedMax 置顶上限(超出显式报错,不静默丢弃)。
const pinnedMax = 8

// nowUnix 当前时间(unix 秒;集中一处便于测试替换语义)。
func nowUnix() int64 { return time.Now().Unix() }

// summaryEntry 概述缓存条目(meta.json.summary;结构镜像 sdk.SessionSummary)。
type summaryEntry struct {
	Text          string   `json:"text"`
	Topics        []string `json:"topics,omitempty"`
	CoveredFrames int      `json:"covered_frames,omitempty"`
	Model         string   `json:"model,omitempty"`
	TS            int64    `json:"ts,omitempty"`
	InputHash     string   `json:"input_hash,omitempty"`
}

// sessionMeta 单个会话的元数据条目。
type sessionMeta struct {
	Name     string        `json:"name,omitempty"`
	Pinned   bool          `json:"pinned,omitempty"`
	PinnedAt int64         `json:"pinned_at,omitempty"`
	Summary  *summaryEntry `json:"summary,omitempty"`
	// 会话级偏好(第一百一十六批):每个会话各用各的角色/模型/思考档/沙箱档/审批档。
	//
	// 为什么空 = "跟随全局":老会话零迁移(没有这些字段 = 跟随全局当前值 = 与从前逐字一致),
	// 用户改全局仍能一次改全部;页签上用徽标区分"跟随全局"与"本页签独立"。
	// 为什么不另开一个 prefs 文件:它与会话一一对应,而 meta.json 已经是"按会话的元数据"这一张表。
	Prefs *sdk.SessionPrefs `json:"prefs,omitempty"`
}

// SetSessionPrefs 写会话级偏好(id 空 = 主会话)。
//
// 只写"非空项"之外的整份快照:调用方拿到的是**合并后的完整值**(先读现状 → 改若干项 → 写回),
// 所以这里不提供单项 setter —— 单项 setter 会在两个页面同时改不同项时丢更新。
// 整份写回用 nmMu + 原子写,多页签并发改**不同会话**也被串行化(同一张 meta.json)。
func (s *Service) SetSessionPrefs(id string, p sdk.SessionPrefs) error {
	file := filepath.Base(SessionPath(SessionsRoot(), s.Current(), id))
	// 存在性检查**放行当前打开的会话**:空会话从未写过内容时文件还不存在(首次写入才建),
	// 而"新建会话后立刻选个角色"是正常操作 —— 按文件判存在会把这条路径堵死。
	// 其它 id 仍要求文件真的存在,避免给永远不会出现的 id 造元数据。
	if id != s.CurrentSession() && !fileExists(filepath.Join(SessionsRoot(), file)) {
		return fmt.Errorf("cwdsessions: 会话不存在: %s", file)
	}
	s.nmMu.Lock()
	defer s.nmMu.Unlock()
	m := loadMeta(metaPath())
	e := m[file]
	e.Prefs = &p
	m[file] = e
	return saveMeta(metaPath(), m)
}

// SessionPrefsOf 读会话级偏好(id 空 = 主会话)。
//
// 返回的是**原始值**:某项为空 = 该会话没单独设 = 跟随全局(回落由消费方用
// sdk.ResolveSessionPrefs 统一做,别在各处各写一份 —— 那必然漂)。
func (s *Service) SessionPrefsOf(id string) sdk.SessionPrefs {
	file := filepath.Base(SessionPath(SessionsRoot(), s.Current(), id))
	s.nmMu.Lock()
	defer s.nmMu.Unlock()
	m := loadMeta(metaPath())
	if e := m[file]; e.Prefs != nil {
		return *e.Prefs
	}
	return sdk.SessionPrefs{}
}

// metaPath 会话元数据索引($GAH_HOME/sessions/meta.json;与 names.json 同目录)。
func metaPath() string { return filepath.Join(SessionsRoot(), "meta.json") }

// loadMeta 读元数据索引(缺文件/坏 JSON = 空 map,容忍)。
func loadMeta(path string) map[string]sessionMeta {
	b, err := os.ReadFile(path)
	if err != nil {
		return map[string]sessionMeta{}
	}
	var m map[string]sessionMeta
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]sessionMeta{}
	}
	if m == nil {
		m = map[string]sessionMeta{}
	}
	return m
}

// saveMeta 原子写元数据索引(临时文件 + rename;权限 0600)。
func saveMeta(path string, m map[string]sessionMeta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeIndexAtomic(path, b, 0o600)
}

// writeIndexAtomic 覆写式索引文件原子落盘(同目录临时文件 + chmod + rename):
// workspaces.json / names.json / fork-tree.json / meta.json 共用。这些索引读端按
// "坏 json = 空表"容忍,一次半截覆写即让整表消失,故必须原子替换。
func writeIndexAtomic(path string, raw []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".idx-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := sdk.ReplaceFile(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// quarantineCorrupt 把解析失败的索引文件改名留存(.corrupt-<时间戳>),不静默丢弃也不
// 让它被"空表覆写"抹掉;改名失败(权限等)不影响调用方的容忍语义。
func quarantineCorrupt(path string) {
	_ = os.Rename(path, fmt.Sprintf("%s.corrupt-%s", path, time.Now().Format("20060102-150405")))
}

// RenameMeta 设置某会话文件名的元数据(空名清除名字字段;保留置顶与概述)。
// 供 Rename(f 一致签名)。
func (s *Service) renameMeta(file, name string) error {
	s.nmMu.Lock()
	defer s.nmMu.Unlock()
	m := loadMeta(metaPath())
	e := m[file]
	if name == "" {
		e.Name = ""
	} else {
		e.Name = name
	}
	if e.Name == "" && !e.Pinned && e.Summary == nil {
		delete(m, file) // 无任何字段 → 不留空条目(避免 meta.json 膨胀)
	} else {
		m[file] = e
	}
	return s.saveMetaAndNames(m, file, name)
}

// saveMetaAndNames 双写:meta.json(权威)+ names.json(兼容旧读路径)。
func (s *Service) saveMetaAndNames(m map[string]sessionMeta, file, name string) error {
	if err := saveMeta(metaPath(), m); err != nil {
		return err
	}
	names := loadNames(sessionNamesPath())
	if names == nil {
		names = map[string]string{}
	}
	if name == "" {
		delete(names, file)
	} else {
		names[file] = name
	}
	return saveNames(sessionNamesPath(), names)
}

// metaName 取会话显示名(meta.json 优先,缺失回退 names.json)。
func metaName(m map[string]sessionMeta, names map[string]string, file string) string {
	if e, ok := m[file]; ok && e.Name != "" {
		return e.Name
	}
	return names[file]
}

// SetName 按 id 设置会话显示名(id 空 = 主会话;双写 meta.json + names.json)。
func (s *Service) SetName(id, name string) error {
	file := filepath.Base(SessionPath(SessionsRoot(), s.Current(), id))
	return s.renameMeta(file, name)
}

// SetSummary 写入概述缓存(F3:host-session-summary 经 ctx.cwdSessions 回写)。
func (s *Service) SetSummary(id string, sum sdk.SessionSummary) error {
	file := filepath.Base(SessionPath(SessionsRoot(), s.Current(), id))
	if !fileExists(filepath.Join(SessionsRoot(), file)) {
		return fmt.Errorf("cwdsessions: 会话不存在: %s", file)
	}
	return s.storeSummary(file, &summaryEntry{
		Text: sum.Text, Topics: sum.Topics, CoveredFrames: sum.CoveredFrames,
		Model: sum.Model, TS: sum.TS, InputHash: sum.InputHash,
	})
}

// SetPinned 置顶/取消置顶(id 空 = 主会话;幂等;置顶上限 8)。
func (s *Service) SetPinned(id string, pinned bool) error {
	file := filepath.Base(SessionPath(SessionsRoot(), s.Current(), id))
	s.nmMu.Lock()
	defer s.nmMu.Unlock()
	m := loadMeta(metaPath())
	// 会话必须存在(避免给不存在的 id 造元数据)
	if !fileExists(filepath.Join(SessionsRoot(), file)) {
		return fmt.Errorf("cwdsessions: 会话不存在: %s", file)
	}
	e := m[file]
	if e.Pinned == pinned {
		return nil // 幂等
	}
	if pinned {
		n := 0
		for _, x := range m {
			if x.Pinned {
				n++
			}
		}
		if n >= pinnedMax {
			return fmt.Errorf("cwdsessions: 置顶已达上限 %d(先取消其余置顶)", pinnedMax)
		}
		e.Pinned = true
		e.PinnedAt = nowUnix()
	} else {
		e.Pinned = false
		e.PinnedAt = 0
	}
	if e.Name == "" && !e.Pinned && e.Summary == nil {
		delete(m, file)
	} else {
		m[file] = e
	}
	return saveMeta(metaPath(), m)
}

// storeSummary 写概述缓存(F3 插件经 sdk 服务回写;此处由 Service 内部调用方使用)。
func (s *Service) storeSummary(file string, sum *summaryEntry) error {
	s.nmMu.Lock()
	defer s.nmMu.Unlock()
	m := loadMeta(metaPath())
	e := m[file]
	if sum == nil {
		e.Summary = nil
	} else {
		e.Summary = sum
	}
	if e.Name == "" && !e.Pinned && e.Summary == nil {
		delete(m, file)
	} else {
		m[file] = e
	}
	return saveMeta(metaPath(), m)
}

// summaryStateOf 概述状态(ready/stale/missing)。
func summaryStateOf(e *summaryEntry, frames int) string {
	if e == nil || e.Text == "" {
		return "missing"
	}
	if e.CoveredFrames != frames {
		return "stale"
	}
	return "ready"
}

// sortSessionsByPinned 排序:置顶区(pinned_at 倒序)→ 主会话 → 其余(mtime 倒序)。
// 单一实现(多端共用;TUI 选择器 / Web 列表顺序一致)。
func sortSessionsByPinned(out []sdk.SessionInfo) {
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if a.Pinned && b.Pinned {
			return a.PinnedAt > b.PinnedAt
		}
		if (a.ID == "") != (b.ID == "") {
			return a.ID == "" // 主会话在非置顶区仍居首(F0 前既有语义)
		}
		return a.MTime > b.MTime
	})
}
