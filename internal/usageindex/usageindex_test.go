// usageindex 的用例:聚合口径、坏行容忍、时间窗、排序。
//
// 这些用例守的是「说得对不对」而不是「跑不跑得通」:用量数字错了不会报错,
// 只会让人按错的数去做成本决策 —— 所以每条都对着一个具体的口径。
package usageindex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// writeSession 造一个会话文件(每行一个 JSON 字符串)。
func writeSession(t *testing.T, dir, name string, lines []string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name+".jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func usageLine(prompt, completion, cached int) string {
	return `{"Kind":"` + sdk.EventUsage + `","Seq":1,"Payload":{"Model":"m","Usage":{"PromptTokens":` +
		itoa(prompt) + `,"CompletionTokens":` + itoa(completion) + `,"CachedTokens":` + itoa(cached) + `}}}`
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// TestScanAggregatesPerSession 按会话求和 + 汇总 + 排序(Total 降序)。
func TestScanAggregatesPerSession(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "proj-a-20261020-100000", []string{
		`{"Kind":"user/message","Seq":0,"Payload":{"Content":"你好"}}`,
		usageLine(100, 10, 40),
		`{"Kind":"assistant/message","Seq":2,"Payload":{"Content":"在"}}`,
		usageLine(200, 20, 0),
	}, time.Time{})
	writeSession(t, dir, "proj-a-20261021-100000", []string{usageLine(50, 5, 0)}, time.Time{})
	// 空会话(只有设置事件)不该进榜
	writeSession(t, dir, "proj-a-20261022-100000", []string{`{"Kind":"session/config","Seq":0}`}, time.Time{})

	res, err := Scan(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Requests != 3 || res.Prompt != 350 || res.Complete != 35 || res.Cached != 40 {
		t.Fatalf("汇总不对: %+v", res)
	}
	if res.Sum() != 385 {
		t.Fatalf("Sum 应为输入+输出 = 385,got %d", res.Sum())
	}
	if len(res.Sessions) != 2 {
		t.Fatalf("空会话不该进榜,got %d 条: %+v", len(res.Sessions), res.Sessions)
	}
	// 降序:300 总量在前
	// 小计 = 输入 100+200 + 输出 10+20 = 330(Total 是**输入+输出**,不是只算输入)
	if res.Sessions[0].Session != "proj-a-20261020-100000" || res.Sessions[0].Total() != 330 {
		t.Fatalf("排序/小计不对: %+v", res.Sessions[0])
	}
	if res.Sessions[0].Requests != 2 {
		t.Fatalf("该会话应为 2 次请求,got %d", res.Sessions[0].Requests)
	}
	if res.Files != 3 {
		t.Fatalf("应扫过 3 个文件(含空会话),got %d", res.Files)
	}
}

// TestScanToleratesBadLines 坏行跳过并计数 —— 不能因为一行坏掉就整场失败,
// 也不能把"读不出来"静默算成"没花过"。
func TestScanToleratesBadLines(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s1", []string{
		// 截断的用量行(kind 在、JSON 不完整):必须**计数**,否则"读不出来"会静默变成"没花过"
		`{"Kind":"` + sdk.EventUsage + `","Payload":{"Usage":{`,
		usageLine(100, 10, 0),
		`{"Kind":"` + sdk.EventUsage + `","Payload":{"Usage":` + `}}`, // 坏 payload
		usageLine(100, 10, 0),
	}, time.Time{})
	res, err := Scan(dir, time.Time{})
	if err != nil {
		t.Fatalf("坏行不该让整场失败: %v", err)
	}
	if res.Requests != 2 || res.Prompt != 200 {
		t.Fatalf("好行应照常计入,got %+v", res)
	}
	if res.Skipped < 2 {
		t.Fatalf("坏行应如实计数(否则「读不出来」会被当成「没花过」),got %d", res.Skipped)
	}
}

// TestScanSinceFiltersByFileMtime 时间窗按**文件 mtime** 过滤(口径写进输出文案)。
func TestScanSinceFiltersByFileMtime(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().AddDate(0, 0, -100)
	writeSession(t, dir, "old", []string{usageLine(1000, 0, 0)}, old)
	writeSession(t, dir, "new", []string{usageLine(10, 1, 0)}, time.Time{})
	res, err := Scan(dir, time.Now().AddDate(0, 0, -30))
	if err != nil {
		t.Fatal(err)
	}
	if res.Prompt != 10 || len(res.Sessions) != 1 || res.Sessions[0].Session != "new" {
		t.Fatalf("30 天窗口应只算新会话,got %+v", res)
	}
	all, err := Scan(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if all.Prompt != 1010 {
		t.Fatalf("不限窗口应全部计入,got %d", all.Prompt)
	}
}

// TestScanMissingDirIsEmpty 目录不存在 = 还没跑过 = 0,不是错误(否则首装用户点 /usage 会看到报错)。
func TestScanMissingDirIsEmpty(t *testing.T) {
	res, err := Scan(filepath.Join(t.TempDir(), "nope"), time.Time{})
	if err != nil {
		t.Fatalf("目录不存在不该报错: %v", err)
	}
	if res.Requests != 0 || len(res.Sessions) != 0 {
		t.Fatalf("应为空结果,got %+v", res)
	}
}

// TestScanIgnoresNonJSONL 目录里的其它文件(workspaces.json / 子目录)不参与统计。
func TestScanIgnoresNonJSONL(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "s1", []string{usageLine(5, 5, 0)}, time.Time{})
	if err := os.WriteFile(filepath.Join(dir, "workspaces.json"), []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := Scan(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 {
		t.Fatalf("只该扫 .jsonl 文件,got %d", res.Files)
	}
}

// 派生会话(fork/clone)复制了父会话的前缀事件 ⇒ 统计只看派生点之后的用量。
// **反向验证**:去掉 scanFile 的 `skipUntil` 判断 ⇒ 本用例红(父的消耗被计两遍)。
func TestForkedSessionPrefixNotDoubleCounted(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, evs []string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(evs, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	usageLine := func(seq int, model string, prompt int) string {
		return fmt.Sprintf(`{"Seq":%d,"Kind":"session/usage","Payload":{"Model":%q,"Usage":{"PromptTokens":%d,"CompletionTokens":1}}}`, seq, model, prompt)
	}
	// 父会话:seq 1..2 两条 usage
	write("proj-20260101-000000.jsonl", []string{usageLine(1, "m", 100), usageLine(2, "m", 100)})
	// 分支:fork at seq 2 ⇒ 前缀两条是复制的,自己又花了一条(seq 10)
	write("proj-20260102-000000.jsonl", []string{usageLine(1, "m", 100), usageLine(2, "m", 100), usageLine(10, "m", 7)})
	if err := os.WriteFile(filepath.Join(dir, "fork-tree.json"), []byte(`{"20260102-000000":{"ID":"20260102-000000","Parent":"","ParentSeq":2}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Scan(dir, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Prompt != 207 {
		t.Fatalf("父两次 100 + 分支自己 7 = 207;得 %d(分支把父的前缀重复计了)", res.Prompt)
	}
}
