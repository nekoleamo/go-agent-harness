# 全局安装(Windows,方案 A):一次部署,任意目录敲 `gah` 启动(pi 式)。
#   - 构建 gah.exe → 安装到 %LOCALAPPDATA%\gah(可 -InstallDir 覆盖)
#   - 把安装目录加入【用户级】PATH(新开终端生效;不改系统级)
#   - 数据根(R8)= gah.exe 同级 gah-data/(即 %LOCALAPPDATA%\gah\gah-data,首次运行自动创建)
# 用法(以管理员不需要;用户级 PATH 即可):
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -InstallDir D:\tools\gah
#   powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Uninstall
# 前置:本机已安装 Go 工具链(或从 GitHub Release 下载 gah.exe 后手动放入 InstallDir)。
param(
    [string]$InstallDir = "$env:LOCALAPPDATA\gah",
    [switch]$Uninstall
)
$ErrorActionPreference = 'Stop'
$prog = Join-Path $InstallDir 'gah.exe'

function Add-UserPath([string]$dir) {
    $cur = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($cur -split ';' -notcontains $dir) {
        $next = if ([string]::IsNullOrEmpty($cur)) { $dir } else { $cur.TrimEnd(';') + ';' + $dir }
        [Environment]::SetEnvironmentVariable('Path', $next, 'User')
        Write-Host "已加入用户 PATH:$dir(请新开终端后生效)"
    } else {
        Write-Host "PATH 已包含:$dir"
    }
}

if ($Uninstall) {
    if (Test-Path (Join-Path $InstallDir 'gah.exe')) { Remove-Item -Recurse -Force $InstallDir }
    # 从用户 PATH 移除安装目录
    $cur = [Environment]::GetEnvironmentVariable('Path', 'User')
    if ($cur) {
        $kept = ($cur -split ';' | Where-Object { $_ -ne $InstallDir }) -join ';'
        [Environment]::SetEnvironmentVariable('Path', $kept, 'User')
    }
    Write-Host "已卸载:$InstallDir(数据已删除;如需保留请先 /backup)"
    exit 0
}

Write-Host "[1/3] 准备目录:$InstallDir"
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

Write-Host "[2/3] 构建 gah.exe(当前源码)"
$ver = 'dev'
$tag = git describe --tags --exact-match 2>$null
if ($LASTEXITCODE -eq 0 -and $tag) { $ver = $tag.TrimStart('v') }
Push-Location (Split-Path -Parent $PSScriptRoot)   # 仓库根(cmd/gah 所在)
try {
    $env:CGO_ENABLED = '0'
    & go build -trimpath "-ldflags=-s -w -X main.version=$ver" -o $prog ./cmd/gah
    if ($LASTEXITCODE -ne 0) { throw 'go build 失败(请确认已安装 Go 并处于仓库目录)' }
} finally { Pop-Location }

Write-Host "[3/3] 加入用户 PATH"
Add-UserPath $InstallDir

Write-Host ""
Write-Host "安装完成 ✓"
Write-Host "  命令   : 新开 PowerShell/CMD → gah(TUI)/ gah web / gah --profile headless -input ..."
Write-Host "  数据根 : $InstallDir\gah-data(首次运行自动创建;会话按项目 cwd 自动隔离)"
Write-Host "  升级   : 重跑本脚本(替换 exe,数据不动)"
Write-Host "  卸载   : powershell -ExecutionPolicy Bypass -File scripts\install.ps1 -Uninstall"
