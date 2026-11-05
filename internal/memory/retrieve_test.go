package memory

// 检索层的行为契约(P0 自动调取)。
//
// 每条都钉在"检索坏了会静默失效"的方向上:
// 中文命不中(bigram 退化成空白分词就全废)、噪声 bigram 把真命中压到阈值下、
// 高频词淹没相关性、老记忆被衰减到检索不到、结果不稳定导致提示缓存每步失效。
//
// **反向验证**(2026-11-03 做过,记录在此以免后来当摆设):把 Tokenize 改成只按空白切分,
// TestRetrieveChineseHit / TestRetrieveIDFSuppressesCommonBigram 立即转红。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func day(offset int, base time.Time) string {
	return base.AddDate(0, 0, -offset).Format("2006-01-02")
}

func TestTokenizeChineseBigram(t *testing.T) {
	terms := Tokenize("怎么部署这个项目")
	has := func(want string) bool {
		for _, x := range terms {
			if x == want {
				return true
			}
		}
		return false
	}
	if !has("部署") {
		t.Fatalf("中文应按 bigram 切出「部署」,got %v", terms)
	}
	if has("怎么") {
		t.Fatalf("停用双字词应被滤掉,got %v", terms)
	}
	if has("这个") {
		t.Fatalf("停用双字词应被滤掉(这个),got %v", terms)
	}
}

func TestTokenizeASCIIAndMixed(t *testing.T) {
	terms := Tokenize("用 PNPM 装依赖 / npm")
	joined := strings.Join(terms, ",")
	for _, want := range []string{"pnpm", "npm", "依赖"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("应含 term %q,got %v", want, terms)
		}
	}
}

// TestRetrieveChineseHit 是最核心的一条:中文 query 命中中文条目。
func TestRetrieveChineseHit(t *testing.T) {
	now := time.Now()
	items := []Item{
		{ID: "u:1", Date: day(0, now), Content: "部署统一走 make deploy,不要手动 scp", Scope: ScopeUser},
		{ID: "u:2", Date: day(0, now), Content: "报告配色用蓝灰,不要渐变", Scope: ScopeUser},
	}
	res := Rank(items, "怎么部署", RetrieveOptions{Now: now})
	if len(res.Items) != 1 || !strings.Contains(res.Items[0].Content, "make deploy") {
		t.Fatalf("中文 query 应命中部署那条,got %+v (terms=%v)", res.Items, res.Terms)
	}
}

func TestRetrieveASCIIHitCaseInsensitive(t *testing.T) {
	now := time.Now()
	items := []Item{{ID: "u:1", Date: day(0, now), Content: "用 PNPM 装依赖", Scope: ScopeUser}}
	res := Rank(items, "pnpm", RetrieveOptions{Now: now})
	if len(res.Items) != 1 {
		t.Fatalf("大小写不敏感应命中,got %+v", res.Items)
	}
}

// TestRetrieveIDFSuppressesCommonBigram:高频词不能把无关条目顶上来。
func TestRetrieveIDFSuppressesCommonBigram(t *testing.T) {
	now := time.Now()
	var items []Item
	for i := 0; i < 10; i++ {
		items = append(items, Item{ID: "u:c" + string(rune('a'+i)), Date: day(0, now),
			Content: "项目结构说明第 " + string(rune('0'+i)) + " 条", Scope: ScopeUser})
	}
	items = append(items, Item{ID: "u:port", Date: day(0, now),
		Content: "网关端口统一用 18300", Scope: ScopeUser})
	hits, _ := RankScored(items, "项目网关端口", RetrieveOptions{Now: now})
	if len(hits) == 0 {
		t.Fatal("应至少命中端口那条")
	}
	if !strings.Contains(hits[0].Item.Content, "18300") {
		t.Fatalf("最相关的应是端口那条(高频「项目」不该压过它),got %q", hits[0].Item.Content)
	}
}

