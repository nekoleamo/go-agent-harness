// A-1 条目 16(S-P0-2 异步提问 + 问题栈)的 pty 真机验收:
// 一轮里模型**并行**提两个问题 → 状态栏「待答 2」、两问都在会话流;答第一问后自动切第二问;
// 全部答完「待答」段消失(以 `/answer` 无参的「当前没有待答提问」为判据 —— 消失类判定不走累计原始流)。
package tests

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTUIAcceptTwoQuestionsStack(t *testing.T) {
	bin, env, _, _ := tuiAcceptSetup(t)
	root := probeDataDir(t, bin)
	cfg := filepath.Join(root, "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAcceptFile(t, cfg, "patch-accq.yaml", `entries:
  - id: llm-openai-compat
    enabled: false
  - id: llm-mock
    enabled: true
    data:
      script: '[{"tools":[{"name":"ask_user_question","args":"{\"prompt\":\"聚合第一问?\",\"options\":[{\"value\":\"甲\",\"desc\":\"选项甲\"}]}"},{"name":"ask_user_question","args":"{\"prompt\":\"聚合第二问?\",\"options\":[{\"value\":\"乙\",\"desc\":\"选项乙\"}]}"}]},{"text":"两问都已作答。"}]'
`)
	writeAcceptFile(t, cfg, "profile-accq.yaml",
		"name: accq\nbundles:\n  - base\n  - tui\n  - confirm-fusion\npatches:\n  - patch-accq.yaml\n")

	s := newTuiSess(t, bin, env, "--profile", "accq")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("两个问题\r")
	// 状态栏这类"当前态"读**屏幕**(差分重绘下 raw 里 `待答 2` 会被提问块插写拆断:
	// 只见 `待答(` + `2(Esc 退出作答)`,实测约半数假阴);「出现过」类才用 raw。
	if !s.waitScreen("待答 2", 45*time.Second) {
		t.Fatalf("条目 16:状态栏未出现「待答 2」(两问未共存);屏=%q", stripANSI(s.screen()))
	}
	for _, q := range []string{"聚合第一问", "聚合第二问"} {
		if !s.waitRaw(q, 10*time.Second) {
			t.Errorf("条目 16:提问 %q 未进入会话流", q)
		}
	}

	// 逐问作答(栈序:栈首 = 最早到达)。两问都答完 → 回合收尾,证明第一问作答后第二问被接上。
	for i := 0; i < 2; i++ {
		s.send("/answer 1\r")
		time.Sleep(1200 * time.Millisecond)
	}
	if !s.waitRaw("两问都已作答", 60*time.Second) {
		t.Fatalf("条目 16:两问答完后回合未收尾(第二问未被接上);尾段 %q", stripANSI(tailS(s.rawText(), 500)))
	}
	if !s.waitIdle(30 * time.Second) {
		t.Errorf("条目 16:作答后回合未回到空闲")
	}
	// 待答段消失的**实时**判据:命令面读数(状态栏旧帧会留残影,不能拿屏幕 Contains 判消失)
	s.send("/answer\r")
	if !s.waitRaw("当前没有待答提问", 15*time.Second) {
		t.Errorf("条目 16:答完后待答栈未清空")
	}
}
