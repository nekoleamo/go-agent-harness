// 界面装饰层(S2.2 组件化):输入行 / 命令提示行 / 状态栏的文本构建。
// 从 render.go 抽出的无状态(仅读 s)纯函数,便于逐段单测;Render 按序拼装。
package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// renderInputRight 输入行右侧挂载信息(M15 pi 式):模型(含来源)· 思维 · 上下文/缓存。
// 运行指标从状态栏移到输入行右侧(pi 布局:底部右侧挂模型与用量),输入留白时可见。
func renderInputRight(s *State) string {
	var parts []string
	if s.Model != "" {
		m := s.Model
		switch {
		case s.ModelFrom == sdk.SourceRole:
			m += "(角色)" // 角色指定:不拼 provider 域名(该域名只属于会话通用适配器)
		case s.ModelSrc != "":
			m += "(" + s.ModelSrc + ")"
		}
		parts = append(parts, "模型: "+m)
	}
	// off 本来隐身(会话档里 off 是默认值,天天占位没信息量);
	// 但**角色强制 off** 是“它真的生效了”的唯一证据,得显出来。
	if s.Thinking != "" && (s.Thinking != "off" || s.ThinkingFrom == sdk.SourceRole) {
		t := s.Thinking
		if s.ThinkingFrom == sdk.SourceRole {
			t += "(角色)"
		}
		parts = append(parts, "思维: "+t)
	}
	// 上下文占用:**按水位分档着色**(中性/提示/警示),并且**不假精确**。
	//
	// 分档(2026-10-20 B3):<70% 中性 · 70–90% 提示(琥珀)· ≥90% 警示(红)。
	// 为什么要色:数字要用户自己算才知道"快满了",而"快满了"是需要**立刻**反应的事
	// (该 /compact 或换窗口更大的模型)—— 颜色让它在余光里就被看见。
	stats, statsStyle := "上下文 -", styleStatus
	if s.Stats.Requests > 0 {
		// 上下文占用用**最近一次请求的实测 prompt**(LastPromptTokens),不是累计 PromptTokens ——
		// 累计是多轮之和,多轮后会显示成 5000K/128K 这种越跑越大的假数字。
		used := s.Stats.LastPromptTokens
		switch {
		case used <= 0:
			// 有请求但拿不到实测占用(本轮未再请求/用量事件缺字段):显示 "?" 而不是
			// "0/128K (0%)" —— 后者是**假数字**(说"没占",其实是"不知道")。
			stats = "上下文 ?"
		case s.Stats.Window > 0:
			pct := used * 100 / s.Stats.Window
			stats = fmt.Sprintf("上下文 %s/%s (%d%%)", fmtK(used), fmtK(s.Stats.Window), pct)
			statsStyle = contextUsageStyle(pct)
		default:
			stats = fmt.Sprintf("上下文 %s", fmtK(used)) // 窗口未知:仅使用量(不假精确)
		}
		// 缓存命中率:状态栏用**累计**口径(稳定的"这場会话缓存省了多少")。
		// 逐轮命中率的诊断价值在 /cache(那里同时给累计与最近一轮)—— 状态栏只放一个数,
		// 两个口径摆在一起反而没人看得懂。
		if s.Stats.CachedTokens > 0 && s.Stats.PromptTokens > 0 {
			stats += fmt.Sprintf(" 缓存 %d%%", s.Stats.CachedTokens*100/s.Stats.PromptTokens)
		}
		if s.Stats.AutoSummary {
			// 自动压缩标记:上下文里有一部分已经是**摘要**了(模型看到的不是原话)。
			// 不标的话,用户看到占用突然变小却不知道为什么模型"忘了前面说过什么"。
			stats += " (含自动摘要)"
		}
	}
	parts = append(parts, statsStyle.Render(stats))
	return strings.Join(parts, " · ")
}

// contextUsageStyle 上下文占用的分档样式(中性/提示/警示)。
//
// 阈值 70% / 90%:70% 是"还能聊但该留意了",90% 是"再聊几句就要压缩"。
// 阈值写成常量而不是散在渲染里:换档位时只有一处要动。
func contextUsageStyle(pct int) *lipgloss.Style {
	switch {
	case pct >= 90:
		return styleError
	case pct >= 70:
		return styleBusy
	default:
		return styleStatus
	}
}

