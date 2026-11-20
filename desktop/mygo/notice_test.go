package main

import "testing"

// fakePoller 造一个可控的轮询器:fetch 由用例驱动,send 只记账。
func fakePoller(pages []noticePage) (*noticePoller, *[]string) {
	var got []string
	i := 0
	p := &noticePoller{
		fetch: func(uint64) (noticePage, bool) {
			if i >= len(pages) {
				return noticePage{}, false
			}
			pg := pages[i]
			i++
			return pg, true
		},
		send: func(title, body string) error { got = append(got, title); return nil },
	}
	return p, &got
}

// TestNoticePrimeSkipsHistory 首次成功拉取只定位游标:启动前就存在的失败不是新闻。
// 反向验证:去掉 primed 分支(把首页当新提示发出去),本用例必红。
func TestNoticePrimeSkipsHistory(t *testing.T) {
	p, got := fakePoller([]noticePage{{
		Items: []noticeItem{{ID: 1, Level: "error", Title: "旧错误"}},
		MaxID: 5,
	}})
	if n := p.tick(); n != 0 {
		t.Fatalf("首次拉取不该发出通知,发 %d 条", n)
	}
	if len(*got) != 0 {
		t.Fatalf("首次拉取不该发通知: %v", *got)
	}
	if p.cursor != 5 {
		t.Fatalf("游标应定位到 max_id=5,得到 %d", p.cursor)
	}
}

// TestNoticeOnlyWarnError warn/error 才打扰人;info 交给状态栏(壳没有 → 丢弃)。
func TestNoticeOnlyWarnError(t *testing.T) {
	p, got := fakePoller([]noticePage{
		{Items: []noticeItem{}, MaxID: 1}, // 首次:定位游标
		{Items: []noticeItem{
			{ID: 2, Level: "info", Title: "信息"},
			{ID: 3, Level: "warn", Title: "警告", Source: "host-jobs"},
			{ID: 4, Level: "error", Title: "错误", Body: "匹配\n第二行"},
			{ID: 3, Level: "warn", Title: "重复 ID 不该再发"}, // 已消费
		}, MaxID: 4},
	})
	p.tick()
	if n := p.tick(); n != 2 {
		t.Fatalf("应发 2 条(warn+error),发 %d 条: %v", n, *got)
	}
	if p.cursor != 4 {
		t.Fatalf("游标 = %d, want 4", p.cursor)
	}
}

// TestNoticeGapWarnOnce 环形缓冲丢过条目必须说一次,且只说一次(否则每轮都在刷)。
func TestNoticeGapWarnOnce(t *testing.T) {
	p, got := fakePoller([]noticePage{
		{MaxID: 1},
		{Gap: true, MaxID: 9},
		{Gap: true, MaxID: 10},
	})
	p.tick()
	p.tick()
	p.tick()
	if len(*got) != 1 {
		t.Fatalf("gap 提示应只发一次,发 %v", *got)
	}
}

// TestTurnWatch 回合结束(running true→false)发一次;首次见到的状态不算翻转。
func TestTurnWatch(t *testing.T) {
	p, got := fakePoller(nil)
	p.watchTurn(true)  // 首次:只记状态
	p.watchTurn(true)  // 无翻转
	p.watchTurn(false) // 翻转 → 发
	p.watchTurn(false) // 不再发
	if len(*got) != 1 {
		t.Fatalf("回合完成应只发一次,发 %v", *got)
	}
	// 从未见过 running 时不该报「完成」。
	p2, got2 := fakePoller(nil)
	p2.watchTurn(false)
	if len(*got2) != 0 {
		t.Fatalf("没跑过回合不该发完成通知: %v", *got2)
	}
}

// TestStateRunning 形状不符不猜(宁可当没在跑,也不发假「完成」)。
func TestStateRunning(t *testing.T) {
	if !stateRunning([]byte(`{"running":true}`)) {
		t.Fatal("running=true 应识别")
	}
	if stateRunning([]byte(`{"running":false}`)) {
		t.Fatal("running=false 应识别")
	}
	if stateRunning([]byte(`not json`)) {
		t.Fatal("坏 JSON 应返回 false")
	}
}
