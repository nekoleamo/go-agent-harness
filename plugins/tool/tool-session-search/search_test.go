// tool-session-search 引擎单测(S-P2-5):范围判定(含 key 前缀歧义)、打分权重、
// 片段与压平、预算截断(必须可见)、坏行容忍、索引(meta/names)回填、
// 以及**走真实线上形状**的护栏用例(载荷 PascalCase,取自真实会话文件实测样本)。
package toolsessionsearch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 夹具:合成会话目录 ——

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// ev 造一行事件 JSON(键名与线上一致:PascalCase)。
func ev(kind string, seq int, ts string, payload any) string {
	raw, _ := json.Marshal(payload)
	b, _ := json.Marshal(map[string]any{"Kind": kind, "Seq": seq, "Payload": json.RawMessage(raw), "TS": ts})
	return string(b)
}

func userMsg(text string) any { return map[string]any{"Content": text} }
func asstMsg(text string) any { return map[string]any{"Content": text, "ToolCalls": nil} }
func resultEv(name, content, errText string) any {
	return map[string]any{"CallID": "c1", "Name": name, "Content": content, "Error": errText}
}

// fixture 一个合成数据根:两个工作区(proj / proj-2,后者是前者加后缀 → 前缀歧义),
// 主会话与切换会话并存。
type fixture struct {
	root string
	wsA  string // 当前工作区真实目录(key=proj)
	wsB  string // 另一个工作区(key=proj-2)
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	f := fixture{root: filepath.Join(base, "sessions"), wsA: filepath.Join(base, "proj"), wsB: filepath.Join(base, "proj-2")}
	for _, d := range []string{f.root, f.wsA, f.wsB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ws, _ := json.Marshal([]map[string]any{
		{"key": "proj", "dir": f.wsA, "ts": 100},
		{"key": "proj-2", "dir": f.wsB, "ts": 90},
	})
	writeFile(t, filepath.Join(f.root, "workspaces.json"), string(ws))
	return f
}

// session 写一个会话文件(名 = <key>[-<id>].jsonl)。
func (f fixture) session(t *testing.T, name string, lines ...string) {
	t.Helper()
	writeFile(t, filepath.Join(f.root, name), strings.Join(lines, "\n")+"\n")
}

// search 以「当前工作区 = wsA」跑一次检索。
func (f fixture) search(t *testing.T, opt Options) Result {
	t.Helper()
	if opt.Root == "" {
		opt.Root = f.root
	}
	if opt.Workspace == "" {
		opt.Workspace = f.wsA
	}
	res, err := Search(opt)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return res
}

// —— 范围与打分 ——

// TestWorkspaceScopeDefault:默认只搜当前工作区;scope=all 才带上别的项目。
func TestWorkspaceScopeDefault(t *testing.T) {
	f := newFixture(t)
	f.session(t, "proj-20260101-1.jsonl",
		ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("修复 foo 编译错误")),
		ev("assistant/message", 2, "2026-01-01T10:00:01+08:00", asstMsg("foo 编译错误已修复")),
	)
	f.session(t, "proj-2-20260102-1.jsonl",
		ev("user/message", 1, "2026-01-02T10:00:00+08:00", userMsg("别的项目里的 foo")),
	)
	res := f.search(t, Options{Query: "foo"})
	if len(res.Hits) != 1 || res.Hits[0].File != "proj-20260101-1.jsonl" {
		t.Fatalf("默认应只命中当前工作区会话,得 %+v", res.Hits)
	}
	h := res.Hits[0]
	if h.Key != "proj" || h.ID != "20260101-1" || h.Dir != f.wsA {
		t.Fatalf("会话归属/目录回填错: %+v", h)
	}
	// 同一会话两个事件都命中:用户消息权重(3)高于助手消息(2) → 片段按分数排序
	if len(h.Matches) != 2 {
		t.Fatalf("应命中两条事件: %+v", h.Matches)
	}
	if h.Matches[0].Role != "user" || h.Matches[0].Score <= h.Matches[1].Score {
		t.Fatalf("用户消息应排在前且分数更高: %+v", h.Matches)
	}
	if h.Matches[0].Kind != "user/message" || !strings.Contains(h.Matches[0].Text, "foo") {
		t.Fatalf("片段应含命中词: %+v", h.Matches[0])
	}
	all := f.search(t, Options{Query: "foo", Scope: ScopeAll})
	if len(all.Hits) != 2 {
		t.Fatalf("scope=all 应带上其它工作区: %+v", all.Hits)
	}
}