// fmtDur 耗时短格式:>=10s 整数秒,<10s 一位小数(状态栏展示)。
func fmtDur(d time.Duration) string {
	secs := d.Seconds()
	if secs >= 10 {
		return fmt.Sprintf("%.0fs", secs)
	}
	return fmt.Sprintf("%.1fs", secs)
}

// thinkEdgeColor 输入左缘竖线色:按思考等级映射(P5;对齐 pi thinkingOff/low/medium/high 边框色)。
func thinkEdgeColor(level string) color.Color {
	switch level {
	case "low":
		return fg(TokThinkLow)
	case "medium":
		return fg(TokThinkMed)
	case "high":
		return fg(TokThinkHigh)
	default:
		return fg(TokThinkOff)
	}
}

// renderMetricLine 指标行(M15 修正:F15.1):模型/思维/上下文独立一行置于输入区上方,
// 固定显示不随输入移动(输入多长都不挤压,信息完整不省略)。
func renderMetricLine(s *State) string {
	// 前导空格与状态栏左缘对齐(F15.4:倒数两行左对齐,模型前不顶格)
	return styleStatus.Render(" " + renderInputRight(s))
}

// renderInputLine 输入区**框内内容**(圆角矩形框由 renderInputFrame 外包):
// 提示符 + 块光标插入在光标处(前后分半);双按退出武装提示。
// 首行提示符 "❯ " 与续行缩进统一 2 列对齐;思考等级语义移至框边框色(renderInputFrame)。
// 超长输入(P4-6 多行 + 长行):长行按列宽折行(复用 wrapSegment,双宽字符不跨行,
// 不再横向截断);物理行数超过 maxRows(>0,由 render.go 按终端高度给)时以光标为锚
// 滚动窗口(复用 pickWindow 语义,光标触底/触顶滚),窗口上方省略指示行。
// 短行/无 maxRows 时输出与历史逐行等价(前缀对齐、光标行插块)。
// 光标块始终在光标物理行可见(跟随滚动),↑/↓/Home/End 移动即可查看并删改全文。
// 模型/上下文指标不在本行(M15 曾挂首行右缘,输入长时挤压——F15.1 拆独立指标行)。
func renderInputLine(s *State, width int, maxRows ...int) string {
	maxR := 0
	if len(maxRows) > 0 {
		maxR = maxRows[0]
	}
	// 文本列宽:框内内容区(左右边框 4 列)再减前缀 2 列(❯ / 续行缩进)
	w := width - 4 - 2
	if w < 1 {
		w = 1
	}
	phys, curPhys, curCol := inputPhys(s.Input, s.Cursor, w)
	ws, we := 0, len(phys)
	if maxR > 0 && len(phys) > maxR {
		ws, we = pickWindow(len(phys), curPhys, maxR)
	}
	var sb strings.Builder
	if ws > 0 { // 窗口上方仍有内容:省略指示行(前缀对齐)
		sb.WriteString("  " + styleMeta.Render(fmt.Sprintf("…↑ %d 行在窗口上方(↑/↓ 滚动)", ws)))
	}
	for i := ws; i < we; i++ {
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		if i > 0 { // 续行缩进(与提示符 ❯ 同 2 列宽;窗口滚动后窗口首行非逻辑首行,同样续行对齐)
			sb.WriteString("  ")
		} else if s.Answering { // S-P0-2:作答态提示符换形(单列宽:❓ 与 ❯ 同为 2 列),输入归属一眼可辨
			sb.WriteString(styleBusy.Render("❓ "))
		} else {
			sb.WriteString(stylePrompt.Render("❯ "))
		}
		r := []rune(phys[i])
		if i == curPhys {
			c := curCol
			if c < 0 {
				c = 0
			}
			if c > len(r) {
				c = len(r)
			}
			sb.WriteString(string(r[:c]))
			sb.WriteString(styleCursor.Render("█"))
			sb.WriteString(string(r[c:]))
		} else {
			sb.WriteString(phys[i])
		}
	}
	// 双按退出武装提示(防误触):第一次 Ctrl+C(输入为空)后高亮提醒再按一次才彻底退出
	if s.QuitArmed {
		sb.WriteString(" " + styleBusy.Render("⚠ 再按一次 Ctrl+C 彻底退出 (2s)"))
	}
	return sb.String()
}

