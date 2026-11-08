package memory

// 失败记账与触发的契约。
//
// 钉的是"会静默害人"的三处:
//   ① 签名归一化**不能抹太多**:抹掉文字会让"文件不存在"与"目录不存在"撞成一个签名;
//   ② 阈值必须翻倍:同一签名重复几十次时每一步都提候选会把候选池灌满;
//   ③ 错误文本里的凭据**不能落到账本里**(那等于把密钥写进一个长驻 json)。

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestFailureSignatureNormalizesVolatileParts(t *testing.T) {
	a, _ := FailureSignature("bash", "exit status 1: 找不到 /tmp/build-20261108/abc123")
	b, _ := FailureSignature("bash", "exit status 1: 找不到 /tmp/build-20261109/def456")
	if a != b {
		t.Fatalf("只有数字/哈希不同的同一个错误应归一到同一签名:\n%s\n%s", a, b)
	}
	c, _ := FailureSignature("bash", "exit status 1: 目录不存在")
	if a == c {
		t.Fatal("错误语义不同(文件 vs 目录)不该撞成一个签名 —— 归一化抹太多了")
	}
	d, _ := FailureSignature("file_read", "exit status 1: 找不到 /tmp/build-20261108/abc123")
	if a == d {
		t.Fatal("不同工具的同一个错误文本应是两个签名")
	}
}

func TestFailureRecordThresholdDoubles(t *testing.T) {
	var st FailureState
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	var fires []int
	for i := 1; i <= 9; i++ {
		var hit FailureHit
		st, hit = st.Record("bash", "docker: no such service", "proj", "sess-1", 2, now.Add(time.Duration(i)*time.Minute))
		if hit.Count != i {
			t.Fatalf("第 %d 次:累计数不对 %d", i, hit.Count)
		}
		if hit.Fire {
			fires = append(fires, i)
		}
	}
	// 阈值 2 起、每次触发后翻倍 ⇒ 2、4、8
	want := []int{2, 4, 8}
	if len(fires) != len(want) {
		t.Fatalf("触发次数不对: %v(期望 %v)", fires, want)
	}
	for i := range want {
		if fires[i] != want[i] {
			t.Fatalf("触发点不对: %v(期望 %v)", fires, want)
		}
	}
}

func TestFailureRecordSeparatesProjects(t *testing.T) {
	var st FailureState
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	st, h1 := st.Record("bash", "permission denied", "proj-a", "s1", 2, now)
	st, h2 := st.Record("bash", "permission denied", "proj-b", "s2", 2, now)
	if h1.Count != 1 || h2.Count != 1 {
		t.Fatalf("不同项目各自的重复次数应独立: %d / %d", h1.Count, h2.Count)
	}
	if h1.Fire || h2.Fire {
		t.Fatal("首次出现(阈值 2)不该触发")
	}
	_, h3 := st.Record("bash", "permission denied", "proj-a", "s1", 2, now)
	if !h3.Fire {
		t.Fatal("同一项目第二次应触发")
	}
}

// 错误文本里的凭据绝不落盘:记的是"这个错误重复了",不是错误原文。
func TestFailureRecordRedactsSecrets(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	var st FailureState
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	st, hit := st.Record("bash", "curl 失败 api_key=sk-live-ABC123DEF456 无效", "", "s1", 2, now)
	if strings.Contains(hit.Head, "sk-live") {
		t.Fatalf("进候选的样本里不该出现凭据: %q", hit.Head)
	}
	st, _ = st.Record("bash", "curl 失败 api_key=sk-live-ABC123DEF456 无效", "", "s1", 2, now)
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(FailuresPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-live") {
		t.Fatalf("账本里不该出现凭据原文:\n%s", raw)
	}
	if !strings.Contains(string(raw), "已省略") {
		t.Fatalf("应留下'已省略'的占位,让人知道为什么看不到原文:\n%s", raw)
	}
}

func TestFailureCandidateTextIsFactual(t *testing.T) {
	e := FailureEntry{Tool: "bash", Head: "docker compose: no such service", Count: 4}
	text := e.CandidateText()
	for _, must := range []string{"bash", "4 次", "no such service"} {
		if !strings.Contains(text, must) {
			t.Fatalf("候选正文应含 %q: %s", must, text)
		}
	}
	if strings.Contains(text, "因为") || strings.Contains(text, "应该是") {
		t.Fatalf("候选不能编造原因(那会变成长期生效的错记忆): %s", text)
	}
}

// alpha 把序号编成互不相同的小写字母串(a、b、…、z、aa、ab…),避免撞归一化。
func alpha(n int) string {
	if n < 26 {
		return string(rune('a' + n))
	}
	return alpha(n/26-1) + string(rune('a'+n%26))
}

func TestFailureStateCapAndRoundTrip(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	var st FailureState
	base := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	for i := 0; i < MaxFailures+10; i++ {
		// 用字母区分(数字会被归一化成 <n>,那样所有条目会撞成一个签名)。
		st, _ = st.Record("bash", "错误 "+alpha(i), "", "s", 2, base.Add(time.Duration(i)*time.Minute))
	}
	if len(st.Items) != MaxFailures {
		t.Fatalf("账本应被压到上限 %d, got %d", MaxFailures, len(st.Items))
	}
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFailures()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != MaxFailures {
		t.Fatalf("往返条数不对: %d", len(got.Items))
	}
	// 坏文件:按空账本继续 + 返回 error
	if err := os.WriteFile(FailuresPath(), []byte("{坏"), 0o644); err != nil {
		t.Fatal(err)
	}
	if empty, err := LoadFailures(); err == nil || len(empty.Items) != 0 {
		t.Fatalf("坏账本应按空继续并报错, got %+v err=%v", empty, err)
	}
}