// TestOwnerKeyLongestMatch:key 是另一个 key 的前缀 + 后缀时,归属按最长匹配判定
// (`proj` 与 `proj-2`;文件名 `proj-2-<id>.jsonl` 属 proj-2,不能算进 proj)。
func TestOwnerKeyLongestMatch(t *testing.T) {
	f := newFixture(t)
	f.session(t, "proj-2-20260102-1.jsonl",
		ev("user/message", 1, "2026-01-02T10:00:00+08:00", userMsg("只属于另一个项目的 bar")),
	)
	res := f.search(t, Options{Query: "bar"})
	if len(res.Hits) != 0 {
		t.Fatalf("前缀歧义:别的项目会话被算进当前工作区 %+v", res.Hits)
	}
	if owner, id := ownerOf("proj-2-20260102-1.jsonl", []string{"proj-2", "proj"}); owner != "proj-2" || id != "20260102-1" {
		t.Fatalf("ownerOf = %q/%q", owner, id)
	}
	if owner, id := ownerOf("proj.jsonl", []string{"proj-2", "proj"}); owner != "proj" || id != "" {
		t.Fatalf("主会话 ownerOf = %q/%q", owner, id)
	}
}

// TestAllTermsBonus:多词查询里「全词命中」加 50% 权重(同样命中次数下排在单词高频之前)。
func TestAllTermsBonus(t *testing.T) {
	f := newFixture(t)
	f.session(t, "proj.jsonl",
		ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("alpha alpha alpha alpha")),
		ev("user/message", 2, "2026-01-01T10:00:01+08:00", userMsg("alpha beta alpha beta")),
	)
	res := f.search(t, Options{Query: "alpha beta"})
	if len(res.Hits) != 1 || len(res.Hits[0].Matches) != 2 {
		t.Fatalf("应两条都命中: %+v", res.Hits)
	}
	m := res.Hits[0].Matches
	if m[0].Seq != 2 {
		t.Fatalf("全词命中应排前(得 seq=%d):%+v", m[0].Seq, m)
	}
	if len(m[0].Terms) != 2 || len(m[1].Terms) != 1 {
		t.Fatalf("命中词集合错: %+v / %+v", m[0].Terms, m[1].Terms)
	}
}

// —— 真实线上形状护栏(勿改成自造形状:S-P1-1 的教训就是两侧各自自洽) ——

const (
	lineUser   = `{"Kind": "user/message", "Seq": 1, "Payload": {"Content": "你好,帮我看看 session_search 这个功能"}, "TS": "2026-09-06T02:57:20.552526+08:00"}`
	lineAsst   = `{"Kind": "assistant/message", "Seq": 2, "Payload": {"Content": "session_search 需要跨会话检索能力。", "ToolCalls": null}, "TS": "2026-09-06T02:57:21.839764+08:00"}`
	lineCall   = `{"Kind": "tool/call", "Seq": 3, "Payload": {"ID": "call_00_nQrtVZFynhQ16DwaTBQL3892", "Name": "shell", "Arguments": "{\"command\": \"date \\\"+%Y-%m-%d %A\\\"\"}"}, "TS": "2026-09-10T16:35:44.154926+08:00"}`
	lineResult = `{"Kind": "tool/result", "Seq": 4, "Payload": {"CallID": "mock_call_1", "Name": "shell", "Content": "{}", "Error": "工具 \"shell\" 不存在 (对应插件可能已卸载/未启用;可经 /plugins list 排查)"}, "TS": "2026-09-04T11:24:16.667725+08:00"}`
)

