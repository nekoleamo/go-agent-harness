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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".meta-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
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
	file := filepath.Base(SessionPath(SessionsRoot(), s.key, id))
	return s.renameMeta(file, name)
}

// SetSummary 写入概述缓存(F3:host-session-summary 经 ctx.cwdSessions 回写)。
func (s *Service) SetSummary(id string, sum sdk.SessionSummary) error {
	file := filepath.Base(SessionPath(SessionsRoot(), s.key, id))
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
	file := filepath.Base(SessionPath(SessionsRoot(), s.key, id))
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
