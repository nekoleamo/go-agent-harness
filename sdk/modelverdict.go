// modelverdict.go:「这个模型能不能当 agent 用」的**单一判定源**。
//
// 为什么需要它:agent 与聊天模型的要求差别很大 —— gah 靠**结构化 tool_calls** 让模型
// 驱动工具(见 LLMRequest.Tools 与适配器的 tools 字段),一个不支持工具调用的模型在 gah 里
// 只能正文里"假装"调用,工具永不执行(那是历史 P0 的成因)。所以「模型在不在 /models 里」
// 远不够,还要判它**能不能干活**。
//
// 为什么不用模型名去猜:模型名几个月一变 —— 实测 OpenRouter 的 `:free` 清单一天内就换了一半,
// 我记忆里的 llama/gemini 免费模型**一个都不在了**。名字猜不出能力,端点自述才作数。
//
// 三端(TUI 选择器 / Web 下拉 / 任何将来展示)都调这里,避免三处各判一次、结论漂移。
// 判定是**纯函数**:零网络、零配置,可单测。
package sdk

import (
	"sort"
	"strings"
)

// VerdictFree 仅免费模型专用阈值:低于它就该提醒(长任务输出中途被截断比报错更难查)。
//
// 取 8192:免费档位的输出上限普遍很小(实测 16 个 :free 模型里最低的就是 8192),
// 而一次工具调用的 JSON 参数 + 说明文字很容易超过 8K,截断发生在半句话上时模型不会报错,
// 只会给出一个缺尾的 tool_call —— 那会变成很难定位的「工具参数错误」。
const VerdictFreeOutputWarn = 8192

// VerdictAgentContextFloor 能当 agent 用的最低上下文建议值。
//
// 取 64KiB:一次系统提示(技能+指令+角色)+ 工具 schema + 历史 + 文件内容很容易到几万 token,
// 低于 64K 的模型会在中段任务里直接撞窗(而 gah 的对策是自动压缩,压缩掉的可能正是有用的历史)。
const VerdictAgentContextFloor = 64 * 1024

// ModelVerdict 一个模型对 agent 的可用性判定(展示层直接用这几个字段,不再自己判断)。
type ModelVerdict struct {
	// Free 价格自述为 0(PriceKnown 为真时才有意义)。
	Free bool
	// ToolsKnown 端点是否声明过工具调用能力(nil = 未声明)。
	ToolsKnown bool
	// Tools 声明支持工具调用。
	Tools bool
	// Vision 声明支持图片输入。
	Vision bool
	// ContextWindow 上下文窗口(0 = 未知)。
	ContextWindow int
	// Usable 能不能当 agent 用(端点**声明不支持工具调用**时为假;未声明一律当真,见下文)。
	// 上下文未声明,或 ≥ VerdictAgentContextFloor 时不构成障碍。
	//
	// 刻意**不因"未声明工具调用"就把未声明当不可用** —— 绝大多数 provider 的 /models 不返回
	// 这个字段(实测 DeepSeek 这类只给 id/owned_by),按「未知即不可用」会把正常 provider
	// 全部清空。因此:未声明(ToolsKnown=false)→ Usable 仍为真,但标签不加「工具」。
	Usable bool
	// Tags 展示用短标签(免费 / 工具 / 看图 / 1M)。空表示没有可展示的强项。
	Tags []string
	// AutoRouter 是否是「自动路由」型模型(id = FreeRouterModelID:由 OpenRouter 自己挑一个
	// 当前可用的免费模型)。这类 id **不会随免费清单变动而失效**,所以该给可选项里置顶:
	// 写死某个免费模型名会过期(实测免费清单一天内换了一半),而它是官方维护的。
	AutoRouter bool
	// Warn 一句人话警告(可空)。**只说影响,不说"不推荐"** —— 替用户下判断不如把事实摆出来。
	Warn string
}

// AssessModel 判定一个模型对 agent 的可用性(纯函数;TUI/Web 共用)。
//
// 阈值与理由见本文件头部与两个常量。刻意不做的事:不打分、不排名、不推荐特定模型 ——
// 端点的能力自述只说明「它能干什么」,好不好用只有用户的活儿能回答。
func AssessModel(m ModelInfo) ModelVerdict {
	v := ModelVerdict{
		ToolsKnown:    m.SupportsTools != nil,
		Tools:         m.SupportsTools != nil && *m.SupportsTools,
		Vision:        m.SupportsVision(),
		ContextWindow: m.ContextWindow,
	}
	if m.PriceKnown {
		v.Free = m.PromptPrice <= 0
	}
	v.AutoRouter = m.ID == FreeRouterModelID
	// Usable 能不能当 agent 用。
	//
	// **「端点没声明」不等于「不支持」** —— 实测绝大多数 provider 的 /models 只给 id/owned_by,
	// 若按「必须显式声明 tools 才算可用」判定,DeepSeek/Kimi/通义这些端点的模型会**全部被判死**,
	// 而「只看 agent 可用」过滤又默认开着,那等于直接清空用户的模型列表(本文件自己的测试钩住这条)。
	// 所以:未声明 ⇒ 照常可用、不加「工具」标签;明确声明不支持 ⇒ 才判死并说清原因。
	v.Usable = (v.Tools || !v.ToolsKnown) &&
		(v.ContextWindow == 0 || v.ContextWindow >= VerdictAgentContextFloor)

	if v.Free {
		v.Tags = append(v.Tags, "免费")
	}
	if v.AutoRouter {
		v.Tags = append(v.Tags, "自动路由")
	}
	if v.Tools {
		v.Tags = append(v.Tags, "工具")
	}
	if v.Vision {
		v.Tags = append(v.Tags, "看图")
	}
	switch {
	case v.ContextWindow >= 1024*1024:
		v.Tags = append(v.Tags, "1M 上下文")
	case v.ContextWindow >= 256*1024:
		v.Tags = append(v.Tags, "256K 上下文")
	}

	// 警告按「会不会让活儿干不成」排序:先说真的会让工具调用失败的,再说让输出残缺的。
	switch {
	case v.ToolsKnown && !v.Tools:
		v.Warn = "端点声明不支持工具调用:只能聊天,当不了 agent 的脑子(工具不会执行)"
	case v.ContextWindow > 0 && v.ContextWindow < VerdictAgentContextFloor:
		v.Warn = "上下文偏小,长任务容易撞窗(gah 会自动压缩历史,可能压掉有用的部分)"
	case v.Free && m.MaxOutputTokens > 0 && m.MaxOutputTokens < VerdictFreeOutputWarn:
		v.Warn = "单次输出上限偏低,工具调用参数可能被截在半句上"
	}
	return v
}

