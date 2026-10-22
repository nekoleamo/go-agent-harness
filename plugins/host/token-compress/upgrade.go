// upgrade.go:滚动摘要的**模型摘要升级**通道(2026-10-21 批次 D4)。
//
// 为什么要这条通道:抽取式摘要只是"逐条截断"(用户: 前 120 字 / 助手: 前 200 字),
// 信息密度低、不记"改过哪些文件 / 什么决定 / 未完成什么"。pi / dsh / Codex 都是
// **模型摘要**。但折叠发生在**会话日志锁内**(host-session-log 投影时回调),
// 就地调模型会把锁按住几秒、拖住整个会话的写入 ⇒ 模型摘要只能**事后异步**补写。
//
// 三条纪律(缺一条就会出事):
//
//  1. **不在折叠当场调模型**:折叠当场只落**抽取式**摘要(确定性、零延迟、立刻把
//     占用压回预算内),模型摘要是**事后补写**。
//  2. **补写必须原子**:补写期间可能又折叠了一次 ⇒ 写进去会让投影回退到更旧的摘要
//     (丢掉刚折进去的那一段)。用乐观锁(当前最新摘要 == 我看到的那条)挡住,
//     不匹配就放弃(保持抽取式 = 确定性回退)。
//  3. **模型不可用就回退**:摘要器缺失/超时/解析失败 ⇒ 什么都不写,抽取式摘要原样
//     继续用。摘要质量是锦上添花,**不许让回合失败**。
//
// 触发时机:回合结束(turn/end)之后 —— 避免与本回合的模型请求并发抢同一个端点
// (host-session-summary 也是这个口径:回合后异步,不阻塞回合收尾)。
package tokencompress

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// FoldScope 一次折叠产生的范围(交给可替换的摘要器)。
type FoldScope struct {
	// Prev 折叠前的累计摘要(文本;空 = 首次折叠)。
	Prev string
	// Seg 本次新折叠进来的片段(**原始内容**转述,不是抽取式截断版 ——
	// 模型摘要的价值就在于它能看到原文,抽取式已经把细节丢了)。
	Seg string
	// Merged 抽取式合并后的累计摘要(= 落盘的那条文本)。
	// 升级时作为乐观锁的 expected:期间又折叠过 ⇒ 对不上 ⇒ 放弃。
	Merged string
}

// onFold 折叠后的回调(Engine 的可选钩子;nil = 不做模型摘要)。
func (e *Engine) onFoldHook(scope FoldScope) {
	if e.onFold != nil {
		e.onFold(scope)
	}
}

// upgradeRunner 模型摘要升级的执行器(插件持有)。
type upgradeRunner struct {
	c        sdk.Ctx
	sessions sdk.SessionLog
	timeout  time.Duration
	maxLimit int
	enabled  bool // data.llm_summary=false ⇒ 整条通道不工作(纯抽取式)
	dis      sdk.Disposer

	mu      sync.Mutex
	pending *FoldScope // 最新一次折叠的范围(只留最新:更早的范围已被它覆盖)
	busy    bool       // 全局单飞:同一时刻只跑一次升级
	sum     sdk.SessionSummarizer
	sumDone bool
}

// close 撤销订阅(注册即副作用,卸载即撤销)。
func (u *upgradeRunner) close() {
	if u.dis != nil {
		u.dis()
		u.dis = nil
	}
}

// upgradeOpts 模型摘要升级的配置(manifest data)。
type upgradeOpts struct {
	enabled  bool          // 是否启用(缺省开;data.llm_summary=false 关)
	timeout  time.Duration // 单次调模型的超时(缺省 60s)
	maxFixed int           // 摘要长度上限(0 = 按预算派生:预算/2)
}

// upgradeOptions 读 manifest data(缺省见常量;非法值按缺省,不静默当 0)。
func upgradeOptions(m *sdk.Manifest) upgradeOpts {
	o := upgradeOpts{enabled: true, timeout: 60 * time.Second}
	if m == nil || m.Data == nil {
		return o
	}
	if v, ok := m.Data["llm_summary"].(bool); ok {
		o.enabled = v
	}
	if v, ok := m.Data["llm_summary_timeout_seconds"].(int); ok && v > 0 {
		o.timeout = time.Duration(v) * time.Second
	}
	if v, ok := m.Data["llm_summary_max_chars"].(int); ok && v > 0 {
		o.maxFixed = v
	}
	return o
}

