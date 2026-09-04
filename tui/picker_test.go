// 交互式选择器推进单测:级联逐级、动态枚举、自由级断点、执行时机。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func opt(value, desc string) sdk.Option {
	return sdk.Option{Value: value, Desc: desc}
}

// staticOpts 静态枚举器。
func staticOpts(opts ...sdk.Option) func([]string) []sdk.Option {
	return func([]string) []sdk.Option { return opts }
}

// TestAdvanceNoArgCommand 无参数级命令:Enter 选中即直接执行。
func TestAdvanceNoArgCommand(t *testing.T) {
	levels := func(name string) []func([]string) []sdk.Option { return nil }
	in, pick, commit := advanceEnter("/", &Pick{Items: []sdk.Option{opt("help", "帮助")}}, levels)
	if !commit {
		t.Fatal("无参数命令应直接执行")
	}
	if in != "/help" {
		t.Fatalf("文本应为 /help: %q", in)
	}
	if pick != nil {
		t.Fatalf("执行后选择态应清空: %+v", pick)
	}
}

// TestAdvanceCascade 级联:sandbox → ro|ws|full → 选完执行。
func TestAdvanceCascade(t *testing.T) {
	levels := func(name string) []func([]string) []sdk.Option {
		if name == "sandbox" {
			return []func([]string) []sdk.Option{
				staticOpts(opt("ro", "只读"), opt("ws", "工作区"), opt("full", "全放开")),
			}
		}
		return nil
	}
	// 第一级:选中 sandbox → 进入参数级
	in, pick, commit := advanceEnter("/", &Pick{Items: []sdk.Option{opt("sandbox", "沙箱")}}, levels)
	if commit || pick == nil {
		t.Fatalf("应进入参数级: commit=%v pick=%+v", commit, pick)
	}
	if in != "/sandbox" || pick.Level != 1 || len(pick.Items) != 3 {
		t.Fatalf("参数级状态不符: %q %+v", in, pick)
	}
	// 第二级:↑/↓ 移动选中 ws(Cursor=1)→ 无下一级 → 执行
	pick.Cursor = 1
	in, pick2, commit2 := advanceEnter(in, pick, levels)
	if !commit2 {
		t.Fatal("参数选完应执行")
	}
	if in != "/sandbox ws" {
		t.Fatalf("最终文本应为 /sandbox ws: %q", in)
	}
	// 验证文本可被命令分发直接执行(原 submit 语义)
	fields := strings.Fields(strings.TrimPrefix(in, "/"))
	if fields[0] != "sandbox" || fields[1] != "ws" {
		t.Fatalf("分发字段不符: %v", fields)
	}
	_ = pick2
}

// TestAdvanceDynamicLevel 动态枚举:二级选项依赖一级已选值。
func TestAdvanceDynamicLevel(t *testing.T) {
	levels := func(name string) []func([]string) []sdk.Option {
		if name == "plugins" {
			return []func([]string) []sdk.Option{
				staticOpts(opt("list", "列出"), opt("on", "启用")),
				// 二级:list → 无(list 直接执行);on → 插件列表(动态求值)
				func(picked []string) []sdk.Option {
					if len(picked) >= 2 && picked[1] == "list" {
						return nil
					}
					return []sdk.Option{opt("host-jobs", "后台任务"), opt("host-skills", "技能")}
				},
			}
		}
		return nil
	}
	// 选中 list → 二级 nil → 最后级无选项 → 执行
	in, _, commit := advanceEnter("/", &Pick{Items: []sdk.Option{opt("plugins", "插件")}}, levels)
	// 进入二级选择后:二级枚举器返回 nil?不——list 分支:选中 plugins 时 picked=["plugins"],
	// 二级 = evalLevel(lv,1,["plugins"]) → picked[1] 不存在 → 返回插件列表!
	// 所以先选中 list 才会得到 nil。
	in2, _, commit2 := advanceEnter("/plugins", &Pick{Level: 1, Items: []sdk.Option{opt("list", "列出"), opt("on", "启用")}}, levels)
	if !commit2 {
		t.Fatal("list 无二级应执行")
	}
	if in2 != "/plugins list" {
		t.Fatalf("执行文本: %q", in2)
	}
	_ = in
	_ = commit
}

// TestAdvanceFreeLevelBreak 自由级断点:中间级枚举器返回 nil(自由输入)→ 回输入框不执行。
func TestAdvanceFreeLevelBreak(t *testing.T) {
	// 三级 Args:枚举 a/b → 自由(nil) → 枚举 x/y(断点处停止,不可能到第三级)
	levels := func(name string) []func([]string) []sdk.Option {
		if name == "cmd" {
			return []func([]string) []sdk.Option{
				staticOpts(opt("a", "A"), opt("b", "B")),
				nil, // 自由级
				staticOpts(opt("x", "X")),
			}
		}
		return nil
	}
	// 选中命令 → 一级选项出现
	in, pick, commit := advanceEnter("/", &Pick{Items: []sdk.Option{opt("cmd", "示例")}}, levels)
	if commit || pick == nil || pick.Level != 1 || len(pick.Items) != 2 {
		t.Fatalf("应进入一级选项: commit=%v pick=%+v", commit, pick)
	}
	// 选中 a → 下一级为自由级(nil)→ 断点:回输入框,不执行
	in2, pick2, commit2 := advanceEnter(in, pick, levels)
	if commit2 || pick2 != nil {
		t.Fatalf("自由级应断点回输入框: %q commit=%v pick=%+v", in2, commit2, pick2)
	}
	if in2 != "/cmd a" {
		t.Fatalf("断点文本应保留已选值: %q", in2)
	}
}
