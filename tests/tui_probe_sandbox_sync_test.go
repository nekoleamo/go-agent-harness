// /sandbox sync 的真机探针(pty 级):不只测函数,而是**真终端**里敲命令 →
// 偏好落盘 → 重启后状态栏按偏好显示。
//
// 为什么值得一条 pty 探针:R10 ②-2 的关键风险不是"函数算错",而是
// 「用户敲了开关、界面/拦截却没跟着变」——那类失效只有端到端跑一次才看得见。
package tests

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// stripPTYEscape 去掉 pty 输出里的 CSI/OSC 序列(只留可见文字,便于断言)。
func stripPTYEscape(s string) string {
	s = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`\x1b[=>]`).ReplaceAllString(s, "")
	return s
}

// bootAndExit 起一次 TUI、等首帧状态栏、双击 Ctrl+C 退出(退出失败即失败)。
func bootAndExit(t *testing.T, bin string, env []string) string {
	t.Helper()
	ptmx, cmd, out := runTUIViaPty(t, bin, env)
	defer ptmx.Close()
	boot, ok := drainUntil(out, 6*time.Second, "工作区: ")
	if !ok && len(boot) > 0 {
		more, ok2 := drainUntil(out, 6*time.Second, "工作区: ")
		boot += more
		ok = ok2
	}
	if !ok {
		t.Fatalf("未见 TUI 首帧状态栏;尾段 %q", firstN(tailS(boot, 600), 600))
	}
	ptmx.Write([]byte{0x03})
	drain(out, 300*time.Millisecond)
	ptmx.Write([]byte{0x03})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("退出卡死")
	}
	return boot
}

// patchApprovalOpen 把探针数据根里的 policy-guard 审批档改成 open(模拟"审批开放"用户)。
func patchApprovalOpen(t *testing.T, dataRoot string) {
	t.Helper()
	p := filepath.Join(dataRoot, "config", "bundle-base.yaml")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", p, err)
	}
	s := string(b)
	if !strings.Contains(s, "approval: smart") {
		t.Fatalf("未找到 approval: smart(seed 结构已变,探针需同步):\n%s", firstN(s, 400))
	}
	s = strings.Replace(s, "approval: smart", "approval: open", 1)
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readSyncPref 读数据根偏好文件里的 sandbox_sync(缺失返回 nil)。
func readSyncPref(t *testing.T, dataRoot string) *bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dataRoot, "config", "gah-state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var p struct {
		SandboxSync *bool `json:"sandbox_sync"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("偏好文件不是合法 JSON(%s): %v", b, err)
	}
	return p.SandboxSync
}

// TestTUIProbeSandboxSync 真终端里切开关:命令生效 → 落偏好 → 重启状态栏跟着变。
func TestTUIProbeSandboxSync(t *testing.T) {
	bin := buildGahCurrent(t)
	dataRoot := probeDataDir(t, bin)
	env := probeEnv()

	// 1) 首次启动:生成 seed 配置(自动新建数据根),随后把审批档改成 open
	boot := bootAndExit(t, bin, env)
	if strings.Contains(boot, "panic:") {
		t.Fatalf("启动 panic:%q", tailS(boot, 600))
	}
	patchApprovalOpen(t, dataRoot)

	// 2) 审批 open + 联动默认开 → 状态栏应显示**有效档完全**(带联动来源)
	ptmx, cmd, out := runTUIViaPty(t, bin, env)
	defer ptmx.Close()
	first, ok := drainUntil(out, 6*time.Second, "审批联动")
	if !ok {
		first += drain(out, 3*time.Second)
	}
	plain := stripPTYEscape(first)
	t.Logf("首屏(去转义):%q", firstN(tailS(plain, 400), 400))
	if !strings.Contains(plain, "审批联动") {
		t.Fatalf("审批 open + 联动开时状态栏应标注联动来源(审批联动);实得 %q", firstN(tailS(plain, 400), 400))
	}

	// 3) 真终端里切开关:TUI 本地命令 → 偏好落盘
	io.WriteString(ptmx, "/sandbox sync off\r")
	offOut, _ := drainUntil(out, 6*time.Second, "独立生效")
	plainOff := stripPTYEscape(offOut)
	t.Logf("切换回显(去转义)尾部:%q", firstN(tailS(plainOff, 300), 300))
	want := readSyncPref(t, dataRoot)
	if want == nil || *want {
		t.Fatalf("/sandbox sync off 后偏好应为 false,got %v(说明命令没走到持久化)", want)
	}
	if !strings.Contains(plainOff, "沙箱联动 -> off") && !strings.Contains(plainOff, "独立生效") {
		t.Fatalf("切换应有可见回显;实得 %q", firstN(tailS(plainOff, 300), 300))
	}
	// 状态栏也该立刻改口径:不再宣称"审批联动"
	if strings.Contains(plainOff, "审批联动") {
		t.Fatalf("关掉联动后状态栏不应再写「随审批」;实得 %q", firstN(tailS(plainOff, 300), 300))
	}

	// 退出,验证重启后按偏好恢复(配置里 sync 仍是 true —— 偏好必须压过它)
	ptmx.Write([]byte{0x03})
	drain(out, 300*time.Millisecond)
	ptmx.Write([]byte{0x03})
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("退出卡死")
	}

	reboot := bootAndExit(t, bin, env)
	plainReboot := stripPTYEscape(reboot)
	if strings.Contains(plainReboot, "审批联动") {
		t.Fatalf("重启后应沿用偏好(联动 off),状态栏仍写「审批联动」:%q", firstN(tailS(plainReboot, 400), 400))
	}
	if got := readSyncPref(t, dataRoot); got == nil || *got {
		t.Fatalf("重启不该改写偏好,got %v", got)
	}
}