// renderHintLines 命令提示区(输入 / 前缀时显示;选择器激活时高亮当前项)。
// hintItems 已含高亮样式(由 Render 依 Pick 状态构建),此处截取前 hintRows 行。
func renderHintLines(_ *State, hintItems []string, hintRows int) []string {
	var hints []string
	for i := 0; i < hintRows; i++ {
		hints = append(hints, hintItems[i])
	}
	if len(hintItems) > maxHintRows {
		hints = append(hints, styleMeta.Render("…"))
	}
	return hints
}

// approvalLabel 审批档位中文标签(M17 三档,对齐 Web 设置面板;未知值回退智能档)。
// tierSourceLabel 有效档偏离声明档的来源标签(策略器自报;未知来源按联动口径)。
func tierSourceLabel(from string) string {
	if from == sdk.TierSourceRole {
		return "角色收紧"
	}
	return "审批联动"
}

func approvalLabel(mode string) string {
	switch mode {
	case "open":
		return "开放"
	case "strict":
		return "严格"
	default:
		return "智能"
	}
}

// —— S-P2-4 可配置状态栏(/statusline) ——
//
// 设计:F15.3 基线（回合态集群 + 工作区/沙箱/审批/会话 四段）不变为**默认**;
// 用户可改集合与顺序。渲染拆成「逐项取值 + 按配置拼接」：
//   - 回合态集群(state/queue/questions/dock/last)组内以 " · " 连接(同一信息块);
//   - 其余段(workspace/sandbox/approval/session)以 " | " 连接(并列分区)。
// 空值项一律不渲染(不留悬空分隔符)。

// statuslineCluster 回合态集群:组内 " · "、与分区段 " | "。
var statuslineCluster = map[string]bool{
	"state": true, "queue": true, "questions": true, "dock": true, "last": true, "tps": true,
}

// defaultStatusline 基线默认项顺序(F15.3;不配置时逐字符等价旧输出)。
// NOND-N1:notice 置首 —— 提示是「需要人回来」的信号,无提示时该项渲染空串,
// 输出与旧基线逐字符一致。
// 第一百三十八批:tps 紧跟 last(两项同属「上一回合」的度量,拆开写才能各自 /statusline 开关)。
var defaultStatusline = []string{"notice", "state", "queue", "questions", "plan", "dock", "last", "tps", "workspace", "sandbox", "approval", "role", "session"}

// statuslineTokens 全部合法项(顺序无关;/statusline 错误提示与校验用)。
var statuslineTokens = []string{"notice", "state", "queue", "questions", "plan", "dock", "last", "tps", "workspace", "sandbox", "approval", "role", "session"}

// statuslineTokenDesc 项说明(/statusline 无参与错误提示用)。
var statuslineTokenDesc = map[string]string{
	"notice":    "最近一条提示(NOND-N1;详情 /notice)",
	"state":     "回合状态(思考中/执行工具;运行中带 Esc 提示)",
	"queue":     "转向/待发计数(转向 = 已注入本回合;待发 = 回告后自动续发)",
	"questions": "待答提问计数(S-P0-2)",
	"plan":      "规划进度(已完成/总步数;详情 /plan)",
	"dock":      "后台任务/子代理坞(S-P0-3)",
	"last":      "上一回合耗时",
	"tps":       "上一回合输出速率(输出 tok/回合时长;工具与审批等待计入分母)",
	"workspace": "工作区",
	"sandbox":   "沙箱档位",
	"approval":  "审批档位",
	"role":      "当前角色(空 = 未启用;切换 /role use <id>)",
	"session":   "会话名",
}

