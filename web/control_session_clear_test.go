package web

// /api/control 的会话作用域**清除**语义护栏(第一百三十五批 review 修的 bug)。
//
// 背景:「本会话设置」面板的「改为跟随全局」按钮,动作是给该会话写一个**空串** ——
// 表示清掉这层覆盖、让它回落全局。而 handleControl 当时用 `if req.Model != ""`,
// 把「空串」与「没给」当成同一回事 ⇒ **那个按钮点了完全不生效,且没有任何提示**
// (按钮在、界面像在响应,后端什么都没做)。这是 review 才发现的 —— 单测当时全绿,
// 因为没有任何一条用例会去点那个按钮。
//
// 这组用例钉住两侧:
//   ① 会话作用域 + 空串 = **清除**(按钮必须真的有用);
//   ② 不给字段 = 不动(旧客户端逐字不变);
//   ③ 全局作用域 + 空串 = 不动(全局没有「清除模型」这种操作,别顺手放开)。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// postControl 发一条 /api/control 并返回状态码。
func postControl(t *testing.T, s *Server, body map[string]any) int {
	t.Helper()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(hs.URL+"/api/control", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// 会话作用域:非空 = 设,空串 = 清。
func TestControlSessionScopedSetAndClear(t *testing.T) {
	s, _ := newTestServer()
	withSessionPrefs(t, s, map[string]sdk.SessionPrefs{})
	cs := s.cs.(*stubCS)

	if code := postControl(t, s, map[string]any{"session": "A", "model": "own"}); code != http.StatusOK {
		t.Fatalf("设会话档应 200,得 %d", code)
	}
	if got := cs.prefs.SessionPrefsOf("A").Model; got != "own" {
		t.Fatalf("会话档未写入: %q", got)
	}

	// 关键:空串必须真的清除(这一条就是那个 bug)
	if code := postControl(t, s, map[string]any{"session": "A", "model": ""}); code != http.StatusOK {
		t.Fatalf("清会话档应 200,得 %d", code)
	}
	if got := cs.prefs.SessionPrefsOf("A").Model; got != "" {
		t.Fatalf("空串未清除会话档(按钮会静默失效): %q", got)
	}
}

// 四项都要能清 —— 面板上有四个「改为跟随全局」按钮,漏一个就有一个是哑的。
func TestControlSessionScopedClearAllFour(t *testing.T) {
	for _, f := range []string{"model", "thinking", "sandbox", "approval"} {
		t.Run(f, func(t *testing.T) {
			s, _ := newTestServer()
			withSessionPrefs(t, s, map[string]sdk.SessionPrefs{})
			s.ap = &stubAP{mode: sdk.ApprovalSmart} // 审批服务不装配时端点显式 400,那是既有契约
			cs := s.cs.(*stubCS)

			val := map[string]string{
				"model": "own", "thinking": "high", "sandbox": "read-only", "approval": "strict",
			}[f]
			if code := postControl(t, s, map[string]any{"session": "A", f: val}); code != http.StatusOK {
				t.Fatalf("设 %s 应 200,得 %d", f, code)
			}
			if code := postControl(t, s, map[string]any{"session": "A", f: ""}); code != http.StatusOK {
				t.Fatalf("清 %s 应 200,得 %d", f, code)
			}
			if got := cs.prefs.SessionPrefsOf("A"); !sessionPrefEmpty(got, f) {
				t.Fatalf("空串未清除 %s(该面板的复位按钮是哑的): %+v", f, got)
			}
		})
	}
}

// 旧客户端语义:不带字段 = 不动(向后兼容的底线)。
func TestControlOmittedFieldLeavesSessionPrefs(t *testing.T) {
	s, _ := newTestServer()
	withSessionPrefs(t, s, map[string]sdk.SessionPrefs{})
	cs := s.cs.(*stubCS)

	if code := postControl(t, s, map[string]any{"session": "A", "model": "own"}); code != http.StatusOK {
		t.Fatalf("前置写入失败: %d", code)
	}
	// 只带 thinking,不带 model ⇒ model 不该被动
	if code := postControl(t, s, map[string]any{"session": "A", "thinking": "low"}); code != http.StatusOK {
		t.Fatalf("设 thinking 应 200,得 %d", code)
	}
	got := cs.prefs.SessionPrefsOf("A")
	if got.Model != "own" {
		t.Fatalf("没给 model 却把它清了(向后兼容被破坏): %+v", got)
	}
	if got.Thinking != "low" {
		t.Fatalf("thinking 未写入: %+v", got)
	}
}

// 全局作用域 + 空串 = 不动(别顺手把「清除」放开到全局:全局没有这种操作)。
func TestControlGlobalScopedEmptyIsNoop(t *testing.T) {
	s, _ := newTestServer()
	withSessionPrefs(t, s, map[string]sdk.SessionPrefs{})
	s.llm = &stubLLM{}

	// 不报 400、不崩就算过:全局路径拿不到"清除"语义时应当什么都不做。
	if code := postControl(t, s, map[string]any{"model": ""}); code != http.StatusOK {
		t.Fatalf("全局空串不应报错,得 %d", code)
	}
}

// sessionPrefEmpty 判断某项是否为空(= 未单独设置)。
func sessionPrefEmpty(p sdk.SessionPrefs, field string) bool {
	switch field {
	case "model":
		return p.Model == ""
	case "thinking":
		return p.Thinking == ""
	case "sandbox":
		return p.Sandbox == ""
	case "approval":
		return p.Approval == ""
	}
	return true
}
