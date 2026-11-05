package memory

// 自动生成记忆的决策层契约(P1)。
//
// 钉的是"会长期害人"的三个方向:
//   ① 规则闸滥触发 → 每次收尾都烧一次模型请求;
//   ② 分流把不该直写的写进记忆(凭据/任务态/低置信) → 错误长期生效;
//   ③ 提取失败被记成"本轮没有新事实" → 用户看到一切正常,实际功能没跑。
//
// 反向验证(2026-11-03 实跑):把 Route 的 HasSecret 判定删掉 ⇒ TestRouteDropsSecret 转红;
// 把"库中不存在 term 剔除"之外的分流顺序打乱(先看置信度再看凭据)⇒ 同上用例转红。

import (
	"strings"
	"testing"
	"time"
)

func TestIsMemorablePositive(t *testing.T) {
	cases := []string{
		"以后都用 pnpm 装依赖",
		"记住:报告里的图表用蓝灰配色",
		"我习惯用 vim 改配置",
		"这个项目的接口层统一用 struct 返回",
		"就这么定,交付前先跑 gate.sh",
		"always use pnpm for installing deps",
	}
	for _, c := range cases {
		if ok, _ := IsMemorable(c); !ok {
			t.Fatalf("应命中规则闸: %q", c)
		}
	}
}

func TestIsMemorableNegative(t *testing.T) {
	cases := []string{
		"以后再说吧",
		"帮我看看这个函数在哪",
		"当前进度到哪了",
		"不用记住这句",
		"记住这句台词:<台词>",
		strings.Repeat("日志行\n", 800), // 超长粘贴:截断后仍是日志,不含规则
		"",
	}
	for _, c := range cases {
		if ok, r := IsMemorable(c); ok {
			t.Fatalf("不该命中规则闸(%s): %q", r, c)
		}
	}
}

func TestParseProposalsStrict(t *testing.T) {
	ok := `{"items":[{"text":"部署走 make deploy","kind":"convention","confidence":0.9,"cue":"统一用"}]}`
	ps, err := ParseProposals(ok)
	if err != nil || len(ps) != 1 || ps[0].Kind != "convention" {
		t.Fatalf("合法 JSON 应解析,got %+v err=%v", ps, err)
	}
	// 代码围栏容错(模型常套),但不猜半个 JSON。
	fenced := "```json\n" + ok + "\n```"
	if ps, err := ParseProposals(fenced); err != nil || len(ps) != 1 {
		t.Fatalf("围栏应剥掉,got %+v err=%v", ps, err)
	}
	if _, err := ParseProposals("这不是 JSON"); err == nil {
		t.Fatal("非 JSON 必须是错误(不能当成 0 条 —— 那是把解析失败伪装成\"没有新事实\")")
	}
	if _, err := ParseProposals(`{"foo":1}`); err == nil {
		// 缺 items 字段 → 严格解析失败(不是空数组)
		t.Fatal("缺 items 字段应报错")
	}
}

func TestRouteDecisions(t *testing.T) {
	cases := []struct {
		name string
		p    Proposal
		sim  float64
		want Decision
		why  string
	}{
		{"偏好高置信直写", Proposal{Text: "以后都用 pnpm 装依赖", Kind: "preference", Confidence: 0.92}, 0, DecisionWrite, ""},
		{"约定高置信直写", Proposal{Text: "交付前先跑 gate.sh", Kind: "convention", Confidence: 0.85}, 0, DecisionWrite, ""},
		{"决定高置信直写", Proposal{Text: "接口层统一返回 struct", Kind: "decision", Confidence: 0.9}, 0, DecisionWrite, ""},
		{"坑永远进候选", Proposal{Text: "go test 不认 -covermode=set 写计数器", Kind: "pitfall", Confidence: 0.99}, 0, DecisionCandidate, ""},
		{"环境事实永远进候选", Proposal{Text: "网关端口 18300", Kind: "env", Confidence: 0.99}, 0, DecisionCandidate, ""},
		{"中置信进候选", Proposal{Text: "喜欢蓝灰配色", Kind: "preference", Confidence: 0.7}, 0, DecisionCandidate, ""},
		{"低置信丢弃", Proposal{Text: "可能是用 pnpm", Kind: "preference", Confidence: 0.4}, 0, DecisionDrop, "low_confidence"},
		{"重复丢弃", Proposal{Text: "部署走 make deploy", Kind: "convention", Confidence: 0.95}, 0.95, DecisionDrop, "dup"},
		{"凭据丢弃", Proposal{Text: "api_key: sk-abcdefgh1234", Kind: "convention", Confidence: 0.99}, 0, DecisionDrop, "secret"},
		{"私钥丢弃", Proposal{Text: "-----BEGIN RSA PRIVATE KEY-----", Kind: "env", Confidence: 0.9}, 0, DecisionDrop, "secret"},
		{"任务态丢弃", Proposal{Text: "当前正在改记忆插件", Kind: "decision", Confidence: 0.9}, 0, DecisionDrop, "transient"},
		{"非法 kind 丢弃", Proposal{Text: "随便", Kind: "other", Confidence: 0.9}, 0, DecisionDrop, "kind"},
		{"空内容丢弃", Proposal{Text: "   ", Kind: "preference", Confidence: 0.9}, 0, DecisionDrop, "empty"},
		{"超长丢弃", Proposal{Text: strings.Repeat("长", 401), Kind: "preference", Confidence: 0.9}, 0, DecisionDrop, "too_long"},
	}
	for _, c := range cases {
		dec, why := Route(c.p, c.sim)
		if dec != c.want || (c.why != "" && why != c.why) {
			t.Fatalf("%s: 期望 %v/%s,got %v/%s", c.name, c.want, c.why, dec, why)
		}
	}
}

