// 交互式选择器推进单测:级联逐级、动态枚举、自由级断点(继续输入提示)、执行时机。
package tui

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func opt(value, desc string) sdk.Option {
	return sdk.Option{Value: value, Desc: desc}
}

// staticLevel 静态枚举级。
func staticLevel(opts ...sdk.Option) sdk.ArgLevel {
	return sdk.ArgLevel{Options: func([]string) []sdk.Option { return opts }}
}

// freeLevel 自由输入级(参数名列表)。
func freeLevel(names ...string) sdk.ArgLevel {
	return sdk.ArgLevel{FreeArgs: func([]string) []string { return names }}
}

// TestAdvancePrefixNotMerged 回归:输入前缀过滤(/s)后选中命令,命令文本不得携带前缀。
func TestAdvancePrefixNotMerged(t *testing.T) {
	levels := func(name string) []sdk.ArgLevel {
		if name == "settings" {
			return []sdk.ArgLevel{staticLevel(opt("history", "历史条数"))}
		}
		return nil
	}
	// 用户输入 /s(前缀过滤),高亮 settings,Enter
	res := AdvanceEnter("/s", &Pick{Level: 0, Items: []sdk.Option{opt("sandbox", "沙箱"), opt("settings", "历史注入")}, Cursor: 1}, levels)
	if res.Input != "/settings" {
		t.Fatalf("命令级选择不得携带过滤前缀,应为 /settings, got %q", res.Input)
	}
	if res.Commit || res.Pick == nil {
		t.Fatalf("应进入 settings 的参数级: commit=%v pick=%v", res.Commit, res.Pick)
	}
	// 参数级:基础文本保留并追加
	res2 := AdvanceEnter(res.Input, res.Pick, levels)
	if !res2.Commit || res2.Input != "/settings history" {
		t.Fatalf("参数级追加应为 /settings history: %q commit=%v", res2.Input, res2.Commit)
	}
}

// TestAdvanceNoArgCommand 无参数级命令:Enter 选中即直接执行(/help、/exit 类)。
func TestAdvanceNoArgCommand(t *testing.T) {
	levels := func(name string) []sdk.ArgLevel { return nil }
	res := AdvanceEnter("/", &Pick{Items: []sdk.Option{opt("help", "帮助")}}, levels)
	if !res.Commit || res.Input != "/help" || res.Pick != nil {
		t.Fatalf("无参数命令应直接执行: %q commit=%v pick=%v", res.Input, res.Commit, res.Pick)
	}
}

// TestAdvanceFreeBreak 自由级断点:命令需手动输入参数(如 /model 模型名)→ 不执行,
// 保留文本回输入框并给出继续输入提示。
func TestAdvanceFreeBreak(t *testing.T) {
	levels := func(name string) []sdk.ArgLevel {
		if name == "model" {
			return []sdk.ArgLevel{freeLevel("模型名")}
		}
		return nil
	}
	res := AdvanceEnter("/", &Pick{Items: []sdk.Option{opt("model", "切模型")}}, levels)
	if res.Commit {
		t.Fatal("需自由输入的命令不应直接执行")
	}
	if res.Pick != nil {
		t.Fatalf("断点应退出选择态: %v", res.Pick)
	}
	if res.Input != "/model " {
		t.Fatalf("断点应保留命令文本并带尾随空格(直接打字即参数): %q", res.Input)
	}
	if len(res.Hints) == 0 || !strings.Contains(res.Hints[0], "模型名") {
		t.Fatalf("断点应有继续输入提示: %v", res.Hints)
	}
}

// TestAdvanceSubFreeBreak 子命令自由级断点:provider set 需 baseUrl/apiKey(自由),
// 而 show/clear 无自由级 → 仍直接执行。
func TestAdvanceSubFreeBreak(t *testing.T) {
	levels := func(name string) []sdk.ArgLevel {
		if name == "provider" {
			return []sdk.ArgLevel{
				staticLevel(opt("show", "查看"), opt("set", "设置"), opt("clear", "清除")),
				{FreeArgs: func(picked []string) []string {
					if len(picked) < 2 || picked[1] != "set" {
						return nil // show/clear 无自由级
					}
					return []string{"baseUrl", "apiKey", "model?"}
				}},
			}
		}
		return nil
	}
	// 选中 show → 直接执行
	res := AdvanceEnter("/", &Pick{Items: []sdk.Option{opt("provider", "提供商")}}, levels)
	if res.Commit || res.Pick == nil {
		t.Fatalf("应进入一级(show/set/clear): %v %v", res.Commit, res.Pick)
	}
	res2 := AdvanceEnter(res.Input, res.Pick, levels) // 高亮 show(第 0 项)
	if !res2.Commit || res2.Input != "/provider show" {
		t.Fatalf("show 无自由级应直接执行: %q commit=%v", res2.Input, res2.Commit)
	}
	// 选中 set → 自由级断点(继续输入 baseUrl/apiKey)
	res2b := AdvanceEnter("/provider", &Pick{Level: 1, Items: res.Pick.Items, Cursor: 1}, levels)
	if res2b.Commit || res2b.Input != "/provider set " {
		t.Fatalf("set 应在断点保留文本(带尾随空格): %q commit=%v", res2b.Input, res2b.Commit)
	}
	if len(res2b.Hints) == 0 || !strings.Contains(strings.Join(res2b.Hints, " "), "baseUrl") {
		t.Fatalf("set 断点应提示 baseUrl 等: %v", res2b.Hints)
	}
}

