// QQ 主动消息配额记账(IM_REMOTE §7.5 时效与主动配额:官方单聊主动受限——
// 私信主动消息 2 条/天/用户;频控 30qpm/1000 条日。防“发到成功为止”轰炸,超额静默降级)。
// 内存 + 落盘 $GAH_HOME/config/qqbot-quota.yaml(0600,重启不超发);跨本地日自动重置。
package uimqq

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// activeQuota 主动消息日预算(按 SenderKey 计)。线程安全。
type activeQuota struct {
	mu        sync.Mutex
	path      string
	day       string // 当前记账日(YYYY-MM-DD;跨日重置)
	used      map[string]int
	maxPerDay int // 每用户每日主动消息上限(默认 2)
}

// quotaState 落盘结构。
type quotaState struct {
	Day  string         `yaml:"day"`
	Used map[string]int `yaml:"used"`
}

// newActiveQuota 构造记账器并读入既有状态;path 为空 = 纯内存(不落盘)。
// 读取失败(损坏/权限)不阻塞:以空状态继续(宽松;主动发送低频)。
func newActiveQuota(path string) *activeQuota {
	q := &activeQuota{path: path, day: dayNow(), used: map[string]int{}, maxPerDay: 2}
	if path == "" {
		return q
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return q
	}
	var st quotaState
	if err := yaml.Unmarshal(raw, &st); err != nil {
		return q
	}
	if st.Day == q.day { // 仅当同日才继承;跨日自然重置
		q.used = st.Used
	}
	return q
}

// Allow 该发送方今日是否仍有主动预算。
func (q *activeQuota) Allow(sender string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.day != dayNow() { // 跨日惰性重置
		q.day = dayNow()
		q.used = map[string]int{}
	}
	return q.used[sender] < q.maxPerDay
}

// Consume 扣减一次主动配额并落盘(原子 tmp+rename;落盘失败不阻断计数,宽松)。
func (q *activeQuota) Consume(sender string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.day != dayNow() {
		q.day = dayNow()
		q.used = map[string]int{}
	}
	q.used[sender]++
	if q.path == "" {
		return nil
	}
	return q.saveLocked()
}

// Remaining 剩余预算(诊断/状态展示)。
func (q *activeQuota) Remaining(sender string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.day != dayNow() {
		return q.maxPerDay
	}
	n := q.maxPerDay - q.used[sender]
	if n < 0 {
		return 0
	}
	return n
}

// saveLocked 原子落盘(调用方持锁)。
func (q *activeQuota) saveLocked() error {
	raw, err := yaml.Marshal(&quotaState{Day: q.day, Used: q.used})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(q.path), 0o700); err != nil {
		return err
	}
	tmp := q.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, q.path)
}

func dayNow() string { return time.Now().Format("2006-01-02") }