// VendorHint 模型厂商(端点给了 owned_by 就用它,否则取 id 的 `/` 前段 —— OpenRouter 这类
// 端点不返回 owned_by,以前模型列表里厂商列全是空)。
func (m ModelInfo) VendorHint() string { return m.vendorHint() }

// ModelIsAgentUsable 便捷判定(列表过滤用)。
func ModelIsAgentUsable(m ModelInfo) bool { return AssessModel(m).Usable }

// ModelSelectRank 模型选择器的排序权重(**两端共用**,见下)。
//
// 为什么必须有它、且必须放在 sdk:模型选择器有**两份实现** —— TUI 自己的那份
// (tui/app.go modelOptions)与宿主命令那份(host-internal-commands 的 modelOptions)。
// 早先两份各写各的,于是改了一边另一边不动,用户在 Web 看到徽标、在 TUI 里看不到。
// 排序规则同属「会让人选错模型」那一类(免费/能不能干活/上下文大小),所以和 AssessModel
// 放在同一个包里,而不是让两份实现各自复述。
//
// 权重是刻意粗糙的整数:「能干活 > 免费 > 上下文大」三个维度同权,目的是让值得选���浮上来,
// 而不是拼出一个看似精确的名次 —— 名次在这里没有事实依据。
func ModelSelectRank(m ModelInfo) int {
	v := AssessModel(m)
	rank := 0
	if !v.Usable {
		rank += 1000
	}
	if v.AutoRouter {
		rank -= 50 // 官方自动路由:不会因某个免费模型下线而失效,置顶
	}
	if v.Free {
		rank -= 10
	}
	rank -= v.ContextWindow / 1_000_000
	return rank
}

// ModelSelectEntry 一条已装配好的模型选项(排序用的中间形状)。
type ModelSelectEntry struct {
	// Value 选项值:多 provider 时为 "provider|model",单 provider 时为模型 ID。
	Value string
	// Desc 描述(用 ModelOptionDesc 生成)。
	Desc string
	// Rank 排序权重(ModelSelectRank)。
	Rank int
}

// NewModelSelectEntry 装配一条选项。
func NewModelSelectEntry(value string, m ModelInfo, source string) ModelSelectEntry {
	return ModelSelectEntry{Value: value, Desc: ModelOptionDesc(m, source), Rank: ModelSelectRank(m)}
}

// SortModelSelectEntries 按权重排序(**稳定**;同权重按 Value 字典序 —— 跨 provider 合并后
// 必须有确定性 tie-break,否则每次打开列表顺序可能不同,用户会以为列表在动)。
func SortModelSelectEntries(entries []ModelSelectEntry) []ModelSelectEntry {
	out := append([]ModelSelectEntry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Rank != out[j].Rank {
			return out[i].Rank < out[j].Rank
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// ModelOptionDesc 选项描述:来源 + 厂商 + 能力标签 + 不可用原因(TUI 与 Web 同一口径)。
//
// 格式上有意保持两端一致;web 端是把本函数的产物拆成 label(前两段)与 tags/warn(后两段)
// 分别渲染,不是各写一套文案。
func ModelOptionDesc(m ModelInfo, source string) string {
	v := AssessModel(m)
	base := m.ID + " (来源 " + source
	if owner := m.VendorHint(); owner != "" && !strings.HasPrefix(m.ID, owner+"/") {
		base += "/归属 " + owner
	}
	base += ")"
	if len(v.Tags) > 0 {
		base += " · " + strings.Join(v.Tags, " · ")
	}
	if v.Warn != "" {
		base += " ⚠ " + v.Warn
	}
	return base
}

// FreeRouterModelID OpenRouter 的「自动路由到某个免费模型」模型 id。
//
// 为什么推荐它而不是让用户在几十个 :free 模型里挑:免费清单**天天在变** —— 实测同一个
// `:free` 后缀下的模型名单一天内就换了一半,任何写死的推荐名都会过期,而这个 id 由 OpenRouter
// 自己维护(它会挑一个当前可用且合口的免费模型),所以「不挑」反而是最稳的默认。
const FreeRouterModelID = "openrouter/free"
