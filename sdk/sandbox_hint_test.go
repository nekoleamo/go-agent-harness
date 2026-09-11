package sdk

import (
	"context"
	"testing"
)

// 未注入 hint:ok=false(工具不得假定档位 —— 旧宿主/直连测试路径)。
func TestSandboxHintAbsent(t *testing.T) {
	if _, ok := SandboxHintOf(context.Background()); ok {
		t.Fatal("空 ctx 不应有 hint")
	}
	// nil ctx 走防御分支(经类型化变量传入:字面 nil 会被 staticcheck SA1012 拦下,而此处正是要验防御路径)
	var nilCtx context.Context
	if _, ok := SandboxHintOf(nilCtx); ok {
		t.Fatal("nil ctx 不应 panic/不应有 hint")
	}
}

// 注入后可读回原值,且不污染父 ctx。
func TestSandboxHintRoundTrip(t *testing.T) {
	parent := context.Background()
	h := SandboxHint{Mode: SandboxWorkspace, Root: "/tmp/ws"}
	child := WithSandboxHint(parent, h)

	got, ok := SandboxHintOf(child)
	if !ok || got != h {
		t.Fatalf("读回不符:ok=%v got=%+v", ok, got)
	}
	if _, ok := SandboxHintOf(parent); ok {
		t.Fatal("父 ctx 不应被污染")
	}
}

// 有效档位(联动后)必须原样传递,不被归一化/截断。
func TestSandboxHintKeepsEffectiveMode(t *testing.T) {
	for _, m := range []SandboxMode{SandboxReadOnly, SandboxWorkspace, SandboxFullAccess} {
		got, ok := SandboxHintOf(WithSandboxHint(context.Background(), SandboxHint{Mode: m}))
		if !ok || got.Mode != m {
			t.Fatalf("档位丢失:%q → %+v(ok=%v)", m, got, ok)
		}
	}
}
