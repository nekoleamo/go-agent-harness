// retrieve.go:跨会话记忆的检索层(P0 自动调取)。
//
// 为什么需要它:注入原先只有「全量 + 时间倒序 + 2 KiB 预算」一种形态 —— 记忆一旦超过预算,
// 就是**最新**的进上下文,而不是**相关**的进。recall 工具是大小写不敏感子串匹配,对中文
// 几乎检索不到(中文 query 里没有空格,"整句子串"命中率极低)。
//
// 三条纪律:
//  1. **零依赖**:分词/打分/相似度全在这里自写 —— 运行时依赖 0 是红线,不引分词器/向量库。
//  2. **两源合并**:手写 markdown(user.md / projects/<key>.md)与工具写 JSONL
//     (`<key>.jsonl`)在这一层汇成同一个 Item 视图。此前模型 remember 下的东西永远不会
//     自动出现在上下文里,只能靠它自己主动 recall。
//  3. **输出可稳定复现**:同样的命中集合必须渲染出**逐字节相同**的文本,与命中分数无关
//     —— 否则每步检索都会改写系统提示前缀,提示缓存整段失效(见 Retrieve.Render 注释)。
package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 检索默认参数(可由 host-memory 的 data 覆盖)。
const (
	// DefaultRetrieveBudget 自动调取的注入预算(4 KiB)。比全量注入的 2 KiB 宽:
	// 现在放的只有**相关**条目,不是"能塞几条塞几条"。
	DefaultRetrieveBudget = 4 * 1024
	// DefaultTopK 一次最多注入几条。
	DefaultTopK = 6
	// DefaultMinScore 相关度下限。低于它宁可这一轮不注入,也不拿无关记忆污染提示。
	DefaultMinScore = 0.35
)

// 记忆作用域。
const (
	ScopeUser    = "user"
	ScopeProject = "project"
)

// 时间衰减参数。**不是**越老越接近 0:偏好类事实一年后仍然成立,把它的分数衰减到
// 检索不到就是功能缺失。所以用「软衰减」(floor + (1-floor)·半衰期曲线),衰减只改变
// 排序与阈值边缘,不把老记忆变成死数据。env(路径/端口/版本)例外 —— 这类真的会过期,
// 用更低的 floor 与更短的半衰期。
const (
	defaultHalfLifeDays = 45.0
	envHalfLifeDays     = 30.0
	defaultDecayFloor   = 0.6
	envDecayFloor       = 0.3
	// defaultConfidence 老条目没有 conf 元数据时的缺省置信度(既有的手工条目都属这一类)。
	defaultConfidence = 0.7
)

// kindWeights 类型权重:偏好与约定最该被调取,环境事实最弱(也最易过期)。
var kindWeights = map[string]float64{
	"preference": 1.2,
	"convention": 1.2,
	"decision":   1.1,
	"pitfall":    1.0,
	"env":        0.9,
}

// Item 检索/注入用的统一视图(markdown 条目与 JSONL 条目都投影成它)。
type Item struct {
	ID         string  // 稳定标识:markdown=scope:内容哈希;JSONL=原 id
	Date       string  // YYYY-MM-DD
	Kind       string  // preference|convention|decision|pitfall|env(空 = 老条目)
	Confidence float64 // 0..1;0 = 未标注(按 defaultConfidence 计)
	Content    string
	Source     string // 来源会话 id(可空)
	Scope      string // ScopeUser|ScopeProject
}

// RetrieveOptions 检索参数;零值 = 默认(预算 4 KiB / top6 / 阈值 0.35 / 现在)。
type RetrieveOptions struct {
	Budget   int
	TopK     int
	MinScore float64
	Now      time.Time
}

