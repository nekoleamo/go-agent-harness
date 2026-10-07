package hostcwdsessions

// 会话级偏好的存储测试(第一百一十六批):meta.json 里的会话级偏好读写。
//
// 这里钉两件事:① 存取往返;② **只存显式设置过的项** —— 没设的项读回来是空串,
// 回落由 sdk.ResolveSessionPrefs 统一做(不在这儿回落,理由见 SessionPrefsOf 的注释)。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestSessionPrefsRoundTrip(t *testing.T) {
	svc, _, _ := startSvc(t)
	cur := svc.CurrentSession()
	if got := svc.SessionPrefsOf(cur); got != (sdk.SessionPrefs{}) {
		t.Fatalf("没设过的会话应读到零值,得 %+v", got)
	}
	want := sdk.SessionPrefs{Role: "finance", Model: "deepseek-chat", Thinking: "high", Sandbox: "read-only", Approval: "strict"}
	if err := svc.SetSessionPrefs(cur, want); err != nil {
		t.Fatal(err)
	}
	if got := svc.SessionPrefsOf(cur); got != want {
		t.Fatalf("读写不一致: 写 %+v 读 %+v", want, got)
	}
	// 落盘事实(meta.json 里有这一项,而不是只活在内存)
	raw, err := os.ReadFile(filepath.Join(SessionsRoot(), "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("meta.json 应被写入")
	}
}

// 另一个会话的偏好互不影响(页签的基本要求)。
func TestSessionPrefsArePerSession(t *testing.T) {
	svc, _, _ := startSvc(t)
	cur := svc.CurrentSession()
	other := "20260101-030303"
	if err := os.WriteFile(SessionPath(SessionsRoot(), svc.Current(), other), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSessionPrefs(cur, sdk.SessionPrefs{Role: "A"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetSessionPrefs(other, sdk.SessionPrefs{Role: "B"}); err != nil {
		t.Fatal(err)
	}
	if got := svc.SessionPrefsOf(cur).Role; got != "A" {
		t.Fatalf("主会话应是 A,得 %q", got)
	}
	if got := svc.SessionPrefsOf(other).Role; got != "B" {
		t.Fatalf("另一个会话应是 B,得 %q", got)
	}
}

// 给不存在的会话写偏好必须显式失败(否则会造出一份永远没人读的元数据)。
func TestSessionPrefsUnknownSessionFails(t *testing.T) {
	svc, _, _ := startSvc(t)
	if err := svc.SetSessionPrefs("20990101-000000", sdk.SessionPrefs{Role: "x"}); err == nil {
		t.Fatal("给不存在的会话写偏好应显式报错")
	}
}

// 坏 meta.json(半写/手改)不得让偏好读取崩掉,也不得报成"设置了"。
func TestSessionPrefsToleratesCorruptMeta(t *testing.T) {
	svc, _, _ := startSvc(t)
	if err := os.WriteFile(filepath.Join(SessionsRoot(), "meta.json"), []byte("{ 坏 json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := svc.SessionPrefsOf(svc.CurrentSession()); got != (sdk.SessionPrefs{}) {
		t.Fatalf("坏 meta 应读成零值(回落全局),得 %+v", got)
	}
}
