// extract.go:自动生成记忆的**宿主侧**(回合结束时的一次后台提取)。
//
// 三条纪律(都在代码里落地,不是注释):
//  1. **不阻塞回合**:ObserveTurn 只投队列(满则丢弃并记账),模型请求在独立 goroutine 里跑;
//  2. **不静默提权**:只有 memory.AutoWriteKinds + confidence ≥ 0.8 + 非重复才直写,其余进候选;
//  3. **失败不是"没有新事实"**:llm/parse 失败与"模型返回空 items"分别记在 TotalFailed / TotalEmpty。
package hostmemory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// extractQueueCap 队列深度:提取是"顺带做的事",积压超过几条就说明回合太密,
// 丢掉新的比排队到用户下次会话更诚实(记账为 busy)。
const extractQueueCap = 8

// 提取模式的 data 取值。
const (
	extractRule = "rule"
	extractOff  = "off"
)

type extractor struct {
	c   sdk.Ctx
	svc *Service

	q    chan sdk.TurnDigest
	quit chan struct{}
	wg   sync.WaitGroup

	mu   sync.Mutex
	st   memory.ExtractState
	last sdk.MemoryExtractSnapshot
}

func newExtractor(c sdk.Ctx, svc *Service) *extractor {
	e := &extractor{c: c, svc: svc, q: make(chan sdk.TurnDigest, extractQueueCap), quit: make(chan struct{})}
	if st, err := memory.LoadExtractState(); err == nil {
		e.st = st
	} else {
		c.Logger().Warn("host-memory: 提取账本读取失败(按空账本继续)", "err", err)
	}
	e.wg.Add(1)
	go e.run()
	return e
}

func (e *extractor) close() {
	close(e.quit)
	e.wg.Wait()
}

// ObserveTurn 实现 sdk.MemoryExtractor:投队列,立刻返回。
func (e *extractor) ObserveTurn(_ context.Context, d sdk.TurnDigest) error {
	if strings.TrimSpace(d.UserText) == "" {
		return nil
	}
	select {
	case e.q <- d:
		return nil
	default:
		e.record(sdk.MemoryExtractSnapshot{At: time.Now(), Skipped: "busy"})
		return fmt.Errorf("host-memory: 提取队列已满(丢弃本回合;记账为 busy)")
	}
}

// run 单飞 worker:一次只跑一个提取请求(成本可控,也避免并发写记忆文件)。
func (e *extractor) run() {
	defer e.wg.Done()
	for {
		select {
		case <-e.quit:
			return
		case d := <-e.q:
			e.handle(d)
		}
	}
}