// TestAdvanceCascade 级联:sandbox → ro|ws|full → 选完执行。
func TestAdvanceCascade(t *testing.T) {
	levels := func(name string) []sdk.ArgLevel {
		if name == "sandbox" {
			return []sdk.ArgLevel{staticLevel(opt("ro", "只读"), opt("ws", "工作区"), opt("full", "全放开"))}
		}
		return nil
	}
	res := AdvanceEnter("/", &Pick{Items: []sdk.Option{opt("sandbox", "沙箱")}}, levels)
	if res.Commit || res.Pick == nil || res.Pick.Level != 1 || len(res.Pick.Items) != 3 {
		t.Fatalf("应进入参数级: %v %v", res.Commit, res.Pick)
	}
	res.Pick.Cursor = 1 // ↑/↓ 移到 ws
	res2 := AdvanceEnter(res.Input, res.Pick, levels)
	if !res2.Commit || res2.Input != "/sandbox ws" {
		t.Fatalf("参数选完应执行: %q commit=%v", res2.Input, res2.Commit)
	}
	fields := strings.Fields(strings.TrimPrefix(res2.Input, "/"))
	if fields[0] != "sandbox" || fields[1] != "ws" {
		t.Fatalf("分发字段不符: %v", fields)
	}
}

// TestAdvanceDynamicLevel 动态枚举:二级选项依赖一级已选值(如 /plugins on 的插件列表)。
func TestAdvanceDynamicLevel(t *testing.T) {
	levels := func(name string) []sdk.ArgLevel {
		if name == "plugins" {
			return []sdk.ArgLevel{
				staticLevel(opt("list", "列出"), opt("on", "启用")),
				{Options: func(picked []string) []sdk.Option {
					if len(picked) >= 2 && picked[1] == "list" {
						return nil // list 无二级
					}
					return []sdk.Option{opt("host-jobs", "后台任务"), opt("host-skills", "技能")}
				}},
			}
		}
		return nil
	}
	// 一级选中 plugins → 二级
	res := AdvanceEnter("/", &Pick{Items: []sdk.Option{opt("plugins", "插件")}}, levels)
	if res.Commit || res.Pick == nil || res.Pick.Level != 1 || len(res.Pick.Items) != 2 {
		t.Fatalf("一级后应进入二级: %v %v", res.Commit, res.Pick)
	}
	// 二级选中 list → 无二级枚举(Cursor=0 为 list)→ 直接执行
	res2 := AdvanceEnter(res.Input, res.Pick, levels)
	if !res2.Commit || res2.Input != "/plugins list" {
		t.Fatalf("list 无二级应执行: %q commit=%v", res2.Input, res2.Commit)
	}
}

// TestAdvanceLevelOptionsWithFreeFallback 同一级同时声明 Options 与 FreeArgs(如某渠道的 login 子命令):
// 命中枚举路径 → 出选项;Options 返回空 → 回退自由输入序列;两者皆空 → 直接执行。
func TestAdvanceLevelOptionsWithFreeFallback(t *testing.T) {
	levels := func(name string) []sdk.ArgLevel {
		if name != "demo" {
			return nil
		}
		return []sdk.ArgLevel{
			staticLevel(opt("status", "状态"), opt("login", "配置"), opt("env", "环境")),
			{
				Options: func(picked []string) []sdk.Option {
					if len(picked) >= 2 && picked[1] == "env" {
						return []sdk.Option{opt("official", "正式"), opt("sandbox", "沙箱")}
					}
					return nil
				},
				FreeArgs: func(picked []string) []string {
					if len(picked) >= 2 && picked[1] == "login" {
						return []string{"AppID", "AppSecret"}
					}
					return nil
				},
			},
		}
	}
	res := AdvanceEnter("/", &Pick{Items: []sdk.Option{opt("demo", "示例渠道")}}, levels)
	if res.Pick == nil || res.Pick.Level != 1 || len(res.Pick.Items) != 3 {
		t.Fatalf("应进入 L2 子命令枚举: %+v", res.Pick)
	}
	// env → L3 枚举
	res.Pick.Cursor = 2
	res2 := AdvanceEnter(res.Input, res.Pick, levels)
	if res2.Pick == nil || len(res2.Pick.Items) != 2 || res2.Pick.Items[1].Value != "sandbox" {
		t.Fatalf("env 应逐级出 official/sandbox: %+v", res2.Pick)
	}
	res2.Pick.Cursor = 1
	res3 := AdvanceEnter(res2.Input, res2.Pick, levels)
	if !res3.Commit || res3.Input != "/demo env sandbox" {
		t.Fatalf("env 选完应执行: %q commit=%v", res3.Input, res3.Commit)
	}
	// login → Options 空 → 回退自由序列
	res.Pick.Cursor = 1
	res4 := AdvanceEnter(res.Input, res.Pick, levels)
	if res4.Commit || len(res4.Free) != 2 || res4.Free[0] != "AppID" {
		t.Fatalf("login 应回退自由序列: %+v", res4)
	}
	// status → 两者皆空 → 直接执行
	res.Pick.Cursor = 0
	res5 := AdvanceEnter(res.Input, res.Pick, levels)
	if !res5.Commit || res5.Input != "/demo status" {
		t.Fatalf("status 应直接执行: %q commit=%v", res5.Input, res5.Commit)
	}
}
