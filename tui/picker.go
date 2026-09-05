// 交互式命令选择器:输入 / 前缀自动激活选项列表(命令名),↑/↓ 移动、Enter 应用;
// 命令声明的参数级(Args)级联推进:枚举级选完 → 下一级;自由级(FreeArgs)→ 断点
// 回输入框提示继续输入(如 /model 需模型名、/provider set 需 url/key);
// 无可定义级 → 直接执行(如 /help、/provider show)。
package tui

import (
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Pick 选择器状态(Level 0=命令选择,1+=参数级)。
// 参数级(Level>0)支持过滤:Filter 为键入的过滤词,Items = All 的匹配子集(滚动窗口随光标)。
type Pick struct {
	Level  int
	Items  []sdk.Option
	Cursor int
	All    []sdk.Option // 原始全量(进入本级时的选项;过滤/退格恢复用)。空 = Items 即全量。
	Filter string       // 参数级过滤词(非空时 Items 为其匹配子集)
}

// freeStep 多值自由参数逐步向导状态(如 /provider set 的 baseUrl/apiKey/model)。
// 仅自由参数序列 >1 的命令启用;每 Enter 确认一步(值入命令文本),尾可选步空回车 = 跳过。
type freeStep struct {
	Cmd    string   // 命令名(向导归属;命令变更即脱离)
	Params []string // 自由参数名序列(尾 '?' = 可选,须为最后一项)
	Base   int      // 断点时刻命令已含词数(命令名+已选枚举;自由词计数基准)
	Done   int      // 已确认输入的自由参数个数
}

// levelsFn 取命令的参数级定义(由 App 注入:查 ctx.commands 注册表)。
type levelsFn func(name string) []sdk.ArgLevel

// advanceResult 回车推进结果:新文本/下一级选择/是否提交/断点提示行/自由参数序列。
type advanceResult struct {
	Input  string
	Pick   *Pick
	Commit bool
	Hints  []string // 断点时提示“继续输入”的自由参数行(渲染于输入框下)
	Free   []string // 自由断点:待逐级输入的自由参数名序列(>1 启用逐步向导;单参数恒空)
}

// AdvanceEnter 回车推进选择:应用当前高亮项,决定新文本/下一级/提交/断点提示。
func AdvanceEnter(input string, pick *Pick, levels levelsFn) advanceResult {
	if pick == nil || len(pick.Items) == 0 {
		return advanceResult{Input: input, Commit: true} // 无选择态回车 = 直接提交
	}
	v := pick.Items[pick.Cursor].Value
	// 命令级(Level 0):input 只是过滤前缀(如 /s),不进入命令文本——仅选中命令名;
	// 参数级(input 为完整命令文本):已选值保留并追加。
	var picked []string
	if pick.Level == 0 {
		picked = []string{v}
	} else {
		picked = append(strings.Fields(strings.TrimPrefix(input, "/")), v)
	}
	newInput := "/" + strings.Join(picked, " ")

	if pick.Level == 0 {
		return advanceLevel0(newInput, picked, v, levels)
	}
	return advanceLevelN(newInput, picked, pick.Level, levels)
}

// advanceLevel0 命令选中后的推进。
func advanceLevel0(newInput string, picked []string, name string, levels levelsFn) advanceResult {
	lv := levels(name)
	if len(lv) == 0 {
		return advanceResult{Input: newInput, Commit: true} // 无参数级:直接执行
	}
	return advanceInto(newInput, picked, lv, 0, levels)
}

// advanceLevelN 参数级选中后的推进(已选中某参数值)。
// Pick.Level 为 1-based 显示级(显示 Args[Level-1]),选中后下一参数级索引 = Level。
func advanceLevelN(newInput string, picked []string, curLevel int, levels levelsFn) advanceResult {
	lv := levels(picked[0]) // picked[0] 恒为命令名
	next := curLevel
	return advanceInto(newInput, picked, lv, next, levels)
}

// advanceInto 进入第 idx 级:枚举 → 继续选择;自由 → 断点回输入框(带提示);
// 无定义/越界 → 直接执行(最后一级无更多输入)。
func advanceInto(newInput string, picked []string, lv []sdk.ArgLevel, idx int, levels levelsFn) advanceResult {
	next := idx
	if lv == nil || next >= len(lv) {
		return advanceResult{Input: newInput, Commit: true} // 无更多级:执行
	}
	if lv[next].Options != nil {
		if opts := lv[next].Options(picked); len(opts) > 0 {
			return advanceResult{Input: newInput, Pick: &Pick{Level: next + 1, Items: opts, All: opts}}
		}
		// Options 空:继续看本级的自由定义
	}
	if lv[next].FreeArgs != nil {
		if free := lv[next].FreeArgs(picked); len(free) > 0 {
			// 自由级断点:保留文本回输入框,提示继续输入参数。
			// 尾随空格:用户直接打字即拼成 "/cmd <词>",否则粘连成 "/cmd<词>"
			// (断点态输入的词会误拼进命令名,提交报未知命令/误走普通消息)。
			// 多值参数(free >1):返回序列供逐步向导逐级 Enter(见 freeStep)。
			return advanceResult{Input: newInput + " ", Hints: freeStepHints(newInput, free, 0), Free: free}
		}
	}
	return advanceResult{Input: newInput, Commit: true} // 无定义级:直接执行
}

// freeStepHints 自由断点提示行:单参数保持既有文案(兼容);多参数按逐步向导显示当前步。
func freeStepHints(input string, free []string, idx int) []string {
	name := ""
	if f := strings.Fields(strings.TrimPrefix(input, "/")); len(f) > 0 {
		name = f[0]
	}
	n := len(free)
	if n <= 1 {
		out := []string{" 继续输入 " + strings.Join(free, " | ")}
		if name != "" {
			out = append(out, fmt.Sprintf(" /%s %s", name, strings.Join(free, " ")))
		}
		return out
	}
	cur := free[idx]
	disp := strings.TrimSuffix(cur, "?") // '?' 尾 = 可选参数(仅允许最后一步)
	tail := ""
	if idx == n-1 && strings.HasSuffix(cur, "?") {
		tail = "(回车跳过)"
	}
	out := []string{fmt.Sprintf(" 第 %d/%d 步:输入 %s%s", idx+1, n, disp, tail)}
	if name != "" {
		out = append(out, fmt.Sprintf(" /%s … %s", name, disp))
	}
	return out
}

// pickLines 选项的显示行(渲染用):“ /value desc”(命令级与参数级统一格式)。
func pickLines(opts []sdk.Option) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, " /"+o.Value+" "+o.Desc)
	}
	return out
}

// filterOptions 选项子串过滤(大小写不敏感;Value 或 Desc 任一命中)。q 空 = 原样返回。
func filterOptions(items []sdk.Option, q string) []sdk.Option {
	if q == "" {
		return items
	}
	lq := strings.ToLower(q)
	var out []sdk.Option
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Value), lq) ||
			strings.Contains(strings.ToLower(it.Desc), lq) {
			out = append(out, it)
		}
	}
	return out
}
