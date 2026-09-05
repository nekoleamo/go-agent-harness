// P4-8 手动压缩(/compact,CompactService)单测:用真实 token-compress 引擎验证
// Log.Compact 立即折叠超预算历史、短会话无可折叠(folded=0)、未启用压缩的错误路径。
package sessionlog

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
	tokencompress "github.com/nekoleamo/go-agent-harness/plugins/host/token-compress"
)

func TestCompactFoldsHistory(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 6; i++ {
		appendTurn(l, i) // 每轮 user+assistant+tool ~110 字 × 6 → 超 200 预算
	}
	l.RegisterCompressor(200, &tokencompress.Engine{})

	// 手动 Compact:立即折叠(不等投影)并回读累计摘要
	summary, folded, err := l.Compact("重点关注配置")
	if err != nil {
		t.Fatalf("Compact 应成功: %v", err)
	}
	if folded <= 0 {
		t.Fatal("长会话 Compact 应折叠 >0 事件跨度")
	}
	if !strings.Contains(summary, "- 用户") {
		t.Fatalf("应回读抽取式累计摘要: %q", summary)
	}
	// 折叠后投影稳定:摘要置顶 + 水位外最近块;再次 Compact 幂等不报错
	if msgs := l.DeriveMessages(); len(msgs) == 0 || msgs[0].Role != sdk.RoleSystem {
		t.Fatal("折叠后投影摘要应置顶")
	}
	if _, _, err := l.Compact(""); err != nil {
		t.Fatalf("二次 Compact 不应报错: %v", err)
	}
}

func TestCompactShortSessionNoFold(t *testing.T) {
	l := newLog("")
	appendTurn(l, 1)
	l.RegisterCompressor(1000000, &tokencompress.Engine{}) // 预算远超内容
	_, folded, err := l.Compact("")
	if err != nil {
		t.Fatalf("短会话不应报错: %v", err)
	}
	if folded != 0 {
		t.Fatalf("预算内短会话应无可折叠: folded=%d", folded)
	}
}

func TestCompactUnavailable(t *testing.T) {
	l := newLog("")
	if _, _, err := l.Compact(""); err == nil || !strings.Contains(err.Error(), "压缩器未注册") {
		t.Fatalf("未注册压缩器应报错: %v", err)
	}
	l.RegisterCompressor(0, &tokencompress.Engine{}) // 预算关闭(token_budget_chars=0)
	if _, _, err := l.Compact(""); err == nil || !strings.Contains(err.Error(), "预算关闭") {
		t.Fatalf("预算关闭应报错: %v", err)
	}
}

// TestCompactServiceAssertion Log 应经 ctx.sessions 类型断言暴露 CompactService(TUI /compact 依赖)。
func TestCompactServiceAssertion(t *testing.T) {
	l := newLog("")
	var svc sdk.SessionLog = l
	if _, ok := svc.(sdk.CompactService); !ok {
		t.Fatal("Log 应实现 sdk.CompactService")
	}
}