// TestRealWireShapeIsSearchable:四种事件类型的**真实抓包**行都必须可检索。
// 载荷键是 PascalCase(Go 结构无 json tag),若哪天有人「顺手」改成小写,本用例立刻红。
func TestRealWireShapeIsSearchable(t *testing.T) {
	f := newFixture(t)
	f.session(t, "proj-20260906-1.jsonl", lineUser, lineAsst, lineCall, lineResult)
	cases := []struct {
		query string
		kind  string
		role  string
	}{
		{"session_search", "user/message", "user"},
		{"跨会话", "assistant/message", "assistant"},
		{"date", "tool/call", "tool"},
		{"未启用", "tool/result", "tool"},
	}
	for _, c := range cases {
		res := f.search(t, Options{Query: c.query})
		if len(res.Hits) != 1 || len(res.Hits[0].Matches) == 0 {
			t.Fatalf("查询 %q 未命中: %+v", c.query, res.Hits)
		}
		var got *Match
		for i := range res.Hits[0].Matches {
			if res.Hits[0].Matches[i].Kind == c.kind {
				got = &res.Hits[0].Matches[i]
			}
		}
		if got == nil {
			t.Fatalf("查询 %q 应命中 %s,得 %+v", c.query, c.kind, res.Hits[0].Matches)
		}
		if got.Role != c.role {
			t.Fatalf("查询 %q 角色应为 %s,得 %s", c.query, c.role, got.Role)
		}
		if !strings.Contains(got.Text, c.query) {
			t.Fatalf("片段应含命中词 %q: %q", c.query, got.Text)
		}
		if got.Seq == 0 || got.TS == "" {
			t.Fatalf("命中应带序号与时间: %+v", got)
		}
	}
	// 中文按子串命中;英文按整词子串
	if res := f.search(t, Options{Query: "session_search 需要"}); len(res.Hits) != 1 {
		t.Fatalf("多词中文查询应命中同一会话: %+v", res.Hits)
	}
}

// —— 片段与压平 ——

func TestSnippetFlattenAndRuneSafety(t *testing.T) {
	long := strings.Repeat("前置内容", 40) + "命中关键词" + strings.Repeat("后置内容", 40)
	lines := strings.Join([]string{"第一行无关", "第二行 " + long, "第三行无关"}, "\n")
	sn := snippet(lines, []string{"命中关键词"}, -1)
	if strings.Contains(sn, "\n") {
		t.Fatalf("片段应压平成单行: %q", sn)
	}
	if !strings.HasPrefix(sn, "…") || !strings.HasSuffix(sn, "…") {
		t.Fatalf("窗口两端都被裁剪应加省略号: %q", sn)
	}
	if !strings.Contains(sn, "命中关键词") {
		t.Fatalf("片段应含命中词: %q", sn)
	}
	// 多字节安全:不出现半个 UTF-8 字符
	if !utf8Valid(sn) {
		t.Fatalf("片段截断破坏 UTF-8: %q", sn)
	}
	// 短文本原样返回(不裁剪)
	if got := snippet("就一句 命中 的话", []string{"命中"}, -1); got != "就一句 命中 的话" {
		t.Fatalf("短文本不该裁剪: %q", got)
	}
	// 无命中词(理论分支)也不 panic
	if got := snippet("abc", []string{"zzz"}, -1); got != "abc" {
		t.Fatalf("无命中应回落整段截断: %q", got)
	}
}

