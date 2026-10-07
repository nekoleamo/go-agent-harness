package hosttools

// 按会话的工具可见性过滤(第一百一十六批)。
//
// 要钉的是**两处**:模型看到的清单(ListFor)与执行闸门(Execute) —— 只过 List 不够,
// 模型会把历史上下文里出现过的工具名再叫一次(既有纪律);两处都过才有意义。

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

type fakeTool struct{ name string }

func (f fakeTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: f.name, Description: f.name}
}
func (f fakeTool) Execute(context.Context, string) (any, error) {
	return map[string]any{"ok": true}, nil
}

func names(defs []sdk.ToolDefinition) map[string]bool {
	out := map[string]bool{}
	for _, d := range defs {
		out[d.Name] = true
	}
	return out
}

func newRegistry(t *testing.T, toolNames ...string) *reg {
	t.Helper()
	r := &reg{}
	r.tools = map[string]sdk.Tool{}
	r.order = nil
	for _, n := range toolNames {
		r.tools[n] = fakeTool{name: n}
		r.order = append(r.order, n)
	}
	return r
}

func TestListForPerSession(t *testing.T) {
	r := newRegistry(t, "alpha", "beta")
	d := r.SetContextFilter(func(ctx context.Context, def sdk.ToolDefinition) bool {
		return sdk.SessionFromContext(ctx) != "ro" || def.Name != "beta"
	})
	defer d()

	all := names(r.ListFor(context.Background()))
	if len(all) != 2 {
		t.Fatalf("没带会话时该看到全部,得 %v", all)
	}
	ro := names(r.ListFor(sdk.WithSessionContext(context.Background(), "ro")))
	if ro["beta"] {
		t.Fatalf("会话 ro 的过滤应排除 beta,得 %v", ro)
	}
	if !ro["alpha"] {
		t.Fatalf("会话 ro 的其它工具不该被误伤:%v", ro)
	}
	rw := names(r.ListFor(sdk.WithSessionContext(context.Background(), "rw")))
	if len(rw) != 2 {
		t.Fatalf("会话 rw 没设限制,该看到全部:%v", rw)
	}
}

func TestExecuteRejectsSessionFilteredTool(t *testing.T) {
	r := newRegistry(t, "alpha", "beta")
	d := r.SetContextFilter(func(ctx context.Context, def sdk.ToolDefinition) bool {
		return sdk.SessionFromContext(ctx) != "ro" || def.Name != "beta"
	})
	defer d()

	// 会话 ro 调 beta → 被拒,且要说清是"本会话未授权"而不是"工具不存在"
	res, err := r.Execute(sdk.WithSessionContext(context.Background(), "ro"), "beta", "{}")
	if err != nil {
		t.Fatalf("被排除的工具应返回结果里的错误,不是调用错误: %v", err)
	}
	if res.Error == "" {
		t.Fatal("本会话未授权的工具必须被拒")
	}
	// 别的会话:过滤不拦(往下走执行路径 —— 裸注册表没有总线/沙箱,到那一步会走
	// "无沙箱"分支,这里只验证**没有在过滤这一步被拒**)。
	if res2, err := r.Execute(sdk.WithSessionContext(context.Background(), "rw"), "beta", "{}"); err != nil {
		t.Fatalf("别的会话不该在过滤这一步出错: %v", err)
	} else if strings.Contains(res2.Error, "本会话") {
		t.Fatalf("别的会话不该被判未授权:%+v", res2)
	}
}
