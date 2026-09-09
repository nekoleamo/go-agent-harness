// delivery ledger(P2 可靠投递一期):QQ 滞留文本(被动失效/频控/配额耗尽)持久化,
// 落盘 $GAH_HOME/config/qqbot-outbox.yaml(0600 原子 tmp+rename),重启不丢——
// 下次该会话入站 flush 被动补发(hermes delivery ledger 最小形态:按 chat 单槽,
// 成功投递即删除;失败放回,不自动轰炸)。微信线主动受限不建 ledger(留 lastError 诊断)。
package uimqq

import (
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/yaml.v3"
)

// ledgerState 落盘形态(ChatID → 滞留文本;key 用渠道内 chatID 即足够,
// 单渠道文件内不冲突)。
type ledgerState struct {
	Entries map[string]string `yaml:"entries"`
}

// deliveryLedger ChatID → 滞留文本 持久映射(并发安全)。
type deliveryLedger struct {
	mu   sync.Mutex
	path string
	m    map[string]string
}

func newDeliveryLedger(path string) *deliveryLedger {
	l := &deliveryLedger{path: path, m: make(map[string]string)}
	if path != "" {
		l.load()
	}
	return l
}

// Set 滞留整段文本(覆盖旧滞留——同 chat 单槽,旧内容已被新结果取代)。
func (l *deliveryLedger) Set(chatID, text string) {
	l.mu.Lock()
	l.m[chatID] = text
	raw, err := yaml.Marshal(&ledgerState{Entries: l.m})
	path := l.path
	l.mu.Unlock()
	if err != nil || path == "" {
		return
	}
	saveYAML0600(path, raw)
}

// GetAndClear 取走滞留内容(补发成功语义:调用方拿到即视为已投递,失败自行 Set 放回)。
func (l *deliveryLedger) GetAndClear(chatID string) string {
	l.mu.Lock()
	pend := l.m[chatID]
	delete(l.m, chatID)
	raw, err := yaml.Marshal(&ledgerState{Entries: l.m})
	path := l.path
	l.mu.Unlock()
	if err == nil && path != "" {
		saveYAML0600(path, raw)
	}
	return pend
}

// Pending 是否存在滞留(状态展示)。
func (l *deliveryLedger) Pending(chatID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.m[chatID]
	return ok
}

// Count 滞留条目数(诊断/状态)。
func (l *deliveryLedger) Count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.m)
}

func (l *deliveryLedger) load() {
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return
	}
	var st ledgerState
	if err := yaml.Unmarshal(raw, &st); err != nil {
		return
	}
	l.mu.Lock()
	if st.Entries != nil {
		l.m = st.Entries
	}
	l.mu.Unlock()
}

// saveYAML0600 原子落盘(0600 tmp+rename;失败静默——滞留内存仍在,下次入站再试)。
func saveYAML0600(path string, raw []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