// handle 一次提取的完整流程(所有出口都记账)。
func (e *extractor) handle(d sdk.TurnDigest) {
	now := time.Now()
	if !e.enabled() {
		e.record(sdk.MemoryExtractSnapshot{At: now, Skipped: "disabled"})
		return
	}
	// ① 规则闸:不命中就**不花钱**,也不算失败(账上 RuleHit 为空)。
	hit, rule := memory.IsMemorable(d.UserText)
	if !hit {
		e.record(sdk.MemoryExtractSnapshot{At: now})
		return
	}
	e.mu.Lock()
	ok, reason := e.st.Allow(d.SessionID, now)
	e.mu.Unlock()
	if !ok {
		e.mu.Lock()
		e.st.LastSkipped = reason
		e.mu.Unlock()
		e.record(sdk.MemoryExtractSnapshot{At: now, RuleHit: rule, Skipped: reason})
		return
	}

	ll, err := e.llm()
	if err != nil {
		e.record(sdk.MemoryExtractSnapshot{At: now, RuleHit: rule, Skipped: "llm", Err: err.Error()})
		return
	}
	// ② 发请求(记账在前:成本已经发生,失败也要算进去)。
	e.mu.Lock()
	e.st.Consume(d.SessionID, now, rule)
	e.mu.Unlock()
	model := ll.Model()
	maxTok := 700
	req := &sdk.LLMRequest{
		Model: model,
		Messages: []sdk.LLMMessage{
			{Role: sdk.RoleSystem, Content: extractSystemPrompt},
			{Role: sdk.RoleUser, Content: buildExtractPrompt(d, e.existingItems())},
		},
		MaxTokens:   &maxTok,
		Temperature: floatPtr(0.2),
		// 后台补写不需要推理;不显式关会被会话级思考档回填(白烧 token)。
		Thinking:    sdk.ThinkingOff,
		ThinkingSet: true,
	}
	resp, err := ll.Complete(context.Background(), req, nil)
	if err != nil {
		e.mu.Lock()
		e.st.TotalFailed++
		e.st.LastSkipped = "llm"
		e.mu.Unlock()
		e.persist()
		e.record(sdk.MemoryExtractSnapshot{At: now, RuleHit: rule, Model: model, Skipped: "llm", Err: err.Error()})
		return
	}
	props, err := memory.ParseProposals(resp.Message.Content)
	if err != nil {
		e.mu.Lock()
		e.st.TotalFailed++
		e.st.LastSkipped = "parse"
		e.mu.Unlock()
		e.persist()
		e.record(sdk.MemoryExtractSnapshot{At: now, RuleHit: rule, Model: model, Skipped: "parse", Err: err.Error()})
		return
	}

	snap := sdk.MemoryExtractSnapshot{At: now, RuleHit: rule, Model: model, Produced: len(props)}
	existing := e.existingItems()
	total := len(existing)
	for _, p := range props {
		sim := maxSimilarity(p.Text, existing)
		dec, reason := memory.Route(p, sim)
		// 用户把写入模式切成"全部进候选" ⇒ 连白名单类型也不直写(这是"我要自己看一遍"的表达)。
		if dec == memory.DecisionWrite && e.writeAutoOnly() {
			dec, reason = memory.DecisionCandidate, "mode_candidates"
		}
		if dec == memory.DecisionWrite {
			// 直写额度两道闸:当日条数 + 总量软上限;任一顶住就降级为候选(不丢)。
			e.mu.Lock()
			allow, why := e.st.AutoWriteAllowed(total)
			e.mu.Unlock()
			if !allow {
				dec, reason = memory.DecisionCandidate, why
			}
		}
		switch dec {
		case memory.DecisionWrite:
			if err := memory.AppendMeta(memory.UserPath(), p.Text, d.SessionID, p.Kind, p.Confidence); err != nil {
				// 写失败(同日重复/超限)不静默:降级为候选,让人还能看到它。
				if err := memory.Propose(p.Text, d.SessionID); err == nil {
					snap.Candidate++
				} else {
					snap.Dropped++
				}
				continue
			}
			snap.Written++
			total++
			e.mu.Lock()
			e.st.AutoWrites++
			e.mu.Unlock()
		case memory.DecisionCandidate:
			if err := memory.Propose(p.Text, d.SessionID); err != nil {
				snap.Dropped++ // 候选池满/当日额度满:如实计为丢弃,不假装成功
				continue
			}
			snap.Candidate++
			e.mu.Lock()
			e.st.Candidates++
			e.mu.Unlock()
		default:
			_ = reason
			snap.Dropped++
		}
	}
	if snap.Produced == 0 {
		e.mu.Lock()
		e.st.TotalEmpty++
		e.mu.Unlock()
	}
	e.persist()
	e.record(snap)
	e.emit(snap)
}

// enabled 提取是否开启:偏好位(用户一键关)+ data.extract(部署级关)。
func (e *extractor) enabled() bool {
	if prefs.Load().MemoryExtractOff {
		return false
	}
	return e.svc.extract != extractOff
}

// writeAutoOnly 用户是否把写入模式切成"全部进候选"(data 与偏好都可表达)。
func (e *extractor) writeAutoOnly() bool { return prefs.Load().MemoryWriteMode == "candidates" }

func (e *extractor) llm() (sdk.LLMService, error) {
	var ll sdk.LLMService
	if err := e.c.Inject("ctx.llm", &ll); err != nil || ll == nil {
		return nil, fmt.Errorf("ctx.llm 未装配")
	}
	return ll, nil
}

