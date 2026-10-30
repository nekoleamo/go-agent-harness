// health:sidecar 失联检测 + 服务重启(旧 Rust 壳 sidecarLost / restart_service 的等价)。
//
// 为什么要专门一条:sidecar 崩了之后窗口不会自己消失 —— 页面还在,SSE 断了,
// 用户看到的是「界面在,但发出去没反应」。旧壳真机上就卡在这:没有任何提示,
// 只能靠人猜到托盘里点「重新启动服务」。
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// sidecarLostStreak 连续多少次 probe 不可达才判「服务已退出」。
// 设 3(约 15s):单次失败可能只是机器卡一下,报错了反而让人以为服务挂了。
const sidecarLostStreak = 3

// lostTracker 失联判定(纯状态机,便于单测):
// 连续 N 次不可达才算失联,且**只报一次**;恢复后重新武装。
type lostTracker struct {
	streak   int
	reported bool
}

// observe 喂一次探测结果。notify=true 表示"这一次该提示用户";recovered=true 表示刚从失联恢复。
func (t *lostTracker) observe(alive bool) (notify, recovered bool) {
	if alive {
		t.streak = 0
		if t.reported {
			t.reported = false
			return false, true
		}
		return false, false
	}
	t.streak++
	if t.streak < sidecarLostStreak || t.reported {
		return false, false
	}
	t.reported = true
	return true, false
}

// startHealthWatch 后台探测 sidecar 存活。
func (s *shell) startHealthWatch() {
	go func() {
		var t lostTracker
		for {
			time.Sleep(5 * time.Second)
			if !s.ready.Load() {
				continue // 还没就绪:启动链自己会报错,别在这里叠一条
			}
			alive := stateAlive(s.addr, s.gahToken, 2*time.Second)
			notify, recovered := t.observe(alive)
			if recovered {
				shellLogf("后台服务已恢复")
				setShellNotices(nil)
				continue
			}
			if !notify {
				continue
			}
			shellLogf("后台服务失联:%d 次探测不可达", t.streak)
			msg := "后台服务已退出:界面不再接收新内容。可从托盘菜单选择「重新启动服务」恢复(进行中的回合会丢失)。"
			s.announce("gah 后台服务已退出", msg)
			setShellNotices([]string{msg})
		}
	}()
}

// restartSidecar 重启后台服务(不动壳进程、不动窗口)。
//
// 为什么只重启 sidecar 而不是整个壳:壳里没有需要重置的状态,窗口/托盘/控制面全都健在;
// 整套重启要多处理单实例锁的交接,而这条路径的价值恰恰是「别把事情搞复杂」。
// 端口沿用同一个:旧进程已死,重绑通常立刻成功;连不上时用户会看到明确错误。
func (s *shell) restartSidecar() error {
	s.mu.Lock()
	old := s.sc
	s.sc = nil
	s.mu.Unlock()
	s.ready.Store(false)

	if old != nil {
		old.stop(s.addr, s.gahToken)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		sc, err := startSidecar(s.plan.bin, s.addr, s.gahToken, "127.0.0.1:"+s.ctl.port(), s.ctl.token)
		if err != nil {
			shellLogf("重启服务: 第 %d 次启动失败: %v", attempt, err)
			time.Sleep(400 * time.Millisecond)
			continue
		}
		s.mu.Lock()
		s.sc = sc
		s.mu.Unlock()
		go func() { _ = sc.wait() }()
		if !waitReady(s.addr, s.gahToken, 15*time.Second) {
			shellLogf("重启服务: 第 %d 次启动后 15s 未就绪", attempt)
			sc.stop(s.addr, s.gahToken)
			s.mu.Lock()
			s.sc = nil
			s.mu.Unlock()
			continue
		}
		s.ready.Store(true)
		setShellNotices(nil) // 清掉「已退出」那条横带
		s.window.SetURL(s.shellURL())
		shellLogf("重启服务: 成功(attempt=%d)", attempt)
		return nil
	}
	return fmt.Errorf("连续 3 次都没能把后台服务拉起来;请退出后重新打开 gah")
}