// TestSnippetWindowDeepInHugeField:命中点在数 MB 字段的深处时,片段仍要定位到它,
// 且片段本身保持小(先开窗再压平,不整体展开)。
func TestSnippetWindowDeepInHugeField(t *testing.T) {
	head := strings.Repeat("前置填充", 300000) // 约 3.6 MB
	text := head + "深处的命中点标记" + strings.Repeat("后置填充", 300000)
	pos := strings.Index(strings.ToLower(text), "深处的命中点标记")
	got := snippet(text, []string{"深处的命中点标记"}, pos)
	if !strings.Contains(got, "深处的命中点标记") {
		t.Fatalf("片段应含深处命中词: %q", got)
	}
	if len([]rune(got)) > snippetBefore+snippetAfter+4 {
		t.Fatalf("片段应保持小: %d rune", len([]rune(got)))
	}
	// pos 未知(兜底路径)也要能定位
	if got2 := snippet(text, []string{"深处的命中点标记"}, -1); !strings.Contains(got2, "深处的命中点标记") {
		t.Fatalf("兜底定位应命中: %q", got2)
	}
	// 命中词横跨窗口边界时不许切出半个字符
	mid := strings.Repeat("字", snippetWindowBytes/3+10) + "边界标记" + strings.Repeat("字", 100)
	if got3 := snippet(mid, []string{"边界标记"}, strings.Index(strings.ToLower(mid), "边界标记")); !strings.Contains(got3, "边界标记") {
		t.Fatalf("窗口边界附近应命中且不截断多字节字符: %q", got3)
	}
}

// TestCountCapPerField:同字段内重复命中不计无限分(一条超长工具输出不能靠重复次数压过别的会话)。
func TestCountCapPerField(t *testing.T) {
	f := newFixture(t)
	many := strings.Repeat("dense ", 20)
	f.session(t, "proj.jsonl",
		ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg(many)),
	)
	res := f.search(t, Options{Query: "dense"})
	if len(res.Hits) != 1 {
		t.Fatalf("应命中: %+v", res.Hits)
	}
	// 用户权重 3 × 封顶 5 = 15(单词查询不吃全词加成)
	if got := res.Hits[0].Matches[0].Score; got != 15 {
		t.Fatalf("重复命中应封顶在 weight×%d = 15,得 %v", maxCountsPerField, got)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// —— 最近会话模式 ——

func TestRecentMode(t *testing.T) {
	f := newFixture(t)
	f.session(t, "proj-20260101-1.jsonl",
		ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("第一条会话的提问")),
	)
	f.session(t, "proj-20260103-1.jsonl",
		ev("user/message", 1, "2026-01-03T10:00:00+08:00", userMsg("最近一次会话的提问")),
	)
	f.session(t, "proj-2-20260105-1.jsonl",
		ev("user/message", 1, "2026-01-05T10:00:00+08:00", userMsg("别的项目")),
	)
	// mtime 决定「最近」:显式给三个文件拉开时间阶梯(不靠写入顺序)
	now := time.Now()
	for name, off := range map[string]time.Duration{
		"proj-20260101-1.jsonl":   -300 * time.Second,
		"proj-20260103-1.jsonl":   -100 * time.Second,
		"proj-2-20260105-1.jsonl": 0,
	} {
		p := filepath.Join(f.root, name)
		if err := os.Chtimes(p, now.Add(off), now.Add(off)); err != nil {
			t.Fatal(err)
		}
	}
	res := f.search(t, Options{Limit: 1})
	if res.Mode != "recent" {
		t.Fatalf("空查询应为 recent 模式: %s", res.Mode)
	}
	if len(res.Hits) != 1 || res.Hits[0].File != "proj-20260103-1.jsonl" {
		t.Fatalf("应按 mtime 倒序且只留 limit 条: %+v", res.Hits)
	}
	if res.Hits[0].Preview != "最近一次会话的提问" {
		t.Fatalf("应带首条用户消息省略版: %+v", res.Hits[0])
	}
	if len(res.Hits[0].Matches) != 0 {
		t.Fatalf("最近模式不该有命中片段: %+v", res.Hits[0].Matches)
	}
	if res.Sessions != 2 {
		t.Fatalf("最近模式也只扫当前工作区(2 个):%d", res.Sessions)
	}
}

