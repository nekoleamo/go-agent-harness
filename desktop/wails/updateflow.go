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
	// 进行中态:文字 + 置灰。恢复一律放在 defer,避免任何一条 return 把它留在「检查中」。
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
		s.notifyDialog("检查更新失败", err.Error())
		return
	}
	if rel == nil {
		shellLogf("托盘: 已是最新")
		s.notifyDialog("已是最新", "当前版本 "+version+" 已经是最新的。")
		return
	}
	shellLogf("托盘: 发现新版本 %s", rel.Version)

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
	d.AddButton("下载并安装").SetAsDefault().OnClick(func() { go s.installUpdateFromTray() })
	d.Show()
}

// installUpdateFromTray 下载+安装(失败如实弹窗,成功提示重启)。
func (s *shell) installUpdateFromTray() {
	if s.updater == nil {
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
		s.notifyDialog("安装失败", err.Error())
		return
	}
	shellLogf("托盘: 更新已就绪,等待重启")
	d := s.app.Dialog.Question().SetTitle("更新已就绪").SetMessage("新版本已下载完成。现在重启 gah 应用更新吗?")
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
