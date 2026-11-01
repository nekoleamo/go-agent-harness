// 多题问卷(P3 语义交互的「整卷」扩展):一次问完 N 道题,带题目短标签/hint/
// 「自己输入」/「聊聊这个」,并保留「部分作答 + 整卷取消」语义。
//
// 为什么做成**可选能力**而不是 QuestionService 的新方法:ctx.question 的 provider 有
// fusion、tui 单 profile、web 单 profile 三个,再加第三方 UI 插件 —— 给接口加方法等于
// 强制所有实现者改签名(编译期破窗)。这里沿用 InteractionObserver / IsolatedFanout 的
// 「类型断言探测可选能力」惯例:实现 QuestionnaireService / QuestionnairePresenter 才走
// 整卷呈现,否则 sdk.AskQuestionnaire 自动逐题 Ask 降级(功能不丢,只是没有 tab/确认页)。
package sdk

import (
	"context"
	"fmt"
	"strings"
)

// Questionnaire 一卷多题(单题仍走 Question;本结构只承载「一次问完整卷」)。
type Questionnaire struct {
	ID        string     `json:"id,omitempty"`    // 整卷 id(事件/断连补推/前端 key)
	Title     string     `json:"title,omitempty"` // tab 栏上方标题(如「本轮新增待确认(4 项)」)
	Questions []Question `json:"questions"`       // 每题可带 Label/Hint/AllowOther/AllowChat
}

// QuestionnaireAnswer 整卷作答(Answers 与 Questions 同序,漏答为零值)。
//
// 有意不把崩掉的题裁掉:同序保位让消费方(工具/前端)能一眼看出"第几题没答",
// 也避免"过滤掉空值后答案错位"这种只有真机才发现的坑。
type QuestionnaireAnswer struct {
	Answers   []QuestionAnswer `json:"answers"`
	Cancelled bool             `json:"cancelled,omitempty"` // 用户整卷放弃(已答部分保留)
	Chat      string           `json:"chat,omitempty"`      // 用户要求就某题展开讨论(题 id)
}

// QuestionnaireService 整卷提问能力(可选):ctx.question 的 provider 实现它,
// 工具侧才会走「一次问完」(否则逐题 Ask)。
type QuestionnaireService interface {
	AskQuestionnaire(ctx context.Context, qn Questionnaire) (QuestionnaireAnswer, error)
}

// QuestionnairePresenter 整卷呈现能力(可选):UI 渠道实现它才会收到整卷广播;
// 未实现的渠道由宿主逐题 PresentQuestion 降级(见 Fusion.AskQuestionnaire)。
type QuestionnairePresenter interface {
	PresentQuestionnaire(ctx context.Context, qn Questionnaire) (<-chan QuestionnaireAnswer, func(), error)
}

// NormalizeQuestionnaire 补齐整卷与每题的标识/序号(纯函数,返回副本)。
//
// 只补「能定位到题」所需的三样:整卷 ID、卷内题 ID(<group>-<n>)、Index(1..N)、Group、
// 以及 tab 标签缺省(Q1/Q2…)。呈现侧自己的默认值(AllowOther/AllowChat 默认 true 等)
// 由调用方(工具)决定,避免把产品口径塞进契约层。
func NormalizeQuestionnaire(qn Questionnaire) Questionnaire {
	if qn.ID == "" {
		qn.ID = NewQuestionID()
	}
	qs := make([]Question, len(qn.Questions))
	copy(qs, qn.Questions)
	for i := range qs {
		if qs[i].ID == "" {
			qs[i].ID = fmt.Sprintf("%s-%d", qn.ID, i+1)
		}
		if qs[i].Index == 0 {
			qs[i].Index = i + 1
		}
		if qs[i].Group == "" {
			qs[i].Group = qn.ID
		}
		if qs[i].Label == "" {
			qs[i].Label = fmt.Sprintf("Q%d", i+1)
		}
	}
	qn.Questions = qs
	return qn
}

// AskQuestionnaire 工具侧唯一入口:provider 支持整卷就走整卷,否则逐题 Ask 降级。
// 未装配通道显式报错(与 QuestionService.Ask 同口径,不静默假答)。
func AskQuestionnaire(ctx context.Context, svc QuestionService, qn Questionnaire) (QuestionnaireAnswer, error) {
	if svc == nil {
		return QuestionnaireAnswer{}, fmt.Errorf("questionnaire: 提问通道未装配(需 host-confirm-fusion 与至少一个 UI 渠道)")
	}
	qn = NormalizeQuestionnaire(qn) // 幂等:保证每题都有 id/Index,降级路径也能按题定位
	if qsvc, ok := svc.(QuestionnaireService); ok {
		return qsvc.AskQuestionnaire(ctx, qn)
	}
	return AskQuestionnaireSequential(ctx, svc, qn)
}

// AskQuestionnaireSequential 逐题降级:每题一次 Ask(渠道体感是「连问 N 次」)。
// 已答部分在后续失败/取消时**保留**——收集到的信息不该因为最后一题而全丢。
func AskQuestionnaireSequential(ctx context.Context, svc QuestionService, qn Questionnaire) (QuestionnaireAnswer, error) {
	out := QuestionnaireAnswer{Answers: make([]QuestionAnswer, len(qn.Questions))}
	for i, q := range qn.Questions {
		ans, err := svc.Ask(ctx, q)
		if err != nil {
			return out, err
		}
		out.Answers[i] = ans
	}
	return out, nil
}

// QuestionnaireSummary 整卷作答摘要(事件展示/审计/会话流回显用;与 AnswerSummary 同源口径)。
// 未答(空值)的题显式标「(未答)」,不假装有答案。
func QuestionnaireSummary(qn Questionnaire, ans QuestionnaireAnswer) string {
	if ans.Chat != "" {
		label := ans.Chat // 题 id 定位,label 给人和模型看
		for _, q := range qn.Questions {
			if q.ID == ans.Chat && q.Label != "" {
				label = q.Label
				break
			}
		}
		return "就「" + label + "」展开讨论"
	}
	if ans.Cancelled {
		return "(整卷取消)"
	}
	parts := make([]string, 0, len(qn.Questions))
	for i, q := range qn.Questions {
		var a QuestionAnswer
		if i < len(ans.Answers) {
			a = ans.Answers[i]
		}
		label := q.Label
		if label == "" {
			label = fmt.Sprintf("Q%d", i+1)
		}
		if a.Empty() {
			parts = append(parts, label+"=(未答)")
			continue
		}
		parts = append(parts, label+"="+AnswerSummary(a))
	}
	if len(parts) == 0 {
		return "(空问卷)"
	}
	return strings.Join(parts, ";")
}
