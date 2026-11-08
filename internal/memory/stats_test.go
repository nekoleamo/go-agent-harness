package memory

// 累计注入账本的契约。
//
// 钉的是"数字会悄悄算错"的地方:分母是什么、什么算命中、回退与零命中要不要分开、
// 坏文件会不会把累计数清零。

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestInjectLedgerApplySemantics(t *testing.T) {
	var st InjectStats
	at := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	st = st.Apply("", InjectSample{Mode: "fallback", Fallback: true, StableBytes: 800, At: at})           // 没查询(回退)
	st = st.Apply("", InjectSample{Mode: "per_step", At: at})                                             // 检索了但零命中
	st = st.Apply("", InjectSample{Mode: "per_step", Items: 2, TailBytes: 400, StableBytes: 800, At: at}) // 命中
	st = st.Apply("", InjectSample{Mode: "per_step", Items: 1, TailBytes: 900, Truncated: true, At: at})
	st = st.Apply("", InjectSample{Mode: "fallback", Fallback: true, At: at})
	st = st.Apply("", InjectSample{Mode: "per_step", Items: 3, TailBytes: 100, At: at})

	led := st.LedgerFor("")
	if led.Requests != 6 {
		t.Fatalf("分母应是请求数 6, got %d", led.Requests)
	}
	if led.Hits != 3 {
		t.Fatalf("命中应是 3(只有真的追加了条目的才算), got %d", led.Hits)
	}
	if led.Fallbacks != 2 {
		t.Fatalf("回退应是 2(没查询 + 显式 fallback;零命中不算回退), got %d", led.Fallbacks)
	}
	if led.TailBytes != 1400 || led.TailBytesMax != 900 {
		t.Fatalf("尾部字节累计/峰值不对: %+v", led)
	}
	if led.Truncated != 1 {
		t.Fatalf("截断计数不对: %d", led.Truncated)
	}
	// 稳定块记「最近一次」而不是累计:累计会把同一个块重复计价。
	if led.StableBytes != 800 {
		t.Fatalf("稳定块应是最新一次的值, got %d", led.StableBytes)
	}
	if led.HitRate() != 0.5 {
		t.Fatalf("命中率应是 3/6=0.5, got %v", led.HitRate())
	}
	if led.AvgTailBytes() != 1400/3 {
		t.Fatalf("平均尾部字节不对: %d", led.AvgTailBytes())
	}
	if led.CostPerRequest() != 800+1400/6 {
		t.Fatalf("每请求成本应是稳定块 + 摊薄尾部, got %d", led.CostPerRequest())
	}
}

func TestInjectLedgerZeroDenominator(t *testing.T) {
	var led InjectLedger
	if led.HitRate() != 0 || led.AvgTailBytes() != 0 || led.CostPerRequest() != 0 {
		t.Fatal("零请求时所有比值必须是 0(不能是 NaN,它会把面板数字变成 NaN%)")
	}
	if !strings.Contains(led.Format(), "还没有记账") {
		t.Fatalf("空账本文案不对: %s", led.Format())
	}
}

func TestInjectLedgerPerProjectBuckets(t *testing.T) {
	var st InjectStats
	st = st.Apply("proj-a", InjectSample{Mode: "per_step", Items: 1, TailBytes: 100})
	st = st.Apply("proj-b", InjectSample{Mode: "per_step"})
	st = st.Apply("", InjectSample{Mode: "per_step", Items: 1, TailBytes: 50})

	if st.LedgerFor("proj-a").Requests != 1 || st.LedgerFor("proj-a").Hits != 1 {
		t.Fatalf("项目桶应各自独立: %+v", st.Projects)
	}
	if st.LedgerFor("proj-b").Hits != 0 {
		t.Fatalf("零命中的项目桶不该记命中: %+v", st.Projects["proj-b"])
	}
	if st.LedgerFor("").TailBytes != 50 {
		t.Fatalf("用户级桶独立: %+v", st.User)
	}
	if got := st.LedgerFor("nope").Requests; got != 0 {
		t.Fatalf("不存在的项目桶应是零值, got %d", got)
	}
}

// 项目桶必须有上限:项目键是工作区路径派生的,换工作区/换机就长一个新桶。
func TestInjectLedgerEvictsOldestProject(t *testing.T) {
	var st InjectStats
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < MaxLedgerProjects+5; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		st = st.Apply("p"+itoa3(i), InjectSample{Mode: "per_step", Items: 1, TailBytes: 10, At: at})
	}
	if len(st.Projects) != MaxLedgerProjects {
		t.Fatalf("桶数应被压到上限 %d, got %d", MaxLedgerProjects, len(st.Projects))
	}
	// 最早那些必须被淘汰,最新那个必须还在。
	if _, ok := st.Projects["p000"]; ok {
		t.Fatal("最久未动的桶应被淘汰")
	}
	last := "p" + itoa3(MaxLedgerProjects+4)
	if _, ok := st.Projects[last]; !ok {
		t.Fatalf("最新的桶不该被淘汰: %s", last)
	}
}

func itoa3(n int) string {
	if n < 10 {
		return "00" + string(rune('0'+n))
	}
	if n < 100 {
		return "0" + string(rune('0'+n/10)) + string(rune('0'+n%10))
	}
	return string(rune('0'+n/100)) + string(rune('0'+(n/10)%10)) + string(rune('0'+n%10))
}

func TestInjectStatsRoundTrip(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	var st InjectStats
	st = st.Apply("proj", InjectSample{Mode: "per_step", Items: 2, TailBytes: 300, StableBytes: 700})
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadInjectStats()
	if err != nil {
		t.Fatal(err)
	}
	if got.LedgerFor("proj").Hits != 1 || got.LedgerFor("proj").TailBytes != 300 {
		t.Fatalf("往返不对: %+v", got)
	}
	// 不存在 = 零值,不是错误。
	if err := os.Remove(InjectStatsPath()); err != nil {
		t.Fatal(err)
	}
	if empty, err := LoadInjectStats(); err != nil || empty.LedgerFor("proj").Requests != 0 {
		t.Fatalf("文件不存在应是零值, got %+v err=%v", empty, err)
	}
}

// 坏账本:按零值继续,但**返回 error**(否则累计数被悄悄清零却没人知道)。
func TestInjectStatsCorruptFile(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(InjectStatsPath(), []byte("{不是 json"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := LoadInjectStats()
	if err == nil {
		t.Fatal("坏文件应返回 error(便于日志发现)")
	}
	if st.LedgerFor("").Requests != 0 {
		t.Fatalf("坏文件应按零值继续, got %+v", st)
	}
}