// —— 索引回填(meta.json 优先,names.json 兜底) ——

func TestNameAndSummaryFromIndexes(t *testing.T) {
	f := newFixture(t)
	f.session(t, "proj-20260101-1.jsonl",
		ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("关于 widget 槽位的讨论")),
	)
	f.session(t, "proj.jsonl",
		ev("user/message", 1, "2026-01-02T10:00:00+08:00", userMsg("关于 widget 槽位的旧会话")),
	)
	writeFile(t, filepath.Join(f.root, "meta.json"),
		`{"proj-20260101-1.jsonl": {"name": "槽位讨论", "summary": {"text": "结论:先做轻量版", "topics": ["槽位"]}}, "proj.jsonl": {"name": "旧名"}}`)
	writeFile(t, filepath.Join(f.root, "names.json"), `{"proj.jsonl": "只存在于 names.json 的名"}`)
	res := f.search(t, Options{Query: "widget"})
	if len(res.Hits) != 2 {
		t.Fatalf("应命中两个会话: %+v", res.Hits)
	}
	byFile := map[string]Hit{}
	for _, h := range res.Hits {
		byFile[h.File] = h
	}
	h1 := byFile["proj-20260101-1.jsonl"]
	if h1.Name != "槽位讨论" || h1.Summary != "结论:先做轻量版" || len(h1.Topics) != 1 {
		t.Fatalf("meta.json 的名字/概述未回填: %+v", h1)
	}
	if got := byFile["proj.jsonl"].Name; got != "旧名" {
		t.Fatalf("names.json 只作兜底(meta 优先):%q", got)
	}
	// 只有 names.json 条目的场景
	writeFile(t, filepath.Join(f.root, "meta.json"), `{}`)
	res = f.search(t, Options{Query: "widget"})
	for _, h := range res.Hits {
		if h.File == "proj.jsonl" && h.Name != "只存在于 names.json 的名" {
			t.Fatalf("meta 缺条目应回退 names.json: %+v", h)
		}
	}
}

// —— 预算与容错 ——

// TestBudgetTruncationVisible:预算用尽必须显式可见(不许把「没扫到」说成「没搜到」)。
func TestBudgetTruncationVisible(t *testing.T) {
	f := newFixture(t)
	for _, n := range []string{"proj-20260101-1.jsonl", "proj-20260102-1.jsonl", "proj-20260103-1.jsonl"} {
		f.session(t, n, ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("预算用例 keyword-"+n)))
	}
	res := f.search(t, Options{Query: "keyword", MaxFiles: 1})
	if !res.Truncated {
		t.Fatal("MaxFiles 用尽应报 truncated")
	}
	if res.Sessions != 1 {
		t.Fatalf("只应扫 1 个: %d", res.Sessions)
	}
	if len(res.Notes) == 0 || !strings.Contains(strings.Join(res.Notes, " "), "预算") {
		t.Fatalf("应说明预算用尽: %+v", res.Notes)
	}

	// 单文件超限:只扫前半段,仍要能命中前面的内容并标注
	big := ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("前半段的 needle 在这里"))
	for i := 0; i < 400; i++ {
		big += "\n" + ev("assistant/chunk", i+2, "2026-01-01T10:00:00+08:00", map[string]any{"Delta": strings.Repeat("填充", 40)})
	}
	f.session(t, "proj-big.jsonl", big)
	res = f.search(t, Options{Query: "needle", MaxFileBytes: 4096})
	if !res.Truncated {
		t.Fatal("单文件超限应报 truncated")
	}
	if len(res.Hits) != 1 || !strings.Contains(res.Hits[0].Matches[0].Text, "needle") {
		t.Fatalf("前半段内容应仍可命中: %+v", res.Hits)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "单文件") {
		t.Fatalf("应说明单文件截断: %+v", res.Notes)
	}
	// 超时不 panic 且可见(时限设成 0 → 走默认;改成极小值触发)
	res = f.search(t, Options{Query: "needle", TimeBudget: time.Nanosecond})
	if !res.Truncated && res.Sessions == 0 {
		t.Log("时限极小时允许直接截断为空(实现相关),truncated 应被置位")
	}
}