func TestRetrieveTimeDecayOrder(t *testing.T) {
	now := time.Now()
	items := []Item{
		{ID: "u:old", Date: day(400, now), Content: "部署走 make deploy", Scope: ScopeUser},
		{ID: "u:new", Date: day(0, now), Content: "部署改走 make ship", Scope: ScopeUser},
	}
	hits, _ := RankScored(items, "部署", RetrieveOptions{Now: now})
	if len(hits) != 2 {
		t.Fatalf("应命中两条,got %d", len(hits))
	}
	if hits[0].Item.ID != "u:new" {
		t.Fatalf("新的应排前,got %s(老条目不能被衰减到检索不到,但排序要让新的先)",
			hits[0].Item.ID)
	}
	if hits[1].Score < 0.1 {
		t.Fatalf("400 天前的老偏好仍应可检索(软衰减),got score=%f", hits[1].Score)
	}
}

func TestRetrieveThresholdSkipsIrrelevant(t *testing.T) {
	now := time.Now()
	items := []Item{{ID: "u:1", Date: day(0, now), Content: "报告配色用蓝灰", Scope: ScopeUser}}
	res := Rank(items, "怎么配置数据库连接池", RetrieveOptions{Now: now})
	if len(res.Items) != 0 || res.Total != 0 {
		t.Fatalf("无关 query 不该注入任何记忆(不塞最近 N 条),got %+v", res.Items)
	}
	if Render(res) != "" {
		t.Fatalf("空结果必须渲染成空串(不渲染空壳),got %q", Render(res))
	}
}

func TestRetrieveKindAndScopeWeight(t *testing.T) {
	now := time.Now()
	items := []Item{
		{ID: "u:env", Date: day(0, now), Content: "部署命令在 Makefile 里", Scope: ScopeUser, Kind: "env"},
		{ID: "p:conv", Date: day(0, now), Content: "部署统一 make deploy", Scope: ScopeProject, Kind: "convention"},
	}
	hits, _ := RankScored(items, "部署", RetrieveOptions{Now: now})
	if len(hits) != 2 {
		t.Fatalf("应命中两条,got %d", len(hits))
	}
	if hits[0].Item.ID != "p:conv" {
		t.Fatalf("项目级约定应排在用户级 env 前,got %s", hits[0].Item.ID)
	}
}

func TestRetrieveBudgetAndTopKTruncation(t *testing.T) {
	now := time.Now()
	var items []Item
	for i := 0; i < 5; i++ {
		items = append(items, Item{ID: "u:" + string(rune('a'+i)), Date: day(0, now),
			Content: "部署注意事项 " + strings.Repeat("细节", 10) + string(rune('0'+i)), Scope: ScopeUser})
	}
	res := Rank(items, "部署注意事项", RetrieveOptions{Now: now, TopK: 2})
	if len(res.Items) != 2 || !res.Truncated || res.Total != 5 {
		t.Fatalf("topK=2 应截断且如实报告总数,got len=%d truncated=%v total=%d",
			len(res.Items), res.Truncated, res.Total)
	}
	// 预算紧到一条都放不下时:不渲染半条。
	res = Rank(items, "部署注意事项", RetrieveOptions{Now: now, Budget: 10})
	if len(res.Items) != 0 || !res.Truncated {
		t.Fatalf("预算连一行都放不下应返回空且标记截断,got %+v", res.Items)
	}
}

// TestRetrieveRenderStable 守的是提示缓存:同一命中集合必须渲染出逐字节相同的文本。
func TestRetrieveRenderStable(t *testing.T) {
	now := time.Now()
	items := []Item{
		{ID: "u:a", Date: day(0, now), Content: "部署走 make deploy", Scope: ScopeUser},
		{ID: "u:b", Date: day(0, now), Content: "部署前先跑测试", Scope: ScopeUser},
	}
	r1 := Rank(items, "部署", RetrieveOptions{Now: now})
	r2 := Rank(items, "部署 deploy", RetrieveOptions{Now: now})
	if Render(r1) != Render(r2) {
		t.Fatalf("同一集合不同 query 措辞应渲染一致(否则每步改写前缀):\n%q\n%q",
			Render(r1), Render(r2))
	}
	// 时间推进导致分数变化,但集合没变 ⇒ 文本仍必须一致(文本不含分数)。
	r3 := Rank(items, "部署", RetrieveOptions{Now: now.Add(time.Hour)})
	if Render(r1) != Render(r3) {
		t.Fatalf("分数变化不得影响渲染文本(文本里不许有分数)")
	}
	// 输出按 ID 升序(集合不变则顺序不变)。
	if len(r1.Items) == 2 && !(r1.Items[0].ID < r1.Items[1].ID) {
		t.Fatalf("输出应按 ID 升序,got %s,%s", r1.Items[0].ID, r1.Items[1].ID)
	}
}

