// 整卷问卷 HTTP 端点单测:逐题作答 → 收口;坏请求 400;未装配 503。
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// postQnJSON 发一个 JSON POST,返回状态码与响应体(与 server_test 的 postJSON 区分签名)。
func postQnJSON(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestQuestionnaireEndpoints 逐题作答落库 → 收口回传(整卷能力经 HTTP 完整走通)。
func TestQuestionnaireEndpoints(t *testing.T) {
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, release, err := s.question.PresentQuestionnaire(ctx, qnFixture())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	id := ""
	// 从 pending 里取整卷 id(帧载荷同样带,这里直接读服务侧,避免依赖 SSE 时序)
	for _, f := range s.question.PendingQuestionnaireFrames() {
		if r, ok := f.Payload.(*QuestionnaireRequest); ok {
			id = r.ID
		}
	}
	if id == "" {
		t.Fatal("未决整卷应有补推帧")
	}

	code, body := postQnJSON(t, hs.URL+"/api/questionnaire/answer",
		`{"id":"`+id+`","question_id":"scope","values":["web"],"index":1}`)
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("逐题作答应成功: %d %+v", code, body)
	}
	code, body = postQnJSON(t, hs.URL+"/api/questionnaire/submit", `{"id":"`+id+`"}`)
	if code != http.StatusOK || body["ok"] != true {
		t.Fatalf("收口应成功: %d %+v", code, body)
	}
	select {
	case ans := <-ch:
		if len(ans.Answers) != 2 || ans.Answers[0].Values[0] != "web" || !ans.Answers[1].Empty() {
			t.Fatalf("HTTP 路径组装的作答复符: %+v", ans.Answers)
		}
	case <-time.After(time.Second):
		t.Fatal("收口后应回传作答")
	}
	// 重复提交:200 + ok:false(幂等,不报错)
	code, body = postQnJSON(t, hs.URL+"/api/questionnaire/submit", `{"id":"`+id+`"}`)
	if code != http.StatusOK || body["ok"] != false {
		t.Fatalf("重复收口应幂等忽略: %d %+v", code, body)
	}
}

// TestQuestionnaireEndpointsBadRequest 缺 id / 坏 JSON:400。
func TestQuestionnaireEndpointsBadRequest(t *testing.T) {
	s, _ := newTestServer()
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	for _, c := range []struct{ path, body string }{
		{"/api/questionnaire/answer", `{"id":"x"}`},
		{"/api/questionnaire/answer", `{"question_id":"q"}`},
		{"/api/questionnaire/answer", `{bad`},
		{"/api/questionnaire/submit", `{}`},
		{"/api/questionnaire/submit", `{bad`},
	} {
		if code, _ := postQnJSON(t, hs.URL+c.path, c.body); code != http.StatusBadRequest {
			t.Fatalf("%s %s: 应 400, got %d", c.path, c.body, code)
		}
	}
}

// TestQuestionnaireEndpointsNoService 未装配提问通道:503(不 500、不静默成功)。
func TestQuestionnaireEndpointsNoService(t *testing.T) {
	s, _ := newTestServer()
	s.question = nil
	hs := httptest.NewServer(s.handler())
	defer hs.Close()
	for _, c := range []struct{ path, body string }{
		{"/api/questionnaire/answer", `{"id":"x","question_id":"q"}`},
		{"/api/questionnaire/submit", `{"id":"x"}`},
	} {
		if code, _ := postQnJSON(t, hs.URL+c.path, c.body); code != http.StatusServiceUnavailable {
			t.Fatalf("%s: 未装配应 503, got %d", c.path, code)
		}
	}
}