// existingItems 已有记忆(用于让模型自己指出重复,以及给 Route 算相似度)。
func (e *extractor) existingItems() []memory.Item {
	items, err := memory.LoadAll(e.svc.currentProjectKey())
	if err != nil {
		e.c.Logger().Warn("host-memory: 读已有记忆失败(提取时按空集合继续)", "err", err)
		return nil
	}
	return items
}

func (e *extractor) persist() {
	e.mu.Lock()
	st := e.st
	e.mu.Unlock()
	if err := st.Save(); err != nil {
		e.c.Logger().Warn("host-memory: 提取账本落盘失败(限流精度受影响)", "err", err)
	}
}

func (e *extractor) record(snap sdk.MemoryExtractSnapshot) {
	e.mu.Lock()
	e.last = snap
	e.mu.Unlock()
}

func (e *extractor) lastSnapshot() sdk.MemoryExtractSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.last
}

// emit 广播观测事件(界面/遥测可订阅;载荷不含记忆正文,只有计数)。
func (e *extractor) emit(snap sdk.MemoryExtractSnapshot) {
	e.c.Emit(context.Background(), "memory/extracted", map[string]any{
		"rule": snap.RuleHit, "produced": snap.Produced, "written": snap.Written,
		"candidate": snap.Candidate, "dropped": snap.Dropped, "model": snap.Model,
	}, sdk.Emit)
}

// maxSimilarity 与既有条目的最大相似度(0 = 没有可比的)。
func maxSimilarity(text string, items []memory.Item) float64 {
	var max float64
	for _, it := range items {
		if s := memory.Similarity(text, it.Content); s > max {
			max = s
		}
	}
	return max
}

func floatPtr(f float64) *float64 { return &f }

// extractSystemPrompt 提取器的 system 提示(首行带 LLMMemoryMarker,脚本化适配器据此识别)。
//
// 硬性禁止段是防污染的主闸:凭据、任务态、可查代码事实、人身评价、未确认的猜测 ——
// 这些写进长期记忆都是"会长期生效的错误"。
const extractSystemPrompt = sdk.LLMMemoryMarker + `你是记忆提取器。只提取**跨会话仍然成立**的事实。

输出严格 JSON(不要代码围栏、不要解释):
{"items":[{"text":"一句话事实(≤120字)","kind":"preference|convention|decision|pitfall|env","confidence":0.0-1.0,"cue":"用户原话片段(≤40字)"}]}

kind 定义:
- preference 用户偏好/技术选型/风格;convention 项目约定/命令/流程;
- decision 已拍板的决定(可带理由);pitfall 踩过的坑与解法;env 路径/端口/版本等环境事实。

硬性禁止(违反即整条丢弃,不要回抄):
- 凭据/令牌/密钥/私钥/密码;
- 当前任务进度、下一步计划、TODO(那是 todo 工具的职责);
- 能从代码或文档里当场查到的事实(某函数在哪、某常量是多少);
- 对人/组织的主观评价与猜测;
- 只在本会话内成立的临时约定。

没有值得记的内容就输出 {"items":[]}。不要为凑数编造。中文输出。`

// buildExtractPrompt 拼输入:本回合用户话 + 助手收尾 + 已有记忆(供模型自己指出重复)。
func buildExtractPrompt(d sdk.TurnDigest, existing []memory.Item) string {
	var sb strings.Builder
	sb.WriteString("本回合用户消息:\n")
	sb.WriteString(clipRunes(d.UserText, 2000))
	sb.WriteString("\n\n助手收尾:\n")
	if strings.TrimSpace(d.ReplyText) == "" {
		sb.WriteString("(本回合无文本收尾)")
	} else {
		sb.WriteString(clipRunes(d.ReplyText, 600))
	}
	if len(existing) > 0 {
		sb.WriteString("\n\n已有记忆(若本次提取的内容与其中一条重复,就不要重复输出;可在 cue 里注明\"已记过\"):\n")
		// 只给前 30 条 + 首 60 字:再多就是拿预算换噪声。
		for i, it := range existing {
			if i >= 30 {
				sb.WriteString("...(其余略)\n")
				break
			}
			fmt.Fprintf(&sb, "- [%s] %s\n", it.Kind, clipRunes(it.Content, 60))
		}
	}
	return sb.String()
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