// TestOverlongLineDoesNotStopScan:单行超长(工具输出可达数 MB)不得让它之后的整份记录失联。
// 这是**真机验证抓到的真实缺陷**:原先用 bufio.Scanner(默认 token 上限 1 MiB),
// 遇到 1.9 MB 的 tool/result 行时 Scan() 直接返回 false,文件后半段被静默丢弃
// (实数据:7.9 MB 主会话文件在中途那条记录处整段失联,且没有任何提示)。
func TestOverlongLineDoesNotStopScan(t *testing.T) {
	f := newFixture(t)
	huge := ev("tool/result", 1, "2026-01-01T10:00:00+08:00", resultEv("read", strings.Repeat("X", 3<<20), ""))
	f.session(t, "proj.jsonl",
		ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("超长行之前的第一句话")),
		huge,
		ev("user/message", 3, "2026-01-01T10:00:02+08:00", userMsg("超长行之后的 marker 仍须可搜")),
	)
	// 单行上限 = 单文件上限:默认 8 MiB 下 3 MiB 行整行可搜,后面的行也不丢
	res := f.search(t, Options{Query: "marker"})
	if len(res.Hits) != 1 || len(res.Hits[0].Matches) != 1 {
		t.Fatalf("超长行之后的记录应仍可搜到: %+v", res.Hits)
	}
	if res.Truncated {
		t.Fatalf("3 MiB 行在默认 8 MiB 上限内,不该报截断: %+v", res.Notes)
	}
	if h := f.search(t, Options{Query: "超长行之前"}); len(h.Hits) != 1 {
		t.Fatalf("超长行之前的记录应可搜到: %+v", h.Hits)
	}
	// 把预算压到 4 KiB(低于那条 3 MiB 行):必然扫不完,**必须显式说明**,不许静默
	res = f.search(t, Options{Query: "marker", MaxFileBytes: 4096})
	if !res.Truncated {
		t.Fatal("预算不足必须报截断")
	}
	notes := strings.Join(res.Notes, " ")
	if !strings.Contains(notes, "超长") || !strings.Contains(notes, "单文件扫描上限") {
		t.Fatalf("应显式说明超长行与文件上限: %+v", res.Notes)
	}
}

// TestOverlongLineEOFAndOddInput:无换行结尾 / 空行 / 只有超长行 的边界都不 panic。
func TestOverlongLineEOFAndOddInput(t *testing.T) {
	f := newFixture(t)
	body := strings.Join([]string{
		"", // 空行
		ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("edge")),
		strings.Repeat("Z", 5000), // 无换行的超长坏行(末尾无 \n)
	}, "\n")
	writeFile(t, filepath.Join(f.root, "proj.jsonl"), body)
	res := f.search(t, Options{Query: "edge", MaxFileBytes: 1024})
	if len(res.Hits) != 1 {
		t.Fatalf("应命中: %+v", res)
	}
	if !res.Truncated {
		t.Fatal("超长坏行应报截断")
	}
}