func TestRetrieveTwoSourcesMerged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "memory", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Append(UserPath(), "部署统一走 make deploy", ""); err != nil {
		t.Fatal(err)
	}
	if err := Append(ProjectPath("p1"), "本项目部署前必须跑 gate.sh", "s1"); err != nil {
		t.Fatal(err)
	}
	// 工具写的那一层(JSONL)
	jsonl := filepath.Join(Dir(), "p1.jsonl")
	lines := []string{
		`{"id":"t1","text":"部署日志落在 jail/deploy.log","ts":1700000000}`,
		`{"id":"t2","text":"这条已废弃","ts":1700000000,"del":true}`,
		`坏行容忍`,
	}
	if err := os.WriteFile(jsonl, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Retrieve("p1", "部署", RetrieveOptions{Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range res.Items {
		got = append(got, it.Content)
	}
	joined := strings.Join(got, " | ")
	for _, want := range []string{"make deploy", "gate.sh", "deploy.log"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("两源合并应含 %q,got %s", want, joined)
		}
	}
	if strings.Contains(joined, "这条已废弃") {
		t.Fatalf("墓碑行不该被调取,got %s", joined)
	}
}

func TestRetrieveMissingFilesAreEmptyNotError(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	res, err := Retrieve("nope", "部署", RetrieveOptions{})
	if err != nil {
		t.Fatalf("文件不存在不是错误,got %v", err)
	}
	if len(res.Items) != 0 {
		t.Fatalf("无记忆文件应返回空,got %+v", res.Items)
	}
}

func TestReadToolStoreLastWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	lines := []string{
		`{"id":"a","text":"旧文本","ts":1700000000}`,
		`{"id":"a","text":"新文本","ts":1700000100}`,
		`{"id":"b","text":"会删掉","ts":1700000000}`,
		`{"id":"b","text":"","ts":1700000200,"del":true}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	items, err := ReadToolStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Content != "新文本" {
		t.Fatalf("同 id 最后一行胜、墓碑跳过,got %+v", items)
	}
}

func TestSimilarityRange(t *testing.T) {
	if s := Similarity("部署统一走 make deploy", "部署统一走 make deploy"); s < 0.99 {
		t.Fatalf("同文相似度应≈1,got %f", s)
	}
	if s := Similarity("部署统一走 make deploy", "报告配色用蓝灰渐变"); s > 0.3 {
		t.Fatalf("无关文本相似度应低,got %f", s)
	}
	if s := Similarity("部署统一 make deploy 不要 scp", "部署统一 make deploy 禁止 scp"); s < 0.6 {
		t.Fatalf("近似文本应有较高相似度,got %f", s)
	}
}

func TestParseLineMetaAndLegacy(t *testing.T) {
	// 老行(无元数据):Kind/Confidence 零值,正文不含元数据。
	e, ok := ParseLine("- [2026-10-01] 报告配色用蓝灰(来源: 会话 s1)")
	if !ok || e.Content != "报告配色用蓝灰" || e.Kind != "" || e.Confidence != 0 {
		t.Fatalf("老行解析回归,got %+v ok=%v", e, ok)
	}
	// 带元数据的新行:元数据不进正文,且 Raw 保留原行(delete/rewrite 靠它)。
	line := FormatMeta("2026-11-03", "部署走 make deploy", "s2", "convention", 0.92)
	e, ok = ParseLine(line)
	if !ok {
		t.Fatalf("新行应可解析: %s", line)
	}
	if e.Kind != "convention" || e.Confidence < 0.91 || e.Confidence > 0.93 {
		t.Fatalf("元数据应解析出来,got kind=%q conf=%f", e.Kind, e.Confidence)
	}
	if strings.Contains(e.Content, "kind=") {
		t.Fatalf("元数据不该留在正文里,got %q", e.Content)
	}
	if e.Raw != line {
		t.Fatalf("Raw 必须保留原行(回写用),got %q", e.Raw)
	}
	// 没有元数据时 FormatMeta 与 Format 逐字节一致(写侧兼容)。
	if FormatMeta("2026-11-03", "x", "s", "", 0) != Format("2026-11-03", "x", "s") {
		t.Fatal("无元数据时 FormatMeta 应与 Format 完全一致")
	}
}
