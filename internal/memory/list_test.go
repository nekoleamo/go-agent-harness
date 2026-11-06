package memory

// 展示与筛选的契约(P2):过滤条件、被取代条目的隐藏、"过滤中"的计数口径。
//
// 钉的两件会"悄悄错"的事:
//   ① 被取代的旧条目仍然出现 ⇒ 新旧两条矛盾事实同时进上下文/面板;
//   ② 过滤把序号打乱 ⇒ `/memory rm <序号>` 删错条(序号必须始终来自同一份展示序)。

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkEntry(date, content, kind string, conf float64, source string) Entry {
	return Entry{Date: date, Content: content, Kind: kind, Confidence: conf, Source: source}
}

func TestContentIDStable(t *testing.T) {
	a, b := ContentID("部署统一走 make deploy"), ContentID("  部署统一走 MAKE deploy  ")
	if a != b {
		t.Fatalf("同一内容(大小写/空白归一)应同 ID: %s vs %s", a, b)
	}
	if ContentID("甲") == ContentID("乙") {
		t.Fatal("不同内容应不同 ID")
	}
}

func TestActiveHidesSuperseded(t *testing.T) {
	old := mkEntry("2026-10-01", "部署统一走 make deploy", "convention", 0.9, "s1")
	// 新条目声明取代旧条目(单向标记,旧行一个字都不改)
	newer := mkEntry("2026-11-03", "部署改走 make ship", "convention", 0.9, "s2")
	newer.SupersededBy = ContentID(old.Content)

	live := Active([]Entry{old, newer})
	if len(live) != 1 || live[0].Content != newer.Content {
		t.Fatalf("被取代的旧条目应隐藏,got %+v", live)
	}
	// 没有 superseded_by 时原样返回(不能误删)
	if got := Active([]Entry{old, newer}); len(got) != 1 {
		t.Fatalf("回归:只应有新条目,got %+v", got)
	}
}

func TestFilterEntriesByKindAndAutoAndStale(t *testing.T) {
	now := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	entries := []Entry{
		mkEntry("2026-11-03", "以后都用 pnpm", "preference", 0.9, "s1"),     // 自动 + 新
		mkEntry("2026-11-02", "交付前跑 gate.sh", "convention", 0.92, "s1"), // 自动
		mkEntry("2025-01-01", "老的手工偏好", "", 0, ""),                      // 手工 + 超期
	}
	if got := FilterEntries(entries, ListOptions{Now: now, Kind: "preference"}); len(got) != 1 ||
		got[0].Kind != "preference" {
		t.Fatalf("按类型过滤,got %+v", got)
	}
	if got := FilterEntries(entries, ListOptions{Now: now, AutoOnly: true}); len(got) != 2 {
		t.Fatalf("只看自动写入应 2 条,got %d", len(got))
	}
	got := FilterEntries(entries, ListOptions{Now: now, StaleDays: 180})
	if len(got) != 1 || !strings.Contains(got[0].Content, "老的手工偏好") {
		t.Fatalf("超期过滤应只留 2025 那条,got %+v", got)
	}
	// 组合条件
	if got := FilterEntries(entries, ListOptions{Now: now, Kind: "preference", AutoOnly: true}); len(got) != 1 {
		t.Fatalf("组合条件,got %+v", got)
	}
	// 零值 = 全部(且是展示序:新的在前)
	all := FilterEntries(entries, ListOptions{Now: now})
	if len(all) != 3 || all[0].Date != "2026-11-03" {
		t.Fatalf("零值应返回全部且新的在前,got %+v", all)
	}
}

func TestDisplayLinesBadgeAndNumbering(t *testing.T) {
	entries := []Entry{
		mkEntry("2026-11-01", "旧的约定", "convention", 0.9, "s1"),
		mkEntry("2026-11-03", "新的约定", "convention", 0.95, "s2"),
	}
	lines := DisplayLines(entries, ListOptions{})
	if len(lines) != 2 {
		t.Fatalf("应 2 行,got %v", lines)
	}
	if !strings.HasPrefix(lines[0], "1. [2026-11-03] 新的约定") {
		t.Fatalf("序号应基于展示序(新的在前),got %q", lines[0])
	}
	if !strings.Contains(lines[0], "(类型 convention 0.95)") {
		t.Fatalf("应带类型与置信度徽标,got %q", lines[0])
	}
	if !strings.Contains(lines[0], "(来源: 会话 s2)") {
		t.Fatalf("应带来源,got %q", lines[0])
	}
	// 过滤后序号从 1 重新开始(与 /memory rm 的语义一致)
	only := DisplayLines(entries, ListOptions{Kind: "convention", StaleDays: 180})
	if len(only) != 0 {
		t.Fatalf("超期过滤应筛掉这两条新条目,got %v", only)
	}
	old := []Entry{mkEntry("2025-01-01", "很老的一条", "", 0, "")}
	if lines := DisplayLines(old, ListOptions{StaleDays: 180}); len(lines) != 1 ||
		!strings.HasPrefix(lines[0], "1. [2025-01-01] 很老的一条") {
		t.Fatalf("过滤后序号应从 1 开始,got %v", lines)
	}
}

func TestBestMatch(t *testing.T) {
	items := []Item{
		{Content: "部署统一走 make deploy"},
		{Content: "报告配色用蓝灰"},
	}
	sim, target := BestMatch("部署统一走 make deploy 脚本", items)
	if sim < 0.8 || !strings.Contains(target, "make deploy") {
		t.Fatalf("应命中部署那条,got sim=%f target=%q", sim, target)
	}
	if sim, target := BestMatch("完全无关的一句话", items); sim > 0.3 || target != "" {
		t.Fatalf("无关文本不该命中,got %f %q", sim, target)
	}
}

func TestAppendMetaFullRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.md") // 跨平台:不拼 "/" (Windows 上虽能跑,但规范要求 Join)
	old := "部署统一走 make deploy"
	if err := AppendMeta(path, old, "s1", "convention", 0.9); err != nil {
		t.Fatal(err)
	}
	es, err := Read(path)
	if err != nil || len(es) != 1 {
		t.Fatalf("应有 1 条: %v %+v", err, es)
	}
	// 更新:新条声明取代旧条
	if err := AppendMetaFull(path, "部署改走 make ship", "s2", "convention", 0.9,
		ContentID(old)); err != nil {
		t.Fatal(err)
	}
	es, err = Read(path)
	if err != nil || len(es) != 2 {
		t.Fatalf("应有 2 条: %v %+v", err, es)
	}
	if es[1].SupersededBy != ContentID(old) {
		t.Fatalf("新条应解析出 superseded_by,got %+v", es[1])
	}
	if es[1].SupersededBy == ContentID(es[1].Content) {
		t.Fatal("superseded_by 指向自己 = 更新把它自己藏了")
	}
	live := Active(es)
	if len(live) != 1 || live[0].Content != "部署改走 make ship" {
		t.Fatalf("旧条应被隐藏,got %+v", live)
	}
}