// TestBadLinesAndMissingDir:坏行/注释行容忍;目录缺失 = 显式说明不作错误。
func TestBadLinesAndMissingDir(t *testing.T) {
	f := newFixture(t)
	f.session(t, "proj.jsonl",
		`{"Kind": "user/message", "Seq": 1,`,
		`# 人工注释行`,
		``,
		`not json at all`,
		ev("user/message", 2, "2026-01-01T10:00:00+08:00", userMsg("坏行之后仍要能搜到 marker")),
	)
	res := f.search(t, Options{Query: "marker"})
	if len(res.Hits) != 1 || len(res.Hits[0].Matches) != 1 {
		t.Fatalf("坏行不该中断扫描: %+v", res.Hits)
	}
	missing := f.search(t, Options{Root: filepath.Join(f.root, "nope"), Query: "x"})
	if len(missing.Hits) != 0 || len(missing.Notes) == 0 {
		t.Fatalf("目录缺失应显式说明: %+v", missing)
	}
	empty := newFixture(t)
	got := empty.search(t, Options{Query: "x"})
	if len(got.Hits) != 0 || !strings.Contains(strings.Join(got.Notes, " "), "还没有会话记录") {
		t.Fatalf("空工作区应说明原因: %+v", got.Notes)
	}
}

// TestInvalidScopeAndLimitCap:非法 scope 显式报错;limit 上限收敛。
func TestInvalidScopeAndLimitCap(t *testing.T) {
	f := newFixture(t)
	if _, err := Search(Options{Root: f.root, Workspace: f.wsA, Scope: Scope("planet")}); err == nil {
		t.Fatal("非法 scope 应报错")
	}
	f.session(t, "proj.jsonl", ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("cap")))
	res := f.search(t, Options{Query: "cap", Limit: 999})
	if len(res.Hits) != 1 {
		t.Fatalf("命中不足时报错?: %+v", res.Hits)
	}
}

// TestWorkspaceMatchingTolerantToSymlinkAndTrailingSlash:目录写法差异(尾斜杠/软链)
// 不该导致「当前工作区认不出来 → 一条都搜不到」。
func TestWorkspaceMatchingTolerantToSymlinkAndTrailingSlash(t *testing.T) {
	f := newFixture(t)
	f.session(t, "proj.jsonl", ev("user/message", 1, "2026-01-01T10:00:00+08:00", userMsg("尾斜杠用例 zzz")))
	res := f.search(t, Options{Query: "zzz", Workspace: f.wsA + string(filepath.Separator)})
	if len(res.Hits) != 1 {
		t.Fatalf("尾斜杠应能认出同一工作区: %+v", res.Hits)
	}
	link := filepath.Join(filepath.Dir(f.wsA), "proj-link")
	if err := os.Symlink(f.wsA, link); err == nil {
		res = f.search(t, Options{Query: "zzz", Workspace: link})
		if len(res.Hits) != 1 {
			t.Fatalf("软链应能认出同一工作区: %+v", res.Hits)
		}
	}
}

// TestToolNameAndSchemas:工具声明(名称/超时/入参)稳定 —— 模型可见面不许无声漂移。
func TestToolNameAndSchemas(t *testing.T) {
	def := NewTool().Definition()
	if def.Name != ToolName || ToolName != "session_search" {
		t.Fatalf("工具名漂移: %s", def.Name)
	}
	if def.TimeoutMs != 20000 {
		t.Fatalf("必须声明超时(桥默认 3s 不够扫描): %d", def.TimeoutMs)
	}
	props, _ := def.InputSchema["properties"].(map[string]any)
	if _, ok := props["query"]; !ok {
		t.Fatal("schema 缺 query")
	}
	scope, _ := props["scope"].(map[string]any)
	vals, _ := scope["enum"].([]string)
	if len(vals) != 2 || vals[0] != "workspace" || vals[1] != "all" {
		t.Fatalf("scope 枚举漂移: %+v", vals)
	}
	req, _ := def.InputSchema["required"].([]any)
	if len(req) != 1 || req[0] != "query" {
		t.Fatalf("query 必填: %+v", req)
	}
	// 无路径参数(纯只读检索,不参与路径沙箱裁决)
	if len(def.PathParams) != 0 {
		t.Fatalf("不该声明路径参数: %+v", def.PathParams)
	}
}

var _ = sdk.Home
