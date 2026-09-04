// 交互式命令选择器:输入 / 前缀自动激活选项列表(命令名),↑/↓ 移动、Enter 应用;
// 命令声明的参数级(Args)级联推进:sandbox → ro|ws|full → 选完即执行;
// 自由输入级(枚举器返回空/nil)断点回输入框,用户补文本后 Enter 提交。
package tui

import (
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Pick 选择器状态(Level 0=命令选择,1+=参数级)。
type Pick struct {
	Level  int
	Items  []sdk.Option
	Cursor int
}

// levelsFn 取命令的参数级枚举器(由 App 注入:查 ctx.commands 注册表)。
type levelsFn func(name string) []func(picked []string) []sdk.Option

// advanceEnter 回车推进选择:应用当前高亮项,决定新文本/下一级选择/是否提交。
// input 为当前输入(含 /);commit=true 时调用方提交执行。
func advanceEnter(input string, pick *Pick, levels levelsFn) (newInput string, next *Pick, commit bool) {
	if pick == nil || len(pick.Items) == 0 {
		return input, nil, true // 无选择态回车 = 直接提交(普通输入语义)
	}
	v := pick.Items[pick.Cursor].Value
	// 命令级(Level 0):input 只是过滤前缀(如 /s),不进入命令文本——仅选中命令名;
	// 参数级(input 为完整命令文本,如 /sandbox):已选值保留并追加新值。
	var picked []string
	if pick.Level == 0 {
		picked = []string{v}
	} else {
		picked = append(strings.Fields(strings.TrimPrefix(input, "/")), v)
	}
	newInput = "/" + strings.Join(picked, " ")

	if pick.Level == 0 {
		return advanceLevel0(newInput, picked, v, levels)
	}
	return advanceLevelN(newInput, picked, pick.Level, levels)
}

// advanceLevel0 命令选中后的推进。
func advanceLevel0(newInput string, picked []string, name string, levels levelsFn) (string, *Pick, bool) {
	lv := levels(name)
	if len(lv) == 0 {
		return newInput, nil, true // 无参数级:直接执行(如 /help、/exit)
	}
	opts := evalLevel(lv, 0, picked)
	if len(opts) == 0 {
		if len(lv) == 1 {
			return newInput, nil, true // 唯一参数级无选项:执行
		}
		return newInput, nil, false // 断点:回输入框补自由参数
	}
	return newInput, &Pick{Level: 1, Items: opts}, false
}

// advanceLevelN 参数级选中后的推进(已选中某参数值)。
// Pick.Level 为 1-based 显示级(显示 Args[Level-1]),选中后下一参数级索引 = Level。
func advanceLevelN(newInput string, picked []string, curLevel int, levels levelsFn) (string, *Pick, bool) {
	lv := levels(picked[0]) // picked[0] 恒为命令名
	next := curLevel
	if lv == nil || next >= len(lv) {
		return newInput, nil, true // 无更多参数级:执行
	}
	opts := evalLevel(lv, next, picked)
	if len(opts) == 0 {
		if next == len(lv)-1 {
			return newInput, nil, true // 最后级无选项:执行
		}
		return newInput, nil, false // 中间级断点:回输入框
	}
	return newInput, &Pick{Level: next, Items: opts}, false
}

// pickLines 选项的显示行(渲染用):" /value desc"(命令级与参数级统一格式)。
func pickLines(opts []sdk.Option) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, " /"+o.Value+" "+o.Desc)
	}
	return out
}

// evalLevel 求值某级选项(枚举器可动态:插件/任务列表运行时求值)。
func evalLevel(lv []func(picked []string) []sdk.Option, i int, picked []string) []sdk.Option {
	if i >= len(lv) || lv[i] == nil {
		return nil
	}
	return lv[i](picked)
}
