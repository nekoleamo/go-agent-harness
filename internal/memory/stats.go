// stats.go:自动调取的**累计**账本(2026-11-08,B 批借鉴)。
//
// 为什么需要它:在此之前只有一个"最近一次注入"的快照 —— 回答得了"这一轮注入了什么",
// 回答不了"这套记忆值不值它的 token"。Routed CL(arXiv 2610.38796)把成本与奖励并列
// 当一等结论(-65~72% 成本与 +15~30% 奖励写在同一句里),而本仓库连分母都没有:
// 命中几次、平均多少字节、截断几次,全都没有记账。
//
// 口径三件事,写清免得后来对不上(pi 侧 2026-11-07 就吃过"两个口径的数字互比"的亏):
//   - **分母是「请求」不是「轮」**:Augment 在**每次组装模型请求**时调用一次
//     (见 host-agent-loop 的请求组装),一个用户回合可能对应多次请求。用"轮"当分母
//     会把命中率算成偏高的假数。
//   - **命中** = 该请求尾部真的追加了条目(Items > 0)。零命中与"没查询/回退"分开计
//     (Fallbacks),否则"没有查询"会伪装成"检索失败"。
//   - **成本** = 稳定块字节(每个请求都带) + 尾部追加字节(命中才有)。只记后者会
//     系统性低估:稳定块才是每请求都付的那笔钱。
//
// 落 `$GAH_HOME/memory/inject-stats.json`,按项目分桶(项目键上限 50,淘汰最久未动的)。
// 坏文件按零值继续并**返回 error**:账本坏了不该阻塞记忆,但必须能被发现(与
// extract-state.json 同一条纪律)。
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// MaxLedgerProjects 账本里保留几个项目桶。
//
// 为什么要有上限:这个文件会在**每次请求**之后被累计(节流落盘),项目键是工作区
// 绝对路径派生的 ⇒ 换工作区/换机就会长一个新桶。不设上限等于让一个诊断账本
// 变成数据根里的长期垃圾。
const MaxLedgerProjects = 50

// InjectLedger 一个作用域(用户级 / 某个项目)的累计注入账。
type InjectLedger struct {
	Requests     int    `json:"requests"`          // 组装过的模型请求数(分母)
	Hits         int    `json:"hits"`              // 其中尾部真的追加了条目的请求数
	Fallbacks    int    `json:"fallbacks"`         // 没查询/关闭/读盘失败 ⇒ 不追加,不算"命中"
	TailBytes    int    `json:"tail_bytes"`        // 尾部追加字节累计
	TailBytesMax int    `json:"tail_bytes_max"`    // 单次尾部追加峰值(看最坏情况)
	StableBytes  int    `json:"stable_bytes"`      // 最近一次稳定块字节(每请求都付的那笔)
	Truncated    int    `json:"truncated"`         // 因预算/topK 截断的请求数
	LastAt       string `json:"last_at,omitempty"` // RFC3339(最后一次记账)
}

// HitRate 命中率(0..1;没有请求时返回 0,不返回 NaN)。
func (l InjectLedger) HitRate() float64 {
	if l.Requests <= 0 {
		return 0
	}
	return float64(l.Hits) / float64(l.Requests)
}

// AvgTailBytes 平均每次命中的尾部追加字节(没有命中时为 0)。
func (l InjectLedger) AvgTailBytes() int {
	if l.Hits <= 0 {
		return 0
	}
	return l.TailBytes / l.Hits
}

// CostPerRequest 平均每请求的记忆占用(稳定块 + 摊薄后的尾部追加)。这是"值不值"的分子。
func (l InjectLedger) CostPerRequest() int {
	if l.Requests <= 0 {
		return 0
	}
	return l.StableBytes + l.TailBytes/l.Requests
}

// Format 一行口径描述(/memory status 与面板共用;不含分数以外的浮点,便于比对)。
func (l InjectLedger) Format() string {
	if l.Requests == 0 {
		return "还没有记账(没跑过请求或刚升级;账本从第一次注入开始累计)"
	}
	// 两个分项必须**相加等于**每请求成本,否则这条口径句自己就自相矛盾:
	// 尾部按"每请求摊薄"(TailBytes/Requests),不是按命中次数平均(后者只说明命中时多大)。
	return fmt.Sprintf("命中 %d/%d 请求(%.0f%%);每请求约 %d 字节(稳定块 %d + 尾部摊薄 %d),尾部峰值 %d;截断 %d 次,回退 %d 次",
		l.Hits, l.Requests, l.HitRate()*100, l.CostPerRequest(), l.StableBytes, l.TailBytes/max(l.Requests, 1), l.TailBytesMax,
		l.Truncated, l.Fallbacks)
}

