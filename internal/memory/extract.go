// extract.go:自动生成记忆的**决策层**(规则闸 / 解析 / 分流 / 限流账本)。
//
// 为什么决策单独成层、且放在 internal:
//   - 规则闸决定"要不要花一次模型请求" —— 它是唯一的成本闸门,必须能表驱动测试;
//   - 分流决定"这条能不能直接生效" —— 它是"自动写记忆 = 静默提权"的唯一松动口,
//     白名单写死在这里,不给调用方自由裁量;
//   - 限流账本要能持久化、能被人查看(/memory status),不是内存计数器。
//
// 一条贯穿全层的口径:**提取失败 ≠ 本轮没有值得记的事实**。前者是故障,后者是正常结果,
// 它们在账上必须分开(见 Route 与 Skipped 字段)。
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 提取相关的阈值与上限(改这些值等于改"自动化能走多远",所以集中写死在这里)。
const (
	// AutoWriteMinConfidence 直写门槛。低于它的进候选(0.6 起),再低则丢弃。
	AutoWriteMinConfidence = 0.80
	// CandidateMinConfidence 候选门槛:低于它连人工确认都不值得占位。
	CandidateMinConfidence = 0.60
	// ExtractDailyLimit 每日提取请求上限(成本硬上限)。
	ExtractDailyLimit = 30
	// AutoWriteDailyLimit 每日直写条数上限(防"一次失控灌满记忆")。
	AutoWriteDailyLimit = 10
	// TotalSoftLimit 记忆条目软上限:超过后**只进候选**不再直写。
	TotalSoftLimit = 500
	// SessionCooldown 同一会话两次提取的最小间隔。用**时间**而不是回合数:
	// 回合序号不在 ctx 里(见 host-memory 的 Start 注释),而"每 3 分钟最多一次"达到的效果相同。
	SessionCooldown = 3 * time.Minute
)

// AutoWriteKinds 允许直接生效的类型 —— **这张表就是"静默提权"的边界**。
//
// 只覆盖三类可枚举、可核对、写错也能一眼看出来的事实:
//   - preference:用户偏好/技术选型/风格;
//   - convention:项目约定/命令/流程;
//   - decision:已拍板的决定(带理由)。
//
// pitfall(踩坑记录)与 env(路径/端口/版本)永远走候选:这两类最容易过期与记错,
// 而且错了最难发现 —— 自动化不该在这里替人做判断。
var AutoWriteKinds = map[string]bool{
	"preference": true,
	"convention": true,
	"decision":   true,
}

// ValidKinds 全部合法类型(分流与解析共用)。
var ValidKinds = map[string]bool{
	"preference": true,
	"convention": true,
	"decision":   true,
	"pitfall":    true,
	"env":        true,
}

// —— 规则闸 ——

// memorablePatterns 触发提取的规则(决定"要不要花这次钱")。
//
// 设计原则:宁可漏(不提取)也不要滥(每次收尾都烧一次模型)。所以每条都要求**结构**,
// 不接受单个模糊词 —— "以后再说"不该触发,"以后都用 pnpm"才该。
var memorablePatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"显式要求记住", regexp.MustCompile(`(记住|记一下|记下|别忘|备注一下|记到记忆)`)},
	{"长期指令", regexp.MustCompile(`(以后都|今后|从现在起|一律|统一用|不要再|别再|下次都)`)},
	{"偏好表达", regexp.MustCompile(`(我(更|比较)?(习惯|倾向|喜欢|不喜欢)|我一般用|我们一般|尽量用|优先用)`)},
	{"决策落定", regexp.MustCompile(`(就这么定|定了|决定用|采用|换成|改用|约定)`)},
	{"英文长期指令", regexp.MustCompile(`(?i)\b(always|never|from now on|remember to|prefer to|use .* instead)\b`)},
}

// memorableNegative 显式"记住"类的排除式:否定("不用记住")与引用("记住这句台词")
// 都不是在陈述可长期复用的事实 —— 这类误触发会白花一次模型请求,而且提取出来的东西
// 多半是玩笑。
var memorableNegative = regexp.MustCompile(`((不用|不必|别|不要)记住|记住(这|那)(句|个)?(台词|话|梗))`)

// IsMemorable 规则闸:返回是否命中与命中的规则名(空名 = 未命中)。
//
// 命中只说明"这句话像是在陈述可长期复用的偏好/约定/决定",不代表一定值得记 ——
// 值不值得由提取器判断(它会看到已有记忆,能判断是否重复)。
func IsMemorable(userText string) (bool, string) {
	t := strings.TrimSpace(userText)
	if t == "" {
		return false, ""
	}
	// 只在合理长度内判断:超长粘贴(日志/代码)里出现"记住"不是偏好表达。
	runes := []rune(t)
	if len(runes) > 2000 {
		t = string(runes[:2000])
	}
	if memorableNegative.MatchString(t) {
		return false, ""
	}
	for _, p := range memorablePatterns {
		if p.re.MatchString(t) {
			return true, p.name
		}
	}
	return false, ""
}

