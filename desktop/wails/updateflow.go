// updateflow:托盘「检查更新…」的真实流程(旧壳 checkForUpdates 的等价移植)。
//
// 与旧壳一致的三条口径:
//  1. **只查不装**:查完把结论摆给人看,点了「安装」才下载(一次检查不该顺手换掉整个应用);
//  2. 检查期间菜单文字切成「检查更新中…」并置灰 —— 网络不通时几十秒没动静,
//     「点了没反应」是旧壳连续两轮的真机反馈;
//  3. 便携版**只查不装**(Windows 不允许覆盖运行中的程序),结论里直接给下载指引。
package main

import (
	"context"
	"os"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	checkIdleText = "检查更新…"
	checkBusyText = "检查更新中…"
)

// checkUpdateFromTray 托盘入口:查一次,把结论摆出来(有新版则问要不要装)。
func (s *shell) checkUpdateFromTray(item *application.MenuItem) {
	if s.updater == nil {
		s.notifyDialog("检查更新不可用", "更新器未装配(壳日志里有原因)。")
		return
	}
	// 进行中态:文字 + 置灰(以及 update-state 的 busy/seq)。恢复一律放在 defer,
	// 避免任何一条 return 把它留在「检查中」。
	seq := s.updateSeq.Add(1)
	s.updateBusy.Store(true)
	defer s.updateBusy.Store(false)
	_ = seq
	setMenuItemLabel(item, checkBusyText)
	setMenuItemEnabled(item, false)
	defer func() {
		setMenuItemLabel(item, checkIdleText)
		setMenuItemEnabled(item, true)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	shellLogf("托盘: 检查更新(当前 %s)", version)
	rel, err := s.app.Updater.Check(ctx)
	if err != nil {
		shellLogf("托盘: 检查更新失败: %v", err)
		s.notifyDialog("检查更新失败", explainUpdateError(err))
		return
	}
	if rel == nil {
		shellLogf("托盘: 已是最新")
		s.notifyDialog("已是最新", "当前版本 "+version+" 已经是最新的。")
		return
	}
	shellLogf("托盘: 发现新版本 %s", rel.Version)
	// 记下来:用户点「下载并安装」时要用它做一次版本对账。
	s.noteSeenVersion(rel.Version)
	expect := rel.Version

	msg := "发现新版本 " + rel.Version
	if rel.Notes != "" {
		msg += "\n\n" + rel.Notes
	}
	if s.portable {
		msg += "\n\n便携版不自动更新:请到项目 Release 页下载新包覆盖本目录。"
		s.notifyDialog("发现新版本 "+rel.Version, msg)
		return
	}
	msg += "\n\n现在下载并安装吗?安装完成后需要重启 gah。"
	d := s.app.Dialog.Question().SetTitle("发现新版本 " + rel.Version).SetMessage(msg)
	if s.window != nil {
		d.AttachToWindow(s.window)
	}
	d.AddButton("稍后").SetAsCancel()
	// 回调里**不要**做阻塞工作:平台实现可能就在主线程/模态循环里调它
	// (darwin 是 dialogRunModal 的完成回调),下载几十 MB 会把界面冻住。
	d.AddButton("下载并安装").SetAsDefault().OnClick(func() { go s.installUpdateFromTray(expect) })
	d.Show()
}

// installUpdateFromTray 下载+安装(失败如实弹窗,成功提示重启)。
//
// expect = 用户在弹窗里看到的那个版本。下载前先对一次账:检查与点安装之间镜像可能
// 把清单换掉(2026-11-03 真机反馈「显示 0.8.4、装上 0.8.5」),那种情况**不能**静默换成
// 新版本装进去 —— 停下来让用户重新确认。
func (s *shell) installUpdateFromTray(expect string) {
	if s.updater == nil {
		return
	}
	// 准入判定与 /shell/update-install 共用一份(installGate):便携拒绝**先于**版本对账 ——
	// 原先托盘这条路没有便携判定(文件头注释却写着「便携版只查不装」)⇒ 便携版从托盘
	// 点「下载并安装」会真的去覆盖程序目录。
	seen := s.seenVersion()
	// writable 与 control.go 同口径:只有便携模式才做写探测(它会真写临时文件)。
	writable := true
	if s.portable {
		writable = portableWritable()
	}
	action, msg := installGate(expect, seen, s.portable, writable,
		os.Getenv("GAH_DESKTOP_PORTABLE_UPDATE") == "0")
	switch action {
	case installPortableOnly:
		shellLogf("便携版: 拒绝自动安装,提示用户下载新包")
		s.notifyDialog("便携版不自动更新", msg)
		return
	case installVersionChanged:
		shellLogf("托盘: 版本已漂移(确认的是 %s,现在是 %s),不安装", expect, seen)
		s.notifyDialog("版本已变化",
			"你确认的是 "+expect+",而更新源现在是 "+seen+"。\n\n请重新点一次「检查更新…」再确认安装。")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	// 升级前备份:整包替换一旦把数据读坏,用户没有第二份。
	if path, err := backupBeforeUpgrade(s.dataRoot); err != nil {
		shellLogf("升级前备份失败(中止升级): %v", err)
		s.notifyDialog("升级已中止", "升级前备份失败:"+err.Error())
		return
	} else if path != "" {
		shellLogf("升级前备份: %s", path)
	}
	shellLogf("托盘: 开始下载安装更新")
	if err := s.app.Updater.DownloadAndInstall(ctx); err != nil {
		shellLogf("托盘: 安装更新失败: %v", err)
		s.notifyDialog("安装失败", explainUpdateError(err))
		return
	}
	shellLogf("更新已就绪,等待重启(版本 %s)", s.seenVersion())
	d := s.app.Dialog.Question().SetTitle("更新已就绪").SetMessage("新版本 " + s.seenVersion() + " 已下载完成。现在重启 gah 应用更新吗?")
	if s.window != nil {
		d.AttachToWindow(s.window)
	}
	d.AddButton("稍后").SetAsCancel()
	d.AddButton("立即重启").SetAsDefault().OnClick(func() {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := s.app.Updater.Restart(context.Background()); err != nil {
				shellLogf("重启应用失败: %v", err)
			}
		}()
	})
	d.Show()
}