func (o RetrieveOptions) norm() RetrieveOptions {
	if o.Budget <= 0 {
		o.Budget = DefaultRetrieveBudget
	}
	if o.TopK <= 0 {
		o.TopK = DefaultTopK
	}
	if o.MinScore <= 0 {
		o.MinScore = DefaultMinScore
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	return o
}

// RetrieveResult 检索结果。
type RetrieveResult struct {
	Items     []Item   // 选中的条目(**按 ID 升序**:见 Render)
	Total     int      // 相关条目总数(> len(Items) 说明被预算/topK 截断)
	Truncated bool     // 预算或 topK 造成截断
	Terms     []string // 实际参与打分的 term(诊断)
	Ignored   []string // 被丢弃的 term(停用词/库中一个都不含),诊断用
	Query     string
	Budget    int // 生效预算
	Bytes     int // 选中条目渲染后的字节数
}

// Retrieve 读盘 + 检索:用户级 markdown、项目级 markdown、项目级 JSONL(工具写的)三处。
// projectKey 空 ⇒ 只读用户级(无项目上下文的路径,如 /memory 诊断)。
// 读盘失败**显式返回错误**(不当作"没有记忆"—— 那会让一次 IO 故障伪装成空记忆)。
func Retrieve(projectKey, query string, opt RetrieveOptions) (RetrieveResult, error) {
	opt = opt.norm()
	userItems, err := loadMarkdownItems(UserPath(), ScopeUser)
	if err != nil {
		return RetrieveResult{}, fmt.Errorf("memory: 读用户级记忆: %w", err)
	}
	var projItems []Item
	if strings.TrimSpace(projectKey) != "" {
		projItems, err = loadMarkdownItems(ProjectPath(projectKey), ScopeProject)
		if err != nil {
			return RetrieveResult{}, fmt.Errorf("memory: 读项目级记忆: %w", err)
		}
		fromTool, err := ReadToolStore(filepath.Join(Dir(), projectKey+".jsonl"))
		if err != nil {
			return RetrieveResult{}, fmt.Errorf("memory: 读工具记忆: %w", err)
		}
		projItems = append(projItems, fromTool...)
	}
	items := append(userItems, projItems...)
	return Rank(items, query, opt), nil
}

// Scored 一条命中及其分数(诊断面与测试用;注入文本**不含**分数,见 renderLine)。
type Scored struct {
	Item  Item
	Score float64
}

// Rank 在给定条目集合上检索(纯函数:测试与"调用方已读盘"的路径都走它)。
func Rank(items []Item, query string, opt RetrieveOptions) RetrieveResult {
	hits, res := scoreAll(items, query, opt)
	if len(hits) == 0 {
		return res
	}
	opt = opt.norm()
	res.Total = len(hits)

	// 预算 + topK:按分数降序装入(最相关的先占预算)。
	used := 0
	for _, h := range hits {
		if len(res.Items) >= opt.TopK {
			res.Truncated = true
			break
		}
		n := len(renderLine(h.Item))
		if used+n > opt.Budget {
			res.Truncated = true
			break
		}
		used += n
		res.Items = append(res.Items, h.Item)
	}
	res.Bytes = used
	// **输出按 ID 升序**而不是分数序:分数随 query/时间微调,集合不变时若按分数排,
	// 文本就会跟着变 —— 每步检索 = 每步改写系统提示前缀 = 提示缓存整段失效。
	sort.SliceStable(res.Items, func(i, j int) bool { return res.Items[i].ID < res.Items[j].ID })
	return res
}

// RankScored 返回**按分数降序**的完整命中集(不应用 topK/预算):诊断面看"为什么选中这几条"。
func RankScored(items []Item, query string, opt RetrieveOptions) ([]Scored, RetrieveResult) {
	return scoreAll(items, query, opt)
}

// scoreAll 打分与排序(纯计算,不碰预算与渲染)。
func scoreAll(items []Item, query string, opt RetrieveOptions) ([]Scored, RetrieveResult) {
	opt = opt.norm()
	res := RetrieveResult{Query: query, Budget: opt.Budget}
	terms := Tokenize(query)
	if len(terms) == 0 || len(items) == 0 {
		return nil, res
	}

	lowered := make([]string, len(items))
	for i, it := range items {
		lowered[i] = strings.ToLower(it.Content)
	}
	// 文档频次:一次扫描统计,不建索引(记忆是数百条量级,全量扫描是微秒级)。
	df := make(map[string]int, len(terms))
	for _, t := range terms {
		n := 0
		for _, body := range lowered {
			if strings.Contains(body, t) {
				n++
			}
		}
		df[t] = n
	}
	// 库里一条都不含的 term 直接从分母剔除:否则一串噪声 bigram(如"么部")会把
	// 覆盖率的分母抬高,真命中反被压到阈值以下 —— 那不是"更严格",是检索坏了。
	idf := make(map[string]float64, len(terms))
	var totalIDF float64
	kept := make([]string, 0, len(terms))
	for _, t := range terms {
		if df[t] == 0 {
			res.Ignored = append(res.Ignored, t)
			continue
		}
		v := 1 / (1 + math.Log(1+float64(df[t])))
		idf[t] = v
		totalIDF += v
		kept = append(kept, t)
	}
	res.Terms = kept
	if totalIDF <= 0 {
		return nil, res
	}

	var hits []Scored
	for i, it := range items {
		var cov float64
		for _, t := range kept {
			if strings.Contains(lowered[i], t) {
				cov += idf[t]
			}
		}
		if cov <= 0 {
			continue
		}
		score := (cov / totalIDF) *
			kindWeight(it.Kind) *
			scopeWeight(it.Scope) *
			confidenceWeight(it.Confidence) *
			decay(it, opt.Now)
		if score < opt.MinScore {
			continue
		}
		hits = append(hits, Scored{Item: it, Score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Item.ID < hits[j].Item.ID // 同分定序:结果不随遍历顺序漂移
	})
	return hits, res
}

// Render 把选中条目渲染成注入块正文(**不含**抬头与围栏,调用方负责)。
// 空集合返回空串(不渲染空壳)。
func Render(res RetrieveResult) string {
	if len(res.Items) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, it := range res.Items {
		sb.WriteString(renderLine(it))
		sb.WriteByte('\n')
	}
	return sb.String()
}

// renderLine 与既有注入格式一致(`  · ` 前缀,host-memory 的 countLines 依赖它):
// `  · [日期] 内容`。**不输出分数/命中数** —— 那是随 query 浮动的量,写进文本就会破坏
// 前缀稳定性(见 Rank 尾部注释)。
func renderLine(it Item) string {
	return "  · [" + it.Date + "] " + oneLine(it.Content)
}

// oneLine 把多行内容压成一行(注入块按行渲染,多行内容会破坏 `  · ` 计数与预算估算)。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// kindWeight 类型权重;未知/空 = 1.0(老条目不受罚)。
func kindWeight(kind string) float64 {
	if w, ok := kindWeights[kind]; ok {
		return w
	}
	return 1.0
}

// scopeWeight 本项目的事比通用偏好更贴题。
func scopeWeight(scope string) float64 {
	if scope == ScopeProject {
		return 1.25
	}
	return 1.0
}

// confidenceWeight 置信度只做小幅修正(0.7→0.91,1.0→1.0),不让低置信条目直接消失:
// 是否该存在由治理面决定,检索层只负责排序。
func confidenceWeight(conf float64) float64 {
	if conf <= 0 {
		conf = defaultConfidence
	}
	if conf > 1 {
		conf = 1
	}
	return 0.7 + 0.3*conf
}

// decay 软时间衰减(见文件头常量注释)。
func decay(it Item, now time.Time) float64 {
	d, err := time.Parse("2006-01-02", it.Date)
	if err != nil {
		return 1
	}
	days := now.Sub(d).Hours() / 24
	if days < 0 {
		days = 0
	}
	floor, hl := defaultDecayFloor, defaultHalfLifeDays
	if it.Kind == "env" {
		floor, hl = envDecayFloor, envHalfLifeDays
	}
	return floor + (1-floor)*math.Pow(0.5, days/hl)
}

// —— 分词 ——

// stopWords 查询侧停用词。中文按**单字**与**常见双字虚词**两张表:
// bigram 分词会产生"么部""为什"这类跨词噪声,双字表负责在成型前就掐掉它们。
var stopWords = map[string]bool{
	// 中文单字
	"的": true, "了": true, "是": true, "在": true, "和": true, "与": true, "就": true,
	"不": true, "都": true, "也": true, "还": true, "很": true, "太": true, "更": true,
	"最": true, "多": true, "个": true, "这": true, "那": true, "我": true, "你": true,
	"他": true, "她": true, "它": true, "们": true, "吗": true, "呢": true, "吧": true,
	"啊": true, "呀": true, "哦": true, "嗯": true, "把": true, "被": true, "给": true,
	"用": true, "要": true, "能": true, "会": true, "有": true, "没": true, "下": true,
	"上": true, "里": true, "中": true, "对": true, "从": true, "到": true,
	// 中文双字虚词
	"怎么": true, "么样": true, "什么": true, "为什": true, "如何": true, "哪些": true,
	"哪个": true, "哪里": true, "是否": true, "可以": true, "需要": true, "应该": true,
	"以及": true, "但是": true, "如果": true, "因为": true, "所以": true, "现在": true,
	"这个": true, "那个": true, "我们": true, "你们": true, "他们": true, "我的": true,
	"你的": true, "帮我": true, "一下": true, "有没": true, "没有": true, "然后": true,
	"就是": true, "还是": true, "或者": true, "而且": true, "一个": true, "上次": true,
	"以前": true, "之前": true,
	// 英文
	"the": true, "a": true, "an": true, "is": true, "are": true, "was": true, "were": true,
	"be": true, "been": true, "to": true, "of": true, "in": true, "on": true, "at": true,
	"for": true, "with": true, "and": true, "or": true, "but": true, "if": true, "then": true,
	"than": true, "that": true, "this": true, "these": true, "those": true, "it": true,
	"its": true, "as": true, "by": true, "from": true, "how": true, "why": true, "what": true,
	"when": true, "where": true, "who": true, "which": true, "do": true, "does": true,
	"did": true, "can": true, "could": true, "should": true, "would": true, "will": true,
	"shall": true, "may": true, "might": true, "must": true, "not": true, "no": true,
	"yes": true, "i": true, "you": true, "we": true, "they": true, "he": true, "she": true,
	"my": true, "your": true, "our": true, "their": true, "me": true, "us": true,
}

// Tokenize 查询分词:ASCII 按词切分,中文按 **bigram** 滑窗。去重、去停用词、丢掉
// 单个字母(单字母英文没有区分力)。
//
// 为什么是 bigram 而不是外部分词器:运行时依赖 0 是红线。bigram 的代价是短 query 召回偏宽,
// 由相关度阈值收(见 Rank 的 MinScore)。
func Tokenize(q string) []string {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil
	}
	var (
		out  []string
		seen = map[string]bool{}
	)
	add := func(t string) {
		if t == "" || stopWords[t] || seen[t] {
			return
		}
		if len([]rune(t)) < 1 {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	var (
		ascii strings.Builder
		cjk   []rune
	)
	flushASCII := func() {
		w := ascii.String()
		ascii.Reset()
		if len(w) >= 2 {
			add(w)
		}
	}
	flushCJK := func() {
		n := len(cjk)
		switch {
		case n == 0:
		case n == 1:
			add(string(cjk[0]))
		default:
			for i := 0; i+1 < n; i++ {
				add(string(cjk[i : i+2]))
			}
		}
		cjk = cjk[:0]
	}
	for _, r := range q {
		switch {
		case isASCIIWord(r):
			flushCJK()
			ascii.WriteRune(r)
		case isCJK(r):
			flushASCII()
			cjk = append(cjk, r)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return out
}

func isASCIIWord(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == '/'
}

// isCJK 覆盖中日韩统一表意文字(基本区 + 扩展 A)与常用兼容区。
func isCJK(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF || r >= 0x3400 && r <= 0x4DBF || r >= 0xF900 && r <= 0xFAFF
}

// Similarity 两段文本的 bigram 余弦相似度(0..1):去重/归并共用一份实现。
// 用计数向量而不只是集合 —— "部署部署" 与 "部署" 应当不同。
func Similarity(a, b string) float64 {
	va, vb := bigramCounts(a), bigramCounts(b)
	if len(va) == 0 || len(vb) == 0 {
		return 0
	}
	var dot, na, nb float64
	for t, c := range va {
		na += float64(c * c)
		if d, ok := vb[t]; ok {
			dot += float64(c * d)
		}
	}
	for _, c := range vb {
		nb += float64(c * c)
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// bigramCounts 文本的 bigram 计数(不做停用词过滤:相似度看的是字面结构)。
func bigramCounts(s string) map[string]int {
	runes := []rune(strings.ToLower(strings.TrimSpace(s)))
	out := map[string]int{}
	if len(runes) == 1 {
		out[string(runes)] = 1
		return out
	}
	for i := 0; i+1 < len(runes); i++ {
		out[string(runes[i:i+2])]++
	}
	return out
}

// —— 读盘投影 ——

// entriesToItems 把 (已 Active 过的)条目投影成 Item(不读盘)。
func entriesToItems(es []Entry, scope string) []Item {
	out := make([]Item, 0, len(es))
	for _, e := range es {
		out = append(out, Item{
			ID:         stableID(scope, e.Content),
			Date:       e.Date,
			Kind:       e.Kind,
			Confidence: e.Confidence,
			Content:    e.Content,
			Source:     e.Source,
			Scope:      scope,
		})
	}
	return out
}

// loadMarkdownItems 读一个 markdown 记忆文件并投影(文件不存在 = 空,不是错误)。
func loadMarkdownItems(path, scope string) ([]Item, error) {
	es, err := Read(path)
	if err != nil {
		return nil, err
	}
	// 被取代的旧条目**不参与检索**:否则新旧两条事实会同时进上下文,模型无从取舍。
	return entriesToItems(Active(es), scope), nil
}

// Snapshot 一次读盘的完整视图。
//
// 为什么需要它:自动调取在主循环的**每一步**都要用同一批数据做三件事 ——
// 检索、与系统提示稳定块去重、报稳定块条数。分开各读一次盘就是每步 7~9 次文件读
// (实测过),而它们本来就该是同一份快照。
type Snapshot struct {
	Items   []Item          // 全部条目(markdown 已 Active + JSONL)
	Stable  map[string]bool // 稳定块实际渲染的内容(小写 trim;去重用)
	StableN int             // 稳定块条数(观测面)
}

// LoadSnapshot 读三处来源一次,并给出稳定块的去重集合与条数。
func LoadSnapshot(projectKey string, stableBudget int) (Snapshot, error) {
	user, err := Read(UserPath())
	if err != nil {
		return Snapshot{}, fmt.Errorf("memory: 读用户级记忆: %w", err)
	}
	user = Active(user)
	var proj []Entry
	if strings.TrimSpace(projectKey) != "" {
		proj, err = Read(ProjectPath(projectKey))
		if err != nil {
			return Snapshot{}, fmt.Errorf("memory: 读项目级记忆: %w", err)
		}
		proj = Active(proj)
	}
	items := append(entriesToItems(user, ScopeUser), entriesToItems(proj, ScopeProject)...)
	if strings.TrimSpace(projectKey) != "" {
		fromTool, err := ReadToolStore(filepath.Join(Dir(), projectKey+".jsonl"))
		if err != nil {
			return Snapshot{}, fmt.Errorf("memory: 读工具记忆: %w", err)
		}
		items = append(items, fromTool...)
	}
	return Snapshot{
		Items:   items,
		Stable:  InjectedContents(user, proj, stableBudget),
		StableN: StableCount(user, proj, stableBudget),
	}, nil
}

// stableID 内容哈希派生的稳定标识:删掉别的条目不会改它的 ID ⇒ 命中集合不变时
// 渲染结果也不变(见 Rank 的排序注释)。
func stableID(scope, content string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(strings.TrimSpace(content))))
	return fmt.Sprintf("%s:%08x", scope, h.Sum32())
}

// toolEntry JSONL 记忆行(tool-memory 的落盘格式;此处**只读解析**,不 import 那个插件)。
type toolEntry struct {
	ID      string   `json:"id"`
	Text    string   `json:"text"`
	Tags    []string `json:"tags,omitempty"`
	TS      int64    `json:"ts"`
	Deleted bool     `json:"del,omitempty"`
}

// ReadToolStore 读工具写的 JSONL 记忆(同 id 最后一行胜、跳过墓碑行与坏行)。
// 文件不存在 = 空,不是错误;坏行容忍(文件是人可编辑的)。
func ReadToolStore(path string) ([]Item, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	byID := map[string]toolEntry{}
	var order []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e toolEntry
		if err := json.Unmarshal(line, &e); err != nil || e.ID == "" {
			continue
		}
		if _, seen := byID[e.ID]; !seen {
			order = append(order, e.ID)
		}
		byID[e.ID] = e
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(order))
	for _, id := range order {
		e := byID[id]
		if e.Deleted || strings.TrimSpace(e.Text) == "" {
			continue
		}
		out = append(out, Item{
			ID:         "j:" + e.ID,
			Date:       time.Unix(e.TS, 0).Format("2006-01-02"),
			Content:    e.Text,
			Scope:      ScopeProject,
			Confidence: defaultConfidence,
		})
	}
	return out, nil
}

// —— 汇入检索的内存缓存(供 host-memory 诊断面复用) ——

// LoadAll 读取三处来源的全部条目(不检索;诊断/面板/命令用)。读盘失败显式返回。
func LoadAll(projectKey string) ([]Item, error) {
	userItems, err := loadMarkdownItems(UserPath(), ScopeUser)
	if err != nil {
		return nil, err
	}
	out := userItems
	if strings.TrimSpace(projectKey) == "" {
		return out, nil
	}
	proj, err := loadMarkdownItems(ProjectPath(projectKey), ScopeProject)
	if err != nil {
		return nil, err
	}
	out = append(out, proj...)
	fromTool, err := ReadToolStore(filepath.Join(Dir(), projectKey+".jsonl"))
	if err != nil {
		return nil, err
	}
	return append(out, fromTool...), nil
}