func TestHasSecret(t *testing.T) {
	for _, s := range []string{
		"token: ghp_abcdefghijkl",
		"password = hunter2",
		"私钥 -----BEGIN PRIVATE KEY-----",
		"sk-1234567890abc",
	} {
		if !HasSecret(s) {
			t.Fatalf("应判为凭据: %q", s)
		}
	}
	if HasSecret("部署走 make deploy") {
		t.Fatal("正常内容不该被判凭据")
	}
}

func TestExtractStateAllowAndCooldown(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	var st ExtractState
	if ok, why := st.Allow("s1", now); !ok {
		t.Fatalf("空账本应允许,why=%s", why)
	}
	st.Consume("s1", now, "显式要求记住")
	if ok, why := st.Allow("s1", now.Add(time.Minute)); ok || why != "cooldown" {
		t.Fatalf("同会话 3 分钟内应冷却,got %v/%s", ok, why)
	}
	if ok, _ := st.Allow("s2", now); !ok {
		t.Fatal("不同会话互不影响冷却")
	}
	if ok, _ := st.Allow("s1", now.Add(SessionCooldown+time.Second)); !ok {
		t.Fatal("冷却结束后应允许")
	}
	// 每日上限
	st.Requests = ExtractDailyLimit
	if ok, why := st.Allow("s9", now); ok || why != "quota" {
		t.Fatalf("到日上限应拒绝,got %v/%s", ok, why)
	}
	// 跨天归零
	if ok, _ := st.Allow("s9", now.AddDate(0, 0, 1)); !ok {
		t.Fatal("跨天应归零当日计数")
	}
}

func TestExtractStateAutoWriteQuota(t *testing.T) {
	var st ExtractState
	if ok, _ := st.AutoWriteAllowed(0); !ok {
		t.Fatal("新账本应允许直写")
	}
	st.AutoWrites = AutoWriteDailyLimit
	if ok, why := st.AutoWriteAllowed(0); ok || why != "auto_write_quota" {
		t.Fatalf("到日直写上限应拒绝,got %v/%s", ok, why)
	}
	var st2 ExtractState
	if ok, why := st2.AutoWriteAllowed(TotalSoftLimit); ok || why != "total_soft_limit" {
		t.Fatalf("超过总量软上限只进候选,got %v/%s", ok, why)
	}
}

func TestExtractStateRoundTrip(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	var st ExtractState
	now := time.Now()
	st.Consume("s1", now, "长期指令")
	st.TotalEmpty = 2
	st.TotalFailed = 1
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadExtractState()
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests != 1 || got.TotalEmpty != 2 || got.TotalFailed != 1 || got.LastBySess["s1"] == 0 {
		t.Fatalf("账本往返不对: %+v", got)
	}
	line := got.FormatState()
	if !strings.Contains(line, "今日请求 1/") || !strings.Contains(line, "失败 1") {
		t.Fatalf("账本摘要应含当日与累计口径: %s", line)
	}
}

// 坏账本不该阻塞提取(它是账本不是记忆),但要能被发现。
func TestLoadExtractStateCorruptDoesNotBlock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := writeFileAtomic(ExtractStatePath(), []byte("{坏 JSON")); err != nil {
		t.Fatal(err)
	}
	st, err := LoadExtractState()
	if err == nil {
		t.Fatal("坏账本应返回错误(便于日志发现)")
	}
	if st.Requests != 0 {
		t.Fatalf("坏账本应按空账本继续,got %+v", st)
	}
}
