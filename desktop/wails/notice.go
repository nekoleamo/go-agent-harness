// notice:把宿主的**提示流** `/api/notices` 转成系统通知(旧壳 notice.rs 的等价移植)。
//
// 为什么是「消费宿主的提示流」而不是自己判断:旧壳曾经自己轮询 `/api/schedules` 挑
// `last_status == failed`,那是把「哪件事值得打扰人」的判据复制到壳里 —— 规则一改就得
// 三端同步改。现在判据只在宿主一处(host-notices),壳只负责把它转成系统通知。
//
// 语义纪律(与 Web/TUI 同口径):
//   - 只有 warn/error 弹系统通知(info 只该刷状态栏;壳没有状态栏 → 丢弃);
//   - 首次成功拉取只**定位游标**、不补发历史(启动前就存在的失败不是新闻);
//   - 游标只增不减;服务端环形缓冲丢过条目(gap)必须显式说一次,不静默漏掉;
//   - 壳侧仍然独有「回合结束」信号(状态栏 running→idle 翻转):宿主不发这类提示
//     (它面向「需要人回来的时刻」,回合正常结束不是),所以这条留在壳里,与提示流共用
//     同一次轮询 —— 一次 sleep、两个信号源。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// noticeItem 宿主下发的一条提示(sdk.Notice 的 JSON 形状;壳不 import 主模块,故自带一份)。
type noticeItem struct {
	ID     uint64 `json:"id"`
	Level  string `json:"level"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Source string `json:"source"`
}

// noticePage 一次 `/api/notices?since=N` 的应答。
type noticePage struct {
	Items []noticeItem `json:"items"`
	MaxID uint64       `json:"max_id"`
	Gap   bool         `json:"gap"`
}

// stateRunning 从 `/api/state` 里取「是否有回合在跑」(形状不符 → false,不猜)。
func stateRunning(raw []byte) bool {
	var v struct {
		Running bool `json:"running"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	return v.Running
}

// noticePoller 提示流轮询器。fetch/send 可注入(单测用假实现,不碰网络与系统通知)。
type noticePoller struct {
	fetch func(since uint64) (noticePage, bool) // ok=false 表示这次没取到
	send  func(title, body string) error

	cursor    uint64 // 已消费到的最大 ID
	primed    bool   // 是否完成过首次定位
	gapWarned bool   // gap 只提示一次
	lastRun   bool   // 上一次看到的 running
	runSeen   bool   // 是否见过 running 状态(还没见过就不谈「翻转」)
}

// tick 跑一轮:消费提示 + 看回合结束翻转。返回本次发出的通知条数(便于断言/日志)。
func (p *noticePoller) tick() int {
	sent := 0
	if page, ok := p.fetch(p.cursor); ok {
		if !p.primed {
			// 首次成功拉取只定位游标:启动前就存在的失败不是新闻。
			p.cursor = page.MaxID
			p.primed = true
		} else {
			if page.Gap && !p.gapWarned {
				p.gapWarned = true
				p.notify(&sent, "gah", "有提示被丢弃:本次只显示最近几条,更早的已不在缓冲里")
			}
			for _, it := range page.Items {
				if it.ID <= p.cursor {
					continue
				}
				p.cursor = it.ID
				// 只有 warn/error 值得打扰人;info 交给状态栏(壳没有 → 丢弃)。
				if it.Level != "warn" && it.Level != "error" {
					continue
				}
				p.notify(&sent, titleOf(it), bodyOf(it))
			}
			if page.MaxID > p.cursor {
				p.cursor = page.MaxID
			}
		}
	}
	return sent
}

// titleOf/bodyOf 拼通知文案:标题空则回落到正文首行(source 作为副标题线索进正文)。
func titleOf(it noticeItem) string {
	if it.Title != "" {
		return it.Title
	}
	if it.Body != "" {
		return firstLine(it.Body)
	}
	return "gah 提示"
}

