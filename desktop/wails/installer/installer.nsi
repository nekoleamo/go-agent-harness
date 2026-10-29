; gah 桌面壳(Go + Wails v3)Windows 安装包 —— 自持 NSIS 脚本(不再依赖 tauri-cli 生成)。
;
; 设计口径(与旧 Tauri 链一致,只保留真正需要的东西):
;   - **按用户安装**(currentUser):不弹 UAC,不需要管理员;装到 %LOCALAPPDATA%\Programs\gah
;   - 只装一个 exe:sidecar 已 gzip 内嵌在壳里,升级时 updater 换的也是这一个文件
;   - **数据不在安装目录**:壳运行后把 sidecar 解到 %LOCALAPPDATA%\dev.gah.desktop\bin,
;     数据根 = 那里同级的 gah-data(卸载**不删**用户数据)
;   - 桌面快捷方式无条件补建:覆盖安装/升级安装时也不能丢(Tauri 链踩过:模板的桌面图标
;     只在「完成页勾选」且非覆盖安装时创建)
;
; 构建(需 makensis,CI 的 windows runner 上装好;本机 mac 不做):
;   makensis -DVERSION=0.1.7 -DAPPEXE=dist-desktop-wails/windows-x86_64/gah-desktop.exe \
;            -DOUTFILE=dist-desktop-wails/windows-x86_64/gah-0.1.7-setup.exe \
;            desktop/wails/installer/installer.nsi

Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef APPEXE
  !define APPEXE "gah-desktop.exe"
!endif
!ifndef OUTFILE
  !define OUTFILE "gah-setup.exe"
!endif

!define APPNAME "gah"
!define PUBLISHER "gah"
!define APPID "dev.gah.desktop"
; 按用户安装:不需要管理员权限(与 currentUser installMode 同口径)。
!define INSTDIR_USER "$LOCALAPPDATA\Programs\${APPNAME}"

Name "${APPNAME} ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "${INSTDIR_USER}"
InstallDirRegKey HKCU "Software\${APPNAME}" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APPNAME}"
VIAddVersionKey "FileDescription" "${APPNAME} 安装程序"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "${PUBLISHER}"

!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APPNAME}.exe"
!define MUI_FINISHPAGE_RUN_TEXT "立即启动 gah"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

Section "安装" SecMain
  SetOutPath "$INSTDIR"

  ; 正在运行就先让用户关掉:Windows 不允许覆盖运行中的 exe,
  ; 否则这里会以「拒绝访问」失败,而错误信息对用户毫无意义。
  ${If} ${FileExists} "$INSTDIR\${APPNAME}.exe"
    ClearErrors
    FileOpen $0 "$INSTDIR\${APPNAME}.exe" a
    ${If} ${Errors}
      MessageBox MB_OKCANCEL|MB_ICONEXCLAMATION \
        "gah 正在运行。请先从托盘菜单选「退出」后再继续安装。" IDOK retry
      Abort "用户需先退出 gah"
      retry:
      ClearErrors
      FileOpen $0 "$INSTDIR\${APPNAME}.exe" a
      ${If} ${Errors}
        Abort "gah 仍在运行,安装中止"
      ${EndIf}
    ${EndIf}
    FileClose $0
  ${EndIf}

  ; 安装包里的 exe 改名成 gah.exe:用户看到/双击的是 gah,不是内部产物名。
  File "/oname=${APPNAME}.exe" "${APPEXE}"

  ; 快捷方式:开始菜单 + 桌面(无条件,覆盖安装也要有)
  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortCut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" "$INSTDIR\${APPNAME}.exe"
  CreateShortCut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\${APPNAME}.exe"

  ; 卸载信息(控制面板「应用和功能」)
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPID}" "DisplayName" "${APPNAME}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPID}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPID}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPID}" "DisplayIcon" "$INSTDIR\${APPNAME}.exe"
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPID}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPID}" "InstallLocation" "$INSTDIR"
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPID}" "NoModify" 1
  WriteRegDWORD HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPID}" "NoRepair" 1
  WriteRegStr HKCU "Software\${APPNAME}" "InstallDir" "$INSTDIR"

  WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

Section "卸载"
  ; 先尝试关掉正在运行的实例:否则删 exe 会失败,留下半卸载状态。
  ; (不追求完美:关不掉就继续,删不掉的文件交给用户下次重试。)
  nsExec::Exec 'taskkill /IM ${APPNAME}.exe /F'
  Pop $0

  Delete "$INSTDIR\${APPNAME}.exe"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk"
  RMDir "$SMPROGRAMS\${APPNAME}"
  Delete "$DESKTOP\${APPNAME}.lnk"

  ; 开机自启(壳通过 Wails autostart 写在 HKCU Run 下)
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APPID}"
  DeleteRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APPID}"
  DeleteRegKey HKCU "Software\${APPNAME}"

  ; **不删用户数据**:%LOCALAPPDATA%\dev.gah.desktop\{gah-shell.log,bin\gah-data}
  ; 卸载删数据是最不可逆的一类事故;要清理请用户自己删目录,安装器只提示。
  MessageBox MB_ICONINFORMATION \
    "已卸载 gah。$\r$\n$\r$\n用户数据仍保留在:%LOCALAPPDATA%\dev.gah.desktop$\r$\n(会话、配置、日志;需要彻底清理时请手动删除该目录)"
SectionEnd