// —— 提取结果 ——

// Proposal 模型产出的一条记忆候选(严格 JSON 的字段)。
type Proposal struct {
	Text       string  `json:"text"`
	Kind       string  `json:"kind"`
	Confidence float64 `json:"confidence"`
	Cue        string  `json:"cue"` // 用户原话片段(可核对;不进记忆正文)
}

// extractOut 提取请求的响应外壳。
type extractOut struct {
	Items []Proposal `json:"items"`
}

// ParseProposals 解析提取结果。**严格**:非 JSON / 缺 items 都是错误(而不是"0 条")——
// 解析失败与"模型说没有新事实"是两回事,账上必须分开。
func ParseProposals(raw string) ([]Proposal, error) {
	s := strings.TrimSpace(raw)
	// 容错:模型偶尔会套代码围栏。剥掉它,但**不**尝试修复半个 JSON(那属于猜测)。
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s, "\n"); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	body := strings.TrimSpace(s)
	// 先确认 items 键存在:直接反序列化到 struct 时,缺字段与空数组无从区分,
	// 而"模型没按格式回"是失败,"回了空数组"是正常结果 —— 这两笔账不能混。
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return nil, fmt.Errorf("memory: 提取结果不是合法 JSON: %w", err)
	}
	if _, ok := probe["items"]; !ok {
		return nil, fmt.Errorf("memory: 提取结果缺 items 字段(输出格式不符)")
	}
	var out extractOut
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, fmt.Errorf("memory: 提取结果结构不符: %w", err)
	}
	return out.Items, nil
}

// —— 分流 ——

// Decision 一条候选的处置结果。
type Decision int

const (
	// DecisionWrite 直接写进记忆(仅在 type 白名单 + 高置信 + 非重复时)。
	DecisionWrite Decision = iota
	// DecisionCandidate 进候选池,等人工确认。
	DecisionCandidate
	// DecisionDrop 丢弃(不合规/凭据/任务态/重复/低置信)。
	DecisionDrop
)

func (d Decision) String() string {
	switch d {
	case DecisionWrite:
		return "write"
	case DecisionCandidate:
		return "candidate"
	default:
		return "drop"
	}
}

// secretPatterns 凭据特征。命中即整条丢弃,且**绝不落日志**(日志里只记 reason=secret)。
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(sk-[a-z0-9]{8,}|ghp_[a-z0-9]{10,}|xox[baprs]-[a-z0-9-]{10,})`),
	regexp.MustCompile(`(?i)\b(api[_ -]?key|access[_ -]?token|secret|password|passwd|私钥|口令|令牌)\s*[:=]\s*\S+`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

// transientPatterns 任务态特征:这类内容属于 todo/当前进度,不是长期事实。
var transientPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(当前正在|现在正在|下一步|接下来要|待办|TODO|临时|先这样)`),
}

