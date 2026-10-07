// web_search 的 key 失效自愈与兜底报错文案(2026-10-06 用户反馈带出来的一批)。
//
// 背景:用户报「让 gah 分析年报,搜索一直报 web_search: 搜索服务返回 402」。查下来是两件事:
// ① 402 来自 exa(按量付费、额度耗尽),而**兜底文案不指名是谁**,用户无从下手;
// ② anysearch 那条路上注释说「宁可不带 Authorization」,代码却「有 key 就带」—— 一个失效
//
//	key 就能把本来能匿名工作的搜索打成 401。注释与代码矛盾,两边都该有人负责,所以这里钉死。
package toolweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/searchfile"
)

// TestAnysearchFallsBackToAnonymousOnBadKey 带 key 被拒(401)→ 自动退回匿名重试。
//
// 匿名是这条路能工作的**前提**,key 只是提额 —— 加分项不该把主功能打挂。
func TestAnysearchFallsBackToAnonymousOnBadKey(t *testing.T) {
	withAuth := make([]string, 0, 2)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("Authorization") != "" {
			withAuth = append(withAuth, r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"code":-1,"message":"Invalid API key."}`))
			return
		}
		w.Write([]byte(`{"code":0,"data":{"results":[{"title":"年报要点","url":"https://example.test/a","snippet":"s"}]}}`))
	}))
	defer srv.Close()

	p := &anysearchProvider{client: srv.Client(), endpoint: srv.URL, apiKey: "sk-bad"}
	got, err := p.Search(context.Background(), "2025 年报", 3)
	if err != nil {
		t.Fatalf("应自动退回匿名并成功: %v", err)
	}
	if len(got) != 1 || got[0].Title != "年报要点" {
		t.Fatalf("结果不对: %+v", got)
	}
	if len(withAuth) != 1 {
		t.Fatalf("只该带一次无效 key(之后记住),实得 %d: %+v", len(withAuth), withAuth)
	}
}

// TestAnysearchDoesNotRetryForever 记住「key 已被拒」之后就不再重复失败请求。
//
// 否则批量搜索里每次都多一次 401 往返 —— 搜索是可多次调用的工具,这个开销会很明显。
func TestAnysearchDoesNotRetryForever(t *testing.T) {
	var authed int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			atomic.AddInt32(&authed, 1)
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"code":-1,"message":"Invalid API key."}`))
			return
		}
		w.Write([]byte(`{"code":0,"data":{"results":[{"title":"t","url":"u","snippet":"s"}]}}`))
	}))
	defer srv.Close()

	p := &anysearchProvider{client: srv.Client(), endpoint: srv.URL, apiKey: "sk-bad"}
	for i := 0; i < 3; i++ {
		if _, err := p.Search(context.Background(), "q", 3); err != nil {
			t.Fatalf("第 %d 次搜索应成功: %v", i+1, err)
		}
	}
	if n := atomic.LoadInt32(&authed); n != 1 {
		t.Fatalf("带 key 的请求只应发生一次(之后记住),实得 %d", n)
	}
}

// TestAnysearchAnonymousNoRetry 已经匿名时不该再退(没有 key 可退)。
func TestAnysearchAnonymousNoRetry(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"code":-1,"message":"forbidden"}`))
	}))
	defer srv.Close()

	p := &anysearchProvider{client: srv.Client(), endpoint: srv.URL}
	if _, err := p.Search(context.Background(), "q", 3); err == nil {
		t.Fatal("匿名也被拒时应显式失败")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("匿名失败不该再退一次,实得 %d 次请求", n)
	}
}

// TestFallbackErrorNamesTheProvider 兜底文案必须指名是哪个服务 + 下一步。
//
// 「搜索服务返回 402」这句话是用户拿来问我的原话 —— 它既没说是哪家,也没说怎么办。
func TestFallbackErrorNamesTheProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		w.Write([]byte(`{"message":"insufficient balance"}`))
	}))
	defer srv.Close()

	p := &anysearchProvider{client: srv.Client(), endpoint: srv.URL}
	_, err := p.Search(context.Background(), "q", 3)
	if err == nil {
		t.Fatal("402 应报错")
	}
	se := &SearchError{}
	se, ok := err.(*SearchError)
	if !ok {
		t.Fatalf("应是 SearchError,实得 %T", err)
	}
	for _, want := range []string{"anysearch", "402", searchfile.EnvProvider} {
		if !strings.Contains(se.Msg, want) {
			t.Fatalf("兜底文案应含 %q(要指名服务与出路),实得 %q", want, se.Msg)
		}
	}
}
