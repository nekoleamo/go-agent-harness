; 桌面快捷方式补齐(tauri v2 NSIS installerHooks;2026-09-14)
;
; 背景:tauri v2 自带 installer.nsi 只在「完成页复选框被勾选」时创建桌面图标
; (MUI_FINISHPAGE_SHOWREADME_FUNCTION = CreateOrUpdateDesktopShortcut),而且
; CreateOrUpdateDesktopShortcut 内部在 $UpdateMode=1(覆盖安装/升级安装)或
; $NoShortcutMode=1 时**直接 return** —— 于是「先装旧版、再装新版」的机器桌面上
; 没有图标(开始菜单快捷方式不受该守卫影响,所以只有桌面缺)。
;
; 修法:安装末尾无条件补建一次(与开始菜单同目标 = 主程序 ${MAINBINARYNAME}.exe,
; 不是 sidecar)，并复用模板的 AppUserModelID 宏让任务栏分组/固定行为一致。
;
; 说明:
; - hooks 文件在 installer.nsi 第 35 行 !include(早于 PRODUCTNAME/MAINBINARYNAME 的
;   !define),但 ${...} 在宏**被插入**的位置(安装 section 内)展开,定义此时均已存在。
; - 卸载侧不必自己删:模板自带逻辑会删除「目标指向本程序」的
;   $DESKTOP\${PRODUCTNAME}.lnk(IsShortcutTarget 判定,见 installer.nsi 842-848)。
; - SetLnkAppUserModelId 来自模板 include 的 utils.nsh;用 !ifmacrodef 兜底,
;   万一将来上游改名也不会编译失败。

!macro NSIS_HOOK_POSTINSTALL
  ; 无条件创建(覆盖安装同样生效),目标与开始菜单快捷方式一致
  CreateShortcut "$DESKTOP\${PRODUCTNAME}.lnk" "$INSTDIR\${MAINBINARYNAME}.exe"
  !ifmacrodef SetLnkAppUserModelId
    !insertmacro SetLnkAppUserModelId "$DESKTOP\${PRODUCTNAME}.lnk"
  !endif
!macroend

; 卸载兜底:模板自带删除在部分分支(如覆盖安装路径)下会被跳过;这里再按
; 「目标是否本程序」判定一次(IsShortcutTarget)后删除,不动用户自己改过目标的快捷方式。
!macro NSIS_HOOK_POSTUNINSTALL
  !ifmacrodef IsShortcutTarget
    !insertmacro IsShortcutTarget "$DESKTOP\${PRODUCTNAME}.lnk" "$INSTDIR\${MAINBINARYNAME}.exe"
    Pop $0
    ${If} $0 = 1
      !ifmacrodef UnpinShortcut
        !insertmacro UnpinShortcut "$DESKTOP\${PRODUCTNAME}.lnk"
      !endif
      Delete "$DESKTOP\${PRODUCTNAME}.lnk"
    ${EndIf}
  !endif
!macroend