// HasSecret 是否含凭据特征(导出给调用方:候选/直写两条路径都要过同一道闸)。
func HasSecret(s string) bool {
	for _, re := range secretPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// Route 决定一条提取结果的去向。sim = 与既有记忆的最大相似度(0 = 没算过)。
//
// 判定顺序是刻意排的:先看能不能**安全**写(凭据/任务态),再看是不是重复,
// 最后才看置信度与类型 —— 顺序错了会出现"高置信的密钥被直写"这类事故。
func Route(p Proposal, sim float64) (Decision, string) {
	text := strings.TrimSpace(p.Text)
	switch {
	case text == "":
		return DecisionDrop, "empty"
	case HasSecret(text):
		return DecisionDrop, "secret"
	case transient(text):
		return DecisionDrop, "transient"
	case !ValidKinds[p.Kind]:
		return DecisionDrop, "kind"
	case len([]rune(text)) > 400:
		// 单条太长 ⇒ 多半是把整段话搬进来,不是一条事实。
		return DecisionDrop, "too_long"
	case sim >= 0.90:
		return DecisionDrop, "dup"
	case p.Confidence < CandidateMinConfidence:
		return DecisionDrop, "low_confidence"
	case p.Confidence >= AutoWriteMinConfidence && AutoWriteKinds[p.Kind]:
		return DecisionWrite, ""
	default:
		return DecisionCandidate, ""
	}
}

// transient 任务态判定(与凭据分开返回,便于账上区分)。
func transient(s string) bool {
	for _, re := range transientPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// —— 限流账本 ——

// ExtractState 提取账本(落 $GAH_HOME/memory/extract-state.json,人与面板都能看)。
type ExtractState struct {
	Day         string           `json:"day"`          // YYYY-MM-DD(跨天自动归零)
	Requests    int              `json:"requests"`     // 当日提取请求数
	AutoWrites  int              `json:"auto_writes"`  // 当日直写条数
	Candidates  int              `json:"candidates"`   // 当日进候选条数
	LastBySess  map[string]int64 `json:"last_by_sess"` // 会话 id → 上次提取的 unix 秒
	LastSkipped string           `json:"last_skipped,omitempty"`
	LastRule    string           `json:"last_rule,omitempty"`
	// 累计计数(跨天保留,用于 /memory status 的"一共提取过多少次/失败多少次")。
	TotalRequests int `json:"total_requests"`
	TotalFailed   int `json:"total_failed"`
	TotalEmpty    int `json:"total_empty"`
}

// ExtractStatePath 账本路径。
func ExtractStatePath() string { return filepath.Join(Dir(), "extract-state.json") }

// LoadExtractState 读账本(不存在 = 零值,不是错误)。
func LoadExtractState() (ExtractState, error) {
	var st ExtractState
	raw, err := os.ReadFile(ExtractStatePath())
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		// 坏文件不阻塞提取(它是账本,不是记忆):当作空账本继续,并让调用方知道。
		return ExtractState{}, fmt.Errorf("memory: 提取账本损坏(按空账本继续): %w", err)
	}
	if st.LastBySess == nil {
		st.LastBySess = map[string]int64{}
	}
	return st, nil
}

// Save 落盘(原子写;失败只影响限流精度,不该让提取崩掉)。
func (s ExtractState) Save() error {
	if s.LastBySess == nil {
		s.LastBySess = map[string]int64{}
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(ExtractStatePath(), raw)
}

// roll 跨天归零(只重置当日计数;累计计数与冷却记录保留)。
func (s *ExtractState) roll(now time.Time) {
	today := now.Format("2006-01-02")
	if s.Day == today {
		return
	}
	s.Day = today
	s.Requests = 0
	s.AutoWrites = 0
	s.Candidates = 0
}

// Allow 限流判定:返回 (是否允许, 拒绝原因)。原因直接进账本与 status。
//
// 三道闸:全局每日请求数、单会话冷却、以及调用方另行检查的直写条数上限。
func (s *ExtractState) Allow(sessionID string, now time.Time) (bool, string) {
	s.roll(now)
	if s.Requests >= ExtractDailyLimit {
		return false, "quota"
	}
	if last, ok := s.LastBySess[sessionID]; ok {
		if now.Sub(time.Unix(last, 0)) < SessionCooldown {
			return false, "cooldown"
		}
	}
	return true, ""
}

// Consume 记账一次已发出的提取请求(即使随后失败也要记 —— 成本已经发生)。
func (s *ExtractState) Consume(sessionID string, now time.Time, rule string) {
	s.roll(now)
	s.Requests++
	s.TotalRequests++
	if s.LastBySess == nil {
		s.LastBySess = map[string]int64{}
	}
	s.LastBySess[sessionID] = now.Unix()
	s.LastRule = rule
	// 冷却记录只留最近的 200 个会话(防无限增长;超出的丢弃不影响正确性)。
	if len(s.LastBySess) > 200 {
		cut := now.Add(-24 * time.Hour).Unix()
		for k, v := range s.LastBySess {
			if v < cut {
				delete(s.LastBySess, k)
			}
		}
	}
}

// AutoWriteAllowed 当日直写额度是否还有(与总条数软上限一起判)。
func (s ExtractState) AutoWriteAllowed(totalItems int) (bool, string) {
	if s.AutoWrites >= AutoWriteDailyLimit {
		return false, "auto_write_quota"
	}
	if totalItems >= TotalSoftLimit {
		return false, "total_soft_limit"
	}
	return true, ""
}

// FormatState 一行账本摘要(/memory status 用)。
func (s ExtractState) FormatState() string {
	s2 := s
	s2.roll(time.Now())
	return fmt.Sprintf("提取账本:今日请求 %d/%d,直写 %d/%d,候选 %d;累计 请求 %d / 空 %d / 失败 %d%s",
		s2.Requests, ExtractDailyLimit, s2.AutoWrites, AutoWriteDailyLimit, s2.Candidates,
		s2.TotalRequests, s2.TotalEmpty, s2.TotalFailed,
		func() string {
			if s2.LastSkipped != "" {
				return ";最近一次未做成:" + s2.LastSkipped
			}
			return ""
		}())
}

// FormatConfidence 置信度打印(两位小数,不带多余的零)。
func FormatConfidence(c float64) string { return strconv.FormatFloat(c, 'f', 2, 64) }