// statuslineItem 渲染单项(空串 = 该项当前无内容,拼接时跳过)。
func statuslineItem(s *State, token string) string {
	switch token {
	case "notice":
		// NOND-N1:只显示标题(正文在 /notice 浮层);级别决定颜色与标记。
		// 标题裁到 40 列:提示占满整条状态栏会把其它实时信号挤没。
		if s.Notice == nil {
			return ""
		}
		txt := truncWidthRunes(s.Notice.Title, 40)
		switch s.Notice.Level {
		case sdk.NoticeError:
			return styleError.Render("✗ " + txt + " (/notice)")
		case sdk.NoticeWarn:
			return styleBusy.Render("⚠ " + txt + " (/notice)")
		default:
			return styleStatus.Render("提示: " + txt)
		}
	case "state":
		if s.Running {
			frame := spinnerFrame(s.SpinnerIdx)
			if s.LastTool != "" {
				return styleBusy.Render(frame + " 执行工具: " + s.LastTool + " (Esc 取消)")
			}
			return styleBusy.Render(frame + " 思考中 (Esc 取消)")
		}
		return styleStatus.Render("空闲")
	case "queue":
		// 转向(已注入本回合,模型下一次请求就会看到)与待发(回合结束后续发)语义不同,
		// 分开展示:不分开用户会以为自己的话排到了下一回合。
		var parts []string
		if s.SteerCount > 0 {
			parts = append(parts, fmt.Sprintf("转向 %d(已注入本回合)", s.SteerCount))
		}
		if n := len(s.Queue); n > 0 {
			parts = append(parts, fmt.Sprintf("待发 %d (Alt+Up 取回)", n))
		}
		if len(parts) > 0 {
			return styleBusy.Render(strings.Join(parts, " · "))
		}
	case "questions":
		if n := len(s.Questions); n > 0 {
			hint := "❓ 待答"
			if n > 1 {
				hint = fmt.Sprintf("❓ 待答 %d", n)
			}
			if s.Answering {
				hint += "(Esc 退出作答)"
			} else {
				hint += "(/answer 作答)"
			}
			return styleBusy.Render(hint)
		}
	case "plan":
		// D2:规划进度(计划落盘了却没人看得见 —— 状态栏给一个常驻信号)。
		// 无规划 ⇒ 隐身(不占位):规划模式不是默认路径。
		if s.PlanTotal > 0 {
			txt := fmt.Sprintf("计划 %d/%d", s.PlanDone, s.PlanTotal)
			if s.PlanTitle != "" {
				txt += " · " + truncWidthRunes(s.PlanTitle, 18)
			}
			return styleBusy.Render(txt)
		}
	case "dock":
		if lbl := dockLabel(s.Dock); lbl != "" {
			if s.Dock.Running > 0 {
				return styleBusy.Render(lbl) // 有东西在跑 = 显眼信号
			}
			return styleStatus.Render(lbl)
		}
	case "last":
		if !s.Running && s.turnDur > 0 {
			return styleStatus.Render("上一回合 " + fmtDur(s.turnDur))
		}
	case "tps":
		// 第一百三十八批:只报**已结束**回合的输出速率(Tra 是当前会话的账本,天然不跨会话)。
		// 无速率不渲染(首个回合还没跑完 / provider 不报用量 ⇒ 摆个 0 只会让人以为“不快”)。
		if !s.Running {
			if v, ok := s.Traj.LastTps(); ok {
				return styleStatus.Render(tpsLabel(v))
			}
		}
	case "workspace":
		return styleStatus.Render("工作区: " + orDefault(s.Workspace, "?"))
	case "sandbox":
		return styleStatus.Render("沙箱: " + orDefault(s.Sandbox, string(sdk.SandboxWorkspace)))
	case "approval":
		if s.Approval != "" {
			txt := "审批: " + approvalLabel(s.Approval)
			// 有效档 != 声明档(目前只有角色收紧一种):把"实际按哪档裁决"一并说清
			if s.ApprovalEff != "" && s.ApprovalEff != s.Approval {
				txt += "→" + approvalLabel(s.ApprovalEff) + "(" + tierSourceLabel(s.ApprovalFrom) + ")"
			}
			return styleStatus.Render(txt)
		}
	case "role":
		// 条件项:无角色(基线)时渲染空串 —— 与旧基线逐字符等价,不占位。
		if s.Role != "" {
			return styleStatus.Render("角色: " + truncWidthRunes(s.Role, 32))
		}
	case "session":
		if s.Session != "" {
			return styleStatus.Render("会话: " + s.Session)
		}
	}
	return ""
}

