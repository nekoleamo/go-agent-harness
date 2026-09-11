// P4-10 会话树/分支:从历史任意点派生独立会话(ForkAt)、复制当前(CloneCurrent)、
// 分支点抽取(ForkPoints)。分支会话文件与主/切换会话同格式(jsonl 事件流),
// 继承事件 Seq 原样保留(Load 按历史 max 续接,后续演进互不影响)。
package hostcwdsessions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// ——— 事件读写(与 sessionlog jsonl 格式兼容;分支文件由本插件直接写) ———

// writeEvents 把事件序列写为新会话文件(jsonl;与 sessionlog 落盘格式一致)。
func writeEvents(path string, evs []sdk.SessionEvent) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, ev := range evs {
		b, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	return w.Flush()
}

// forkID 生成新分支会话 id(时间戳;同分钟冲突追加序号,与 New 同语义)。返回 (id, path)。
func forkID(key string) (string, string, error) {
	base := nowStamp()
	id := base
	for n := 2; ; n++ {
		if !fileExists(SessionPath(SessionsRoot(), key, id)) {
			break
		}
		id = fmt.Sprintf("%s-%d", base, n)
		if n > 10000 {
			return "", "", fmt.Errorf("cwdsessions: 无法生成分支会话 id(%s)", base)
		}
	}
	return id, SessionPath(SessionsRoot(), key, id), nil
}

func nowStamp() string {
	// 与 New 的主 id 格式一致(时间戳;New 同分钟冲突由 forkID 序号去重)
	return timeNow().Format("20060102-150405")
}

// timeNow 可替换时钟(测试注入)。
var timeNow = func() time.Time { return time.Now() }

// ——— ForkableSessions 实现 ———

// ForkAt 从当前会话历史 seq 处派生新会话:继承 seq 及以前的全部事件,
// 写新文件并切换(后续轮次只落新文件)。返回新会话 id。
func (s *Service) ForkAt(seq uint64) (string, error) {
	evs := s.sessions.Replay()
	cut := evs[:0]
	for _, ev := range evs {
		if ev.Seq <= seq {
			cut = append(cut, ev)
		}
	}
	if len(cut) == 0 {
		return "", fmt.Errorf("cwdsessions: fork seq %d 无继承事件(超出历史范围?)", seq)
	}
	parent := s.CurrentSession() // Open 前取父(切换后 current=新 id)
	src := orName(parent, "主")
	id, path, err := forkID(s.Current())
	if err != nil {
		return "", err
	}
	if err := writeEvents(path, cut); err != nil {
		return "", err
	}
	if err := s.Open(id); err != nil {
		return "", err
	}
	_ = s.Rename(fmt.Sprintf("fork@%d ← %s", seq, src))
	s.recordFork(id, parent, seq) // 派生溯源(fork-tree.json;供 /tree 树形)
	return id, nil
}

// CloneCurrent 复制当前会话全量到新会话文件(同一分支的另一路演进)。返回新会话 id。
func (s *Service) CloneCurrent() (string, error) {
	parent := s.CurrentSession() // Open 前取父(切换后 current=新 id)
	evs := s.sessions.Replay()
	src := orName(parent, "主")
	id, path, err := forkID(s.Current())
	if err != nil {
		return "", err
	}
	if err := writeEvents(path, evs); err != nil {
		return "", err
	}
	if err := s.Open(id); err != nil {
		return "", err
	}
	_ = s.Rename("clone ← " + src)
	s.recordFork(id, parent, 0) // 克隆溯源(seq 0 = 全量;供 /tree 树形)
	return id, nil
}

// ForkPoints 某会话文件(空 id = 主会话)的用户消息分支点列表(时间序,seq 供 /fork)。
func (s *Service) ForkPoints(id string) ([]sdk.ForkPoint, error) {
	path := SessionPath(SessionsRoot(), s.Current(), id)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 该会话尚无历史(空)
		}
		return nil, err
	}
	defer f.Close()
	var out []sdk.ForkPoint
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev sdk.SessionEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // 坏行容忍(与 sessionlog 同)
		}
		if ev.Kind != sdk.EventUserMessage {
			continue
		}
		text := ""
		if u, ok := ev.Payload.(sdk.UserMessage); ok {
			text = u.Content
		} else if m, ok := ev.Payload.(map[string]any); ok {
			if c, ok := m["Content"].(string); ok {
				text = c
			}
		}
		text = strings.Join(strings.Fields(text), " ")
		if n := len([]rune(text)); n > 60 {
			text = string([]rune(text)[:59]) + "…"
		}
		out = append(out, sdk.ForkPoint{Seq: ev.Seq, Text: text})
	}
	return out, sc.Err()
}

func orName(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