func bodyOf(it noticeItem) string {
	b := it.Body
	if b == "" {
		b = it.Title
	}
	if it.Source != "" && it.Source != "unknown" {
		b = b + "\n(" + it.Source + ")"
	}
	return b
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

func (p *noticePoller) notify(sent *int, title, body string) {
	*sent++
	if p.send == nil {
		return
	}
	// v3 的通知会**返回真实错误**(未授权/未注册进通知中心),这正是它比旧壳
	// tauri-plugin-notification 强的地方:那边把 show() 丢进 async 任务并丢弃错误,
	// 于是日志写「已发」、屏幕什么都没有(2026-09-22 真机)。
	if err := p.send(title, body); err != nil {
		shellLogf("通知发送失败(标题=%q): %v", title, err)
		return
	}
	shellLogf("通知已发: %s", title)
}

// runTurnWatch 把「回合结束」也接进同一次轮询(见文件头:一次 sleep、两个信号源)。
func (p *noticePoller) watchTurn(running bool) {
	if !p.runSeen {
		p.runSeen = true
		p.lastRun = running
		return
	}
	if p.lastRun && !running {
		p.lastRun = running
		sent := 0
		p.notify(&sent, "回合已完成", "gah 这一轮已经跑完,可以回来看看了")
		return
	}
	p.lastRun = running
}

// startNoticePoller 起后台轮询(2s 一次,与旧壳同节奏)。
func (s *shell) startNoticePoller() {
	p := &noticePoller{fetch: s.fetchNotices, send: s.sendNotification}
	go func() {
		shellLogf("提示流轮询已启动(2s,/api/notices)")
		for {
			time.Sleep(2 * time.Second)
			if !s.ready.Load() {
				continue // 服务没就绪:这一轮不产生任何结论(包括不要把 running 当成 false)
			}
			// 后台轮询不该有能力干掉整个壳:panic 记一条,下一轮继续
			// (本轮实测过一次 nil fetch 把进程打挂 —— 界面直接消失,只剩一行 panic)。
			func() {
				defer func() {
					if r := recover(); r != nil {
						shellLogf("提示流轮询 panic(已恢复): %v", r)
					}
				}()
				p.tick()
				if st, ok := s.getState(); ok {
					p.watchTurn(stateRunning(st))
				}
			}()
		}
	}()
}

// fetchNotices 拉一次提示流(token 模式带 cookie)。取不到就返回 ok=false,
// 由调用方保持游标不动(下次重投,不丢也不重)。
func (s *shell) fetchNotices(since uint64) (noticePage, bool) {
	url := fmt.Sprintf("http://%s/api/notices?since=%d", s.addr, since)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return noticePage{}, false
	}
	if c := cookieHeader(s.gahToken); c != "" {
		req.Header.Set("Cookie", c)
	}
	resp, err := gahClient().Do(req)
	if err != nil {
		return noticePage{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusServiceUnavailable {
		// 宿主没装配提示通道(ctx.notices):记一次即可,不当错误刷日志。
		if !s.noticeSvcMissing.Swap(true) {
			shellLogf("提示流不可用:宿主未装配 ctx.notices(系统通知将只剩回合结束这一条)")
		}
		return noticePage{}, false
	}
	if resp.StatusCode != http.StatusOK {
		return noticePage{}, false
	}
	var page noticePage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return noticePage{}, false
	}
	return page, true
}

// getState 取一次 /api/state 原始体(token 模式带 cookie)。
func (s *shell) getState() ([]byte, bool) {
	req, err := http.NewRequest(http.MethodGet, "http://"+s.addr+"/api/state", nil)
	if err != nil {
		return nil, false
	}
	if c := cookieHeader(s.gahToken); c != "" {
		req.Header.Set("Cookie", c)
	}
	resp, err := gahClient().Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 0, 8192)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil || len(buf) > 1<<20 {
			break
		}
	}
	return buf, resp.StatusCode == http.StatusOK
}

// sendNotification 发一条系统通知(未装配通知服务时只记日志,不假装成功)。
func (s *shell) sendNotification(title, body string) error {
	if s.notifier == nil {
		return fmt.Errorf("通知服务未装配")
	}
	return s.notifier.native(title, body)
}

// nowUnixMilli 通知 ID 用的时间戳(毫秒)。
func nowUnixMilli() int64 { return time.Now().UnixMilli() }