// renderStatusLine 状态栏(/statusline 可配置;未配置 = F15.3 基线)。
//
// 布局(2026-10-20 B2):**左实时、右静态**。
//
// 为什么改:一条 80 列的终端上塞十来段,而工作区/沙箱/审批/角色/会话这几项几乎
// **永不变** —— 它们会把"会变的"(state/queue/questions/dock/last/tps)挤到边上,
// 眼睛要在两堆信息里找。分区后:**左边是会变的(要盯),右边是常驻的(扫一眼)**,
// 右区右对齐 ⇒ 两堆各占一端,互不干扰(pi/Codex 的底栏同款思路)。
//
// 放不下怎么办(窄终端):
//   - **实时项一个都不丢** —— 丢"待答 2"是把用户需要回来的信号弄没了;
//   - 放不下就不做右对齐,退回"实时在前、静态在后"的单行(与旧基线同序),
//     超宽部分由 clampFrame 从**右端**截断 ⇒ 被截掉的天然是静态项(优先级最低)。
//     (右对齐时若强行截断,被截的会是左边的实时项 —— 那才是事故。)
//
// 用户显式 /statusline 配置过 → 保持**用户顺序单行**(那是ta的意图,不由这里重排)。
func renderStatusLine(s *State, width int) string {
	if len(s.Statusline) > 0 {
		return renderStatusLineOrdered(s, width)
	}
	var live, static []string
	for _, tok := range defaultStatusline {
		txt := statuslineItem(s, tok)
		if txt == "" {
			continue
		}
		if statuslineLive[tok] {
			live = append(live, txt)
		} else {
			static = append(static, txt)
		}
	}
	l := strings.Join(live, " · ")
	r := strings.Join(static, " · ")
	out := l
	right := false
	if l != "" && r != "" {
		if width > 0 && 1+lipgloss.Width(l)+1+lipgloss.Width(r) <= width {
			// 两端对齐:左实时、右静态,中间留白。
			pad := width - 1 - lipgloss.Width(l) - lipgloss.Width(r)
			out = l + strings.Repeat(" ", pad) + r
			right = true
		} else {
			out = l + " | " + r
		}
	} else if r != "" {
		out = r
	}
	if out == "" {
		out = "空闲" // 兜底:不留空行(与旧基线"至少有一项"一致)
	}
	if right {
		return styleStatus.Render(" " + out)
	}
	return styleStatus.Render(" " + out + strings.Repeat(" ", maxInt(0, width-1-lipgloss.Width(out))))
}

// renderStatusLineOrdered 用户显式配置过的状态栏:按用户顺序单行渲染。
//
// 与默认布局的区别:不分区、不右对齐(用户要什么就给什么;超宽由 clampFrame 兜底)。
func renderStatusLineOrdered(s *State, width int) string {
	out := ""
	prevCluster := false
	for _, tok := range s.Statusline {
		txt := statuslineItem(s, tok)
		if txt == "" {
			continue
		}
		if out != "" {
			if statuslineCluster[tok] && prevCluster {
				out += " · "
			} else {
				out += " | "
			}
		}
		out += txt
		prevCluster = statuslineCluster[tok]
	}
	return styleStatus.Render(" " + out + strings.Repeat(" ", maxInt(0, width-1-lipgloss.Width(out))))
}

// statuslineLive 该项是否属"实时信号"组(左区;会变、要盯)。
//
// 划分依据:**信息会不会在这一轮内变**。state/queue/questions/dock/last/tps/notice 都会;
// workspace/sandbox/approval/role/session 是配置,一轮内基本不动(除非用户切)。
var statuslineLive = map[string]bool{
	"notice": true, "state": true, "queue": true, "questions": true, "plan": true,
	"dock": true, "last": true, "tps": true,
}

// maxInt 两数取大(避免为一个比较引入 math)。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
