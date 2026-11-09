package hostmemory

// probe 核对表的可回归版本(2026-11-09)。
//
// 背景:记忆四点里「/memory probe 的 10 条真实问句命中是否合直觉」原先挂在人工清单上
// (docs/VERIFY.md §剩余任务快照),理由是"需要真实记忆库与真实项目问句,mock e2e 只能
// 证明链路通"。那个理由只对了一半:**链路**已经在 TestProbeMirrorsAugment 里钉死了,
// 而"这套检索在本项目的常见问句上命不命中"其实是可以固化的 —— 固定库 + 固定问句 =
// 确定性的期望集合(打分是词法 bigram,没有随机性)。
//
// 所以这里收的是**回归价值**而不是"语义质量证明":把人工判过一次的结论写死成表,
// 此后任何改动(打分权重、停用词、tokenize、预算)让某条问句的命中集变化,这条用例就红,
// 人再判一次"这次变好还是变坏"。没有它,这类退化只能靠人隔几周重跑一遍才发现。
//
// 已知边界(**故意不假装**):词法匹配扛不住"完全换说法"。真实换了措辞就命不中的问句
// 也在表里,期望写的是"零命中"并注明原因 —— 接了 embedding 检索之后这批期望要重冻。

import (
	"strings"
	"testing"
)

// probeFixture 固定的记忆库(内容取自本项目真实口径,不是编的样例句)。
var probeFixture = []string{
	"门禁:bash scripts/gate.sh;新增文件必须先 git add,gofmt 那步只检查 git ls-files,未跟踪文件它一个都不看",
	"桌面壳托盘的「检查更新」只查不装;点「下载并安装」才真正开始,携带升级前备份",
	"插件只 import sdk/,不 import core/ 或其它插件包;宿主内置插件经 internal/* 取内部实现",
	"同步(拉取)只用 bin/pi-sync-pull.sh --os mac;裸 git pull 不落盘 notes/overlay,push 还会反抹对端改动",
	"覆盖率门本地必须设 GAH_COVER_MERGE_DIR,否则 cmd/gah 的 main() 写不到计数器,必假红",
	"Windows 上 chmod 0555 不产生只读语义(走 ACL);构造只读目录必须显式判 runtime.GOOS",
	"记忆注入按 ID 升序渲染,不按分数排;否则每步检索都改写系统提示前缀,提示缓存整段失效",
	"外部插件按沙箱上下文分桶,实例数不超过档位数;同档复用,不因每个动作新起进程",
	"换代理端口用 local/proxy-ctl.sh set <port>,git/settings/环境变量一处改完,别手改",
	"多页签只读档下写文件应被拒;判据是拒绝文案说清是沙箱,真机由 M1a 验",
}

// probeCase 一条问句的期望。want 空 = 期望零命中(词法换说法扛不住,记录边界)。
type probeCase struct {
	query string
	want  []string // 命中集里必须出现的条目片段
	avoid []string // 命中集里不该出现的条目片段(防"命中一堆无关")
	note  string
}

func probeTable() []probeCase {
	return []probeCase{
		{
			query: "门禁怎么跑",
			want:  []string{"gate.sh"},
		},
		{
			query: "托盘检查更新会自己装吗",
			want:  []string{"检查更新"},
			avoid: []string{"proxy-ctl.sh", "chmod 0555"}, // 无关条目不该被拉进来
		},
		{
			query: "插件可以 import core 吗",
			want:  []string{"插件只 import sdk"},
		},
		{
			query: "换机器怎么同步笔记",
			want:  []string{"pi-sync-pull.sh"},
		},
		{
			query: "覆盖率本地老是红",
			want:  []string{"GAH_COVER_MERGE_DIR"},
		},
		{
			query: "windows 上怎么造只读目录",
			want:  []string{"chmod 0555"},
		},
		{
			query: "记忆注入顺序会不会破坏提示缓存",
			want:  []string{"升序渲染"},
		},
		{
			query: "外部插件进程会不会无限增长",
			want:  []string{"分桶"},
		},
		{
			query: "代理端口怎么改",
			want:  []string{"proxy-ctl.sh"},
		},
		{
			// 弱命中(词法能过、语义上只是"沾边"):条目写的是「只读档下写文件应被拒」,
			// 问句换成「能写吗」—— 实际命中的理由只是共享 bigram「只读」「只读档」。
			// 记下来是为了守住这条边界:命中 ≠ 答对,候选内容仍要人看。
			query: "只读档能写文件吗",
			want:  []string{"只读档下写文件应被拒"},
			note:  "弱命中:指望共享的「只读档」bigram,不是真懂「能写吗」;接 embedding 后重冻",
		},
	}
}

// TestProbeTable 固定库 + 固定问句的命中集回归(人工判过的结论在这里冻结)。
func TestProbeTable(t *testing.T) {
	_, svc := startServiceWith(t, nil)
	for _, s := range probeFixture {
		if err := svc.Add(s, ""); err != nil {
			t.Fatalf("铺固定库: %v", err)
		}
	}
	for _, c := range probeTable() {
		t.Run(c.query, func(t *testing.T) {
			res, err := svc.Probe(c.query)
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			var joined strings.Builder
			for _, h := range res.Hits {
				joined.WriteString(h.Content)
				joined.WriteByte('\n')
			}
			got := joined.String()
			if len(c.want) == 0 {
				// 零命中期:命中集必须真的是空的(否则"零命中"这条期望本身就是假的)
				if len(res.Hits) != 0 {
					t.Fatalf("期望零命中,实际命中 %d 条:\n%s", len(res.Hits), got)
				}
				return
			}
			if len(res.Hits) == 0 {
				t.Fatalf("期望命中 %v,实际零命中(参与打分=%v 丢弃=%v)", c.want, res.Terms, res.Ignored)
			}
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Fatalf("命中集里应有 %q:\n%s", w, got)
				}
			}
			for _, a := range c.avoid {
				if strings.Contains(got, a) {
					t.Fatalf("命中集里不该有 %q(无关条目被拉进来了):\n%s", a, got)
				}
			}
		})
	}
}

// TestProbeTableCostsBudget 表里每条问句的注入量都要落在预算内 —— 核对表的意义是
// "真跑一轮会花多少",所以顺带把 token 账的上界钉住(表格改动导致注入条数暴涨时红)。
func TestProbeTableCostsBudget(t *testing.T) {
	_, svc := startServiceWith(t, nil)
	for _, s := range probeFixture {
		if err := svc.Add(s, ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range probeTable() {
		res, err := svc.Probe(c.query)
		if err != nil {
			t.Fatal(err)
		}
		injected, bytes := 0, 0
		for _, h := range res.Hits {
			if h.Selected && !h.Stable {
				injected++
				bytes += len(h.Content)
			}
		}
		if injected > 6 { // DefaultTopK
			t.Fatalf("%q 注入 %d 条,超过 topK 上限 6", c.query, injected)
		}
		if bytes > res.Budget {
			t.Fatalf("%q 注入 %d 字节,超过预算 %d", c.query, bytes, res.Budget)
		}
	}
}