// spawnNewInstance 另起一个壳进程(--new-instance ⇒ 不注册单实例)。
//
// 为什么不做成「再开一个窗口」:新窗口共用同一个 sidecar(同端口/同偏好),而用户点
// 「新实例」要的是**另一个进程** —— 各自独立的 sidecar 与端口,互不干扰。
// 失败必须给原生对话框:静默失败会让人以为菜单坏了。
func (s *shell) spawnNewInstance() {
	exe, err := os.Executable()
	if err != nil {
		s.notifyDialog("无法启动新实例", "取程序路径失败:"+err.Error())
		return
	}
	cmd := exec.Command(exe, "--new-instance")
	detach(cmd)
	if err := cmd.Start(); err != nil {
		shellLogf("新实例: 启动失败 %v", err)
		s.notifyDialog("无法启动新实例", err.Error())
		return
	}
	shellLogf("新实例: 已启动 pid=%d(%s)", cmd.Process.Pid, exe)
}

// sidecarVersion 读一次 sidecar 自报的版本(`/api/state.version`)。
//
// 为什么要它:壳与内嵌的 gah 是**两个二进制**,各有自己的 `-X main.version`。
// 2026-10-30 v0.8.0 就只给壳注了版本号 ⇒ 托盘「关于 gah」说 0.8.0、Web 底栏说
// **vdev**,用户报的是「版本号不对」。构建期已有断言(publish/gen 脚本扫版本串),
// 这里补的是**运行期看得见**:真出现不一致时,壳日志与「关于」里都要写明是哪个不一致。
func (s *shell) sidecarVersion() string {
	body, ok := s.getState()
	if !ok {
		return ""
	}
	var st struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &st); err != nil {
		return ""
	}
	return st.Version
}

// logSidecarVersion 启动时把两个版本记进壳日志(不一致就 WARN,不静默)。
func (s *shell) logSidecarVersion() {
	v := s.sidecarVersion()
	if v == "" {
		shellLogf("版本: 壳=%s sidecar=未知(/api/state 没给出 version)", version)
		return
	}
	if strings.TrimPrefix(v, "v") != strings.TrimPrefix(version, "v") {
		shellLogf("版本不一致: 壳=%s sidecar=%s —— 内嵌 sidecar 可能漏了 -X main.version(Web 底栏显示的就是 sidecar 的那份)", version, v)
		return
	}
	shellLogf("版本: %s(壳与 sidecar 一致)", v)
}

// showAbout 托盘「关于 gah」:版本 / Web 地址 / 数据根 / 自启状态 / 日志路径。
// 窗口缩在托盘里时,这是用户最先够到的地方(旧壳同一条文案口径)。
func (s *shell) showAbout() {
	autostart := "未启用"
	if s.autostartEnabled() {
		autostart = "已启用"
	}
	// 两个版本都摊出来:只看壳的版本会让「sidecar 报 dev」这种事一直藏在 Web 底栏里。
	text := fmt.Sprintf("gah 桌面版 %s\n\n后台服务:%s\nWeb 地址:http://%s\n数据根:%s\n开机自启:%s\n日志:%s\n便携模式:%v",
		version, sidecarVersionText(s.sidecarVersion(), version), s.addr, s.dataRoot, autostart, shellLogPath(), s.portable)
	s.notifyDialog("关于 gah", text)
}

// sidecarVersionText 「关于」里的那一行;不一致时把偏差写出来(而不是只给一个数字)。
func sidecarVersionText(sc, shell string) string {
	if sc == "" {
		return "gah(版本未取到)"
	}
	if strings.TrimPrefix(sc, "v") != strings.TrimPrefix(shell, "v") {
		return fmt.Sprintf("gah %s(与壳 %s 不一致;这个包的内嵌 sidecar 没注入版本号)", sc, shell)
	}
	return "gah " + sc
}