// maxChars 摘要长度上限:固定值优先,否则按预算派生(与抽取式摘要同一口径:预算的一半)。
func (o upgradeOpts) maxChars(budget int) int {
	if o.maxFixed > 0 {
		return o.maxFixed
	}
	if budget > 0 {
		return budget / 2
	}
	return 2000
}

// newUpgradeRunner 构造升级执行器。
func newUpgradeRunner(c sdk.Ctx, sessions sdk.SessionLog, timeout time.Duration, maxLimit int) *upgradeRunner {
	return &upgradeRunner{c: c, sessions: sessions, timeout: timeout, maxLimit: maxLimit}
}

// onFold 登记待升级的范围(最新覆盖旧的:旧的已被更晚的折叠取代)。
func (u *upgradeRunner) onFold(scope FoldScope) {
	if !u.enabled {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	s := scope
	u.pending = &s
}

// trigger 回合结束时的触发点:有待升级范围就跑一次(单飞;忙时留到下一回合)。
func (u *upgradeRunner) trigger() {
	if !u.enabled {
		return
	}
	u.mu.Lock()
	if u.busy || u.pending == nil {
		u.mu.Unlock()
		return
	}
	scope := *u.pending
	// 取走就**清空**:同一个折叠范围只升级一次。
	//
	// 为什么(2026-10-21 review):不清空的话,下一次回合结束又会拿**同一个**范围再跑一遍
	// —— 白花一次模型调用(而且乐观锁必然对不上:最新摘要已被上一轮换成了模型摘要)。
	u.pending = nil
	u.busy = true
	u.mu.Unlock()
	go u.run(scope)
}

// run 执行一次升级(失败一律"什么都不写"= 保持抽取式摘要)。
func (u *upgradeRunner) run(scope FoldScope) {
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()
	sum := u.summarizer()
	if sum == nil {
		return // 没有可替换的摘要实现 ⇒ 保持抽取式
	}
	ctx, cancel := context.WithTimeout(context.Background(), u.timeout)
	defer cancel()
	text, err := sum.Summarize(ctx, sdk.SummaryRequest{
		Prev: scope.Prev, Seg: scope.Seg, MaxChars: u.maxLimit,
	})
	if err != nil {
		u.logf("模型摘要失败,保持抽取式摘要: %v", err)
		return
	}
	text = clip(text, u.maxLimit)
	if strings.TrimSpace(text) == "" {
		return // 空摘要不写(否则投影会得到一条空摘要)
	}
	up, ok := u.sessions.(sdk.SessionSummaryUpgrader)
	if !ok {
		return // 宿主不支持原子替换 ⇒ 保持抽取式
	}
	replaced, err := up.UpgradeSummary(scope.Merged, text, sdk.SummarySourceLLM)
	if err != nil {
		u.logf("摘要替换失败,保持抽取式摘要: %v", err)
		return
	}
	if !replaced {
		// 期间又折叠了:当前摘要已经更新,本次升级作废(下一次折叠会重新登记)。
		u.logf("摘要已被更新(期间又折叠),放弃本次模型摘要")
	}
}

// summarizer 懒解析摘要实现(ctx.sessionSummarizer;缺失 = 纯抽取式)。
//
// 为什么懒解析而不是 Start 期注入:插件的启动顺序由依赖图决定,而"模型摘要"是
// **可选能力** —— 谁先启动谁后启动都该能工作(与 ctx.llm 的可选注入同款思路)。
//
// **解析失败不记成"已解析"**:提供方可能晚于第一次尝试才装配好(装配顺序不保证),
// 记成已解析会让这条通道**永久**关着(直到重启)。失败 ⇒ 下次再试。
func (u *upgradeRunner) summarizer() sdk.SessionSummarizer {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.sumDone {
		return u.sum
	}
	var s sdk.SessionSummarizer
	if err := u.c.Inject("ctx.sessionSummarizer", &s); err == nil && s != nil {
		u.sum = s
		u.sumDone = true
	}
	return u.sum
}

// logf 记一条日志(宿主 logger 可能为空)。
func (u *upgradeRunner) logf(format string, args ...any) {
	if u.c == nil || u.c.Logger() == nil {
		return
	}
	u.c.Logger().Debug("token-compress: "+format, args...)
}
