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
type Pick struct {
	Level  int
	Items  []sdk.Option
	Cursor int
}

// levelsFn 取命令的参数级定义(由 App 注入:查 ctx.commands 注册表)。
type levelsFn func(name string) []sdk.ArgLevel

// advanceResult 回车推进结果:新文本/下一级选择/是否提交/断点提示行。
type advanceResult struct {
	Input  string
	Pick   *Pick
	Commit bool
	Hints  []string // 断点时提示“继续输入”的自由参数行(渲染于输入框下)
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
			return advanceResult{Input: newInput, Pick: &Pick{Level: next + 1, Items: opts}}
		}
		// Options 空:继续看本级的自由定义
	}
	if lv[next].FreeArgs != nil {
		if free := lv[next].FreeArgs(picked); len(free) > 0 {
			// 自由级断点:保留文本回输入框,提示继续输入参数。
			// 尾随空格:用户直接打字即拼成 "/cmd <词>",否则粘连成 "/cmd<词>"
			// (断点态输入的词会误拼进命令名,提交报未知命令/误走普通消息)。
			return advanceResult{Input: newInput + " ", Hints: freeHintLines(newInput, free)}
		}
	}
	return advanceResult{Input: newInput, Commit: true} // 无定义级:直接执行
}

// freeHintLines 断点提示行(“继续输入”自由参数)。
func freeHintLines(input string, free []string) []string {
	name := ""
	if f := strings.Fields(strings.TrimPrefix(input, "/")); len(f) > 0 {
		name = f[0]
	}
	out := []string{" 继续输入 " + strings.Join(free, " | ")}
	if name != "" {
		out = append(out, fmt.Sprintf(" /%s %s", name, strings.Join(free, " ")))
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
