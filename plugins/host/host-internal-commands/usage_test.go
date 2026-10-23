// /usage 用例:参数解析、渲染口径、以及「真的读盘算出来了」。
//
// 为什么要有第三条:前两条是纯函数,给不出"它到底有没有读会话目录"的证据 ——
// 而这条命令的全部价值就在于**读的是盘上的真账本**,不是某个内存累加器。
package hostintcmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/usageindex"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func usageJSONLEvent(prompt, completion, cached int) string {
	return `{"Kind":"` + sdk.EventUsage + `","Seq":1,"Payload":{"Model":"m","Usage":{"PromptTokens":` +
		itoaTest(prompt) + `,"CompletionTokens":` + itoaTest(completion) + `,"CachedTokens":` + itoaTest(cached) + `}}}`
}

func itoaTest(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestParseUsageScope 参数解析:默认 30 天 / 显式天数 / all / 非法值显式报错。
func TestParseUsageScope(t *testing.T) {
	if d, all, err := parseUsageScope(nil); err != nil || all || d != usageDefaultDays {
		t.Fatalf("无参应默认 %d 天,got d=%d all=%v err=%v", usageDefaultDays, d, all, err)
	}
	if d, _, err := parseUsageScope([]string{"7"}); err != nil || d != 7 {
		t.Fatalf("显式天数应生效,got d=%d err=%v", d, err)
	}
	if _, all, err := parseUsageScope([]string{"ALL"}); err != nil || !all {
		t.Fatalf("all(大小写不敏感)应生效,got all=%v err=%v", all, err)
	}
	for _, bad := range []string{"0", "-3", "abc"} {
		if _, _, err := parseUsageScope([]string{bad}); err == nil {
			t.Fatalf("%q 应显式报错(不能当成默认值静默跑)", bad)
		}
	}
}

// TestUsageReportShape 渲染:汇总 + 缓存命中率 + 榜单 + 剩余计数 + 口径说明。
func TestUsageReportShape(t *testing.T) {
	res := usageindex.Result{
		Requests: 3, Prompt: 4000, Complete: 1000, Cached: 1000,
		Since: time.Now().AddDate(0, 0, -30),
		Sessions: []usageindex.SessionUsage{
			{Session: "proj-20261020-100000", Requests: 2, Prompt: 3000, Complete: 800, MTime: 100},
			{Session: "proj-20261021-100000", Requests: 1, Prompt: 1000, Complete: 200, MTime: 200},
		},
	}
	out := usageReport(res)
	// 榜单标签是「时间戳 · 项目尾」—— 长项目键裁中间会切断单词,实测谁也认不出
	for _, want := range []string{"请求 3 次", "输入 4.0k", "输出 1.0k", "合计 5.0k", "缓存命中输入 1.0k(25.0%)", "1. 2026-10-20 10:00 · proj"} {
		if !strings.Contains(out, want) {
			t.Fatalf("报告缺 %q:\n%s", want, out)
		}
	}
	// 口径必须写在输出里:时间窗筛的是**会话文件**,不是逐条请求
	if !strings.Contains(out, "近 30 天") || !strings.Contains(out, "非逐请求时间窗") {
		t.Fatalf("口径说明缺失(不写清就是给出错答案):\n%s", out)
	}
	// 空结果要有话说,而不是只印一行 0
	if e := usageReport(usageindex.Result{}); !strings.Contains(e, "没有用量记录") {
		t.Fatalf("空结果应有一句人话:\n%s", e)
	}
	// 榜单截断 + 剩余计数
	many := usageindex.Result{}
	for i := 0; i < usageTopN+3; i++ {
		many.Sessions = append(many.Sessions, usageindex.SessionUsage{Session: "s", Requests: 1, Prompt: 1})
	}
	if o := usageReport(many); !strings.Contains(o, "另有 3 个会话") {
		t.Fatalf("超长榜单应报剩余数量:\n%s", o)
	}
}

// TestUsageSessionLabel 会话名缩写:时间戳拆出来格式化,项目键只留尾部。
func TestUsageSessionLabel(t *testing.T) {
	long := "Users-nekoleamo-Documents-Working-go-agent-harness-20261023-101500"
	got := usageSessionLabel(long)
	if got != "2026-10-23 10:15 · …g-go-agent-harness" {
		t.Fatalf("长项目键应只留尾部,got %q", got)
	}
	if got := usageSessionLabel("proj-20261023-101500"); got != "2026-10-23 10:15 · proj" {
		t.Fatalf("短项目键应完整保留,got %q", got)
	}
	// 同分钟撞名的序号段:不该被当成项目键的一部分
	if got := usageSessionLabel("proj-20261023-101500-2"); got != "2026-10-23 10:15 · proj" {
		t.Fatalf("序号段应被识别,got %q", got)
	}
	// 没有时间戳段 = 该项目的主会话(文件名就是项目键)
	if got := usageSessionLabel("tmp-gah-empty"); got != "主会话 · tmp-gah-empty" {
		t.Fatalf("主会话应有标记,got %q", got)
	}
	if got := usageSessionLabel("Users-nekoleamo-Documents-Working-go-agent-harness"); got != "主会话 · …g-go-agent-harness" {
		t.Fatalf("长项目键的主会话也要收尾,got %q", got)
	}
}

// TestCmdUsageReadsSessionDir 端到端:真的去读 $GAH_HOME/sessions 下的账本。
//
// 反向验证:把 usageindex.Scan 的 root 参数改成 "" 以外的固定路径(或让 Scan 直接返回空
// Result)⇒ 本用例红(拿不到造出来的数字)。
func TestCmdUsageReadsSessionDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := usageJSONLEvent(1200, 300, 600) + "\n" + usageJSONLEvent(800, 200, 0) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "proj-20261023-120000.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ := buildEnv(t)
	cmds := startCmds(t, c)
	out, err := run(t, cmds, "usage")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"请求 2 次", "合计 2.5k", "2026-10-23 12:00 · proj"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺 %q(说明没读到会话账本):\n%s", want, out)
		}
	}
}
