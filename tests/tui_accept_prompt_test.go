// A-1 条目 19(AGENTS.override.md 替换同级 AGENTS.md)的 pty 真机验收。
//
// 手法:spy provider 抓请求体 —— "替换"的硬证据是**模型看到的 system 段**里出现 override
// 内容、且**不含**被覆盖的 AGENTS.md 内容(TUI 没有 dump system prompt 的口子)。
package tests

import (
	"strings"
	"testing"
	"time"
)

func TestTUIAcceptAgentsOverride(t *testing.T) {
	base, spy := newSpyProvider(t, "覆盖验证答复。", 0)
	bin, env, _ := tuiAcceptSetupSlow(t, base)
	dir := t.TempDir()
	writeAcceptFile(t, dir, "AGENTS.md", "项目指令甲:本段应被同级 override 替换掉。\n")
	writeAcceptFile(t, dir, "AGENTS.override.md", "覆盖指令乙:本段应替换同级 AGENTS.md。\n")

	s := newTuiSessIn(t, bin, env, dir, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("问一句\r")
	if !s.waitScreen("覆盖验证答复", 60*time.Second) {
		t.Fatalf("条目 19:回合未完成;屏尾 %q", firstN(tailS(s.screen(), 300), 300))
	}
	bodies := spy.snapshot()
	if len(bodies) == 0 {
		t.Fatal("条目 19:未捕获到模型请求")
	}
	last := bodies[len(bodies)-1]
	hasOverride := strings.Contains(last, "覆盖指令乙")
	hasBase := strings.Contains(last, "项目指令甲")
	t.Logf("条目 19:system 段含 override=%v 含被覆盖内容=%v", hasOverride, hasBase)
	if !hasOverride {
		t.Errorf("条目 19:override 内容未进入 system 段;体尾 %q", firstN(tailS(last, 400), 400))
	}
	if hasBase {
		t.Errorf("条目 19:被覆盖的 AGENTS.md 内容仍进了 system 段(override 未生效)")
	}
}