// InjectStats 账本文件(用户级 + 各项目)。
type InjectStats struct {
	User     InjectLedger            `json:"user"`
	Projects map[string]InjectLedger `json:"projects,omitempty"`
}

// InjectStatsPath 账本路径。
func InjectStatsPath() string { return filepath.Join(Dir(), "inject-stats.json") }

// LoadInjectStats 读账本(不存在 = 零值,不是错误;坏文件按零值继续并返回 error)。
func LoadInjectStats() (InjectStats, error) {
	var st InjectStats
	raw, err := os.ReadFile(InjectStatsPath())
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return InjectStats{}, fmt.Errorf("memory: 注入账本损坏(按空账本继续): %w", err)
	}
	if st.Projects == nil {
		st.Projects = map[string]InjectLedger{}
	}
	return st, nil
}

// Save 落盘(原子写)。
func (s InjectStats) Save() error {
	if s.Projects == nil {
		s.Projects = map[string]InjectLedger{}
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(InjectStatsPath(), append(raw, '\n'))
}

// InjectSample 一次组装请求时的观测值(host-memory 侧采集)。
type InjectSample struct {
	Mode        string // per_step|off|fallback
	Items       int    // 本次尾部追加了几条
	TailBytes   int    // 本次尾部追加的字节
	StableBytes int    // 稳定块字节(每次组装都带)
	Truncated   bool
	Fallback    bool // 没查询/关闭/读盘失败 ⇒ 根本没检索。与「检索了但零命中」不同,分开计。
	At          time.Time
}

// Apply 把一次采样记进账本(纯函数,便于测试;落盘交给调用方)。
func (s InjectStats) Apply(projectKey string, sample InjectSample) InjectStats {
	if s.Projects == nil {
		s.Projects = map[string]InjectLedger{}
	}
	at := sample.At
	if at.IsZero() {
		at = time.Now()
	}
	led := s.User
	if key := projectKey; key != "" {
		led = s.Projects[key]
	}
	led.Requests++
	if sample.Items > 0 {
		led.Hits++
		led.TailBytes += sample.TailBytes
		if sample.TailBytes > led.TailBytesMax {
			led.TailBytesMax = sample.TailBytes
		}
	}
	if sample.Fallback {
		// 「没查询/关闭/读盘失败」与「检索了但零命中」不是一件事:后者分母里要算,
		// 否则命中率会被没检索的请求稀释成偏低的假数。
		led.Fallbacks++
	}
	if sample.Truncated {
		led.Truncated++
	}
	// 稳定块记「最近一次」而不是累计 —— 累计会把同一个块重复计价。
	// **必须无条件赋值**(含 0):关掉注入后稳定块真的是 0,若沿用上一次的值,
	// 成本账会长期停在"每请求 950 字节",而实际上一个字节都没注入。
	led.StableBytes = sample.StableBytes
	led.LastAt = at.Format(time.RFC3339)
	if projectKey == "" {
		s.User = led
		return s
	}
	s.Projects[projectKey] = led
	s.evictProjects()
	return s
}

// evictProjects 桶数超上限时淘汰"最久没动过"的(同一毫秒内按 key 稳定排序,便于测试)。
func (s *InjectStats) evictProjects() {
	for len(s.Projects) > MaxLedgerProjects {
		var oldest string
		var oldestAt string
		keys := make([]string, 0, len(s.Projects))
		for k := range s.Projects {
			keys = append(keys, k)
		}
		sort.Strings(keys) // 稳定次序:时间完全相同时先淘汰字典序靠前的
		for _, k := range keys {
			if oldest == "" || s.Projects[k].LastAt < oldestAt {
				oldest, oldestAt = k, s.Projects[k].LastAt
			}
		}
		delete(s.Projects, oldest)
	}
}

// LedgerFor 取某个作用域的账(不存在的项目桶 = 零值,不是错误)。
func (s InjectStats) LedgerFor(projectKey string) InjectLedger {
	if projectKey == "" {
		return s.User
	}
	return s.Projects[projectKey]
}
