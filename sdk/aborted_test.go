// sdk/aborted.go 单测:中止哨兵在包装链上可判别,且不与普通错误混淆。
package sdk

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsAbortedThroughWrapping(t *testing.T) {
	base := AbortedError("approval: 危险命令未执行(等待确认时被停止)")
	if !IsAborted(base) {
		t.Fatalf("自身应可判别: %v", base)
	}
	wrapped := fmt.Errorf("外层: %w", base)
	if !IsAborted(wrapped) {
		t.Fatalf("包装后仍应可判别(errors.Is 走错误链): %v", wrapped)
	}
	if IsAborted(errors.New("沙箱拒绝写 workspace 外路径")) {
		t.Fatal("普通错误不该被判成中止")
	}
	if IsAborted(nil) {
		t.Fatal("nil 不是中止")
	}
}

// TestAbortedErrorKeepsCauseText 中止文案必须自带「被停止」的字样:
// 它会被直接渲染给用户与模型,不含语境的 "回合已中止" 读不出是用户自己的动作。
func TestAbortedErrorKeepsCauseText(t *testing.T) {
	err := AbortedError("策略 guard: web_fetch 未执行(等待域名确认时被停止)")
	if !errors.Is(err, ErrAborted) {
		t.Fatal("应挂上哨兵")
	}
	if got := err.Error(); !contains(got, "被停止") {
		t.Fatalf("文案缺原因: %s", got)
	}
	_ = context.Canceled // 中止与 context.Canceled 是两回事(见文件注释)
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
