// attention 单测:闪烁与通知**独立成败**、默认标题、未装配时的如实上报、控制面端点。
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeFlasher 记录 Flash 调用序列。
type fakeFlasher struct {
	mu    sync.Mutex
	calls []bool
}

func (f *fakeFlasher) Flash(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, on)
}

func (f *fakeFlasher) seq() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.calls...)
}

// TestAttentionFlashesAndNotifies 两条落点都走通:闪烁开 + 通知发出。
func TestAttentionFlashesAndNotifies(t *testing.T) {
	fl := &fakeFlasher{}
	fn := &fakeNotify{authorized: true}
	sh := &shell{flasher: fl, notifier: &notifier{svc: fn}} // flashDur=0:不排收尾定时器

	res := sh.attention("gah 需要你作答", "题:范围到哪?")
	if !res.Flashed || !res.Notified || res.Error != "" {
		t.Fatalf("两条落点都应成功: %+v", res)
	}
	if got := fl.seq(); len(got) != 1 || !got[0] {
		t.Fatalf("应闪一次(开启),得到 %v", got)
	}
	if fn.sends != 1 {
		t.Fatalf("应发一条通知,得到 %d", fn.sends)
	}
}

// TestAttentionNotifiesEvenIfNoFlasher 没有窗口(测试/无壳场景)也要把通知发出去。
func TestAttentionNotifiesEvenIfNoFlasher(t *testing.T) {
	fn := &fakeNotify{authorized: true}
	sh := &shell{notifier: &notifier{svc: fn}}
	res := sh.attention("标题", "正文")
	if res.Flashed {
		t.Fatal("无 flasher 时不应声称已闪烁")
	}
	if !res.Notified {
		t.Fatalf("通知仍应发出: %+v", res)
	}
}

// TestAttentionFlashesEvenIfNotifyFails 通知未授权不该把闪烁一起吞掉(两条路独立成败)。
func TestAttentionFlashesEvenIfNotifyFails(t *testing.T) {
	fl := &fakeFlasher{}
	fn := &fakeNotify{authorized: false, requestRes: false} // 系统拒绝授权
	sh := &shell{flasher: fl, notifier: &notifier{svc: fn}}
	res := sh.attention("t", "b")
	if !res.Flashed {
		t.Fatal("通知失败时闪烁仍应发生")
	}
	if res.Notified {
		t.Fatal("未授权不应声称已通知")
	}
	if res.Error == "" {
		t.Fatal("通知失败必须如实报错误原文(不静默)")
	}
}

// TestAttentionDefaultTitle 空标题给一句人话(页面可能只给正文)。
func TestAttentionDefaultTitle(t *testing.T) {
	fn := &fakeNotify{authorized: true}
	sh := &shell{notifier: &notifier{svc: fn}}
	sh.attention("", "正文")
	if fn.lastTitle != "gah 需要你的回应" {
		t.Fatalf("空标题应回落默认值,得到 %q", fn.lastTitle)
	}
}

// TestAttentionEndpoint 控制面:200 + 结果字段;两条路都不通 ⇒ 503(不假装成功)。
func TestAttentionEndpoint(t *testing.T) {
	cp := &controlPlane{token: "tok", origin: "http://127.0.0.1:2233", sh: &shell{addr: "127.0.0.1:2233"}}
	h := cp.guard(cp.postOnly(cp.handleAttention))

	// 存活但没有任何落点(无窗口、无通知服务)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9999/shell/attention",
		strings.NewReader(`{"title":"t","body":"b"}`))
	req.Host = "127.0.0.1:9999"
	req.Header.Set("X-Shell-Token", "tok")
	req.Header.Set("Origin", "http://127.0.0.1:2233")
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("两条路都不可用应 503,得到 %d", rec.Code)
	}

	// 装上落点后应 200
	fl := &fakeFlasher{}
	cp.sh.flasher = fl
	cp.sh.notifier = &notifier{svc: &fakeNotify{authorized: true}}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:9999/shell/attention",
		strings.NewReader(`{"title":"t","body":"b"}`))
	req.Host = "127.0.0.1:9999"
	req.Header.Set("X-Shell-Token", "tok")
	req.Header.Set("Origin", "http://127.0.0.1:2233")
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("有落点应 200,得到 %d", rec.Code)
	}
	var got attentionResult
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Flashed || !got.Notified {
		t.Fatalf("结果应如实回两条落点: %+v", got)
	}

	// GET 不允许(只接受 POST)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:9999/shell/attention", nil)
	req.Host = "127.0.0.1:9999"
	req.Header.Set("X-Shell-Token", "tok")
	req.Header.Set("Origin", "http://127.0.0.1:2233")
	h(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应 405,得到 %d", rec.Code)
	}
}
