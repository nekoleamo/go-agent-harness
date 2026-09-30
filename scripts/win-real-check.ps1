# gah Windows 真机验收:自动段(可重放)+ 人工段模板生成
#
# 与 Plan/gah-Windows真机测试清单.md 一一对应(那份是分组与执行顺序,判据仍以 docs/VERIFY.md §B 为准)。
#
# 设计口径:
#   · 自动段只做**确定性判定**(文件落点/进程端口一致性/CLI 子命令/文档抽取/插件清单)。
#     凡"经模型行为才能观测"的项一律标 Kind=weak —— 弱判据不足定论时如实记 SKIP,不假 PASS。
#   · 判不了的项(装得上/首启 SmartScreen/托盘/界面交互)由本脚本渲染成人工段模板。
#   · 全程不读不打印 provider 凭据(只看文件是否存在)。
#
# 编码:本文件必须存为 **UTF-8 with BOM**。Windows PowerShell 5.1 读无 BOM 的 UTF-8 脚本会按
# ANSI 解析 —— 注释乱码无害,判据文案乱码有害(它要进证据文件给人看)。
#
# 用法:
#   powershell -ExecutionPolicy Bypass -File scripts\win-real-check.ps1
#   powershell -ExecutionPolicy Bypass -File scripts\win-real-check.ps1 -GahExe C:\x\gah.exe
#   powershell -ExecutionPolicy Bypass -File scripts\win-real-check.ps1 -Phase post-uninstall
#   powershell -ExecutionPolicy Bypass -File scripts\win-real-check.ps1 -DocSample D:\samples\a.xlsx
#
# 退出码:有 FAIL ⇒ 1;否则 0(有 SKIP 也算通过,清单里逐条记录)。

[CmdletBinding()]
param(
    [string]$GahExe = '',
    [string]$OutDir = '',
    [ValidateSet('installed', 'post-uninstall')][string]$Phase = 'installed',
    [string]$DocSample = '',
    [switch]$SkipWeak
)

try { [Console]::OutputEncoding = New-Object Text.UTF8Encoding($false) } catch { }

if (-not $OutDir) { $OutDir = Join-Path (Get-Location) ('gah-win-evidence-' + (Get-Date -Format 'yyyyMMdd-HHmmss')) }
$RawDir = Join-Path $OutDir 'raw'
$null = New-Item -ItemType Directory -Force -Path $RawDir

$script:R = New-Object System.Collections.ArrayList

function Add-Result {
    param([string]$Id, [string]$Section, [string]$Kind, [string]$Status, [string]$Detail, [string]$Evidence = '')
    $null = $script:R.Add([pscustomobject]@{ Id = $Id; Section = $Section; Kind = $Kind; Status = $Status; Detail = $Detail; Evidence = $Evidence })
    $tag = '[????]'
    if ($Status -eq 'PASS') { $tag = '[PASS]' }
    if ($Status -eq 'FAIL') { $tag = '[FAIL]' }
    if ($Status -eq 'SKIP') { $tag = '[SKIP]' }
    Write-Host ($tag + ' ' + $Id.PadRight(10) + ' ' + $Kind.PadRight(5) + ' ' + $Detail)
}

function Save-Raw {
    param([string]$Name, [string]$Text)
    $p = Join-Path $RawDir ($Name + '.txt')
    [IO.File]::WriteAllText($p, $Text, (New-Object Text.UTF8Encoding($false)))
    return $p
}

# Find-GahExe 默认按桌面壳的落点找(便携口径:数据根 = gah.exe 同级 gah-data/)。
function Find-GahExe {
    $cands = @()
    if ($env:LOCALAPPDATA) {
        $cands += (Join-Path $env:LOCALAPPDATA 'dev.gah.desktop\bin')
        $cands += (Join-Path $env:LOCALAPPDATA 'gah')
    }
    foreach ($d in $cands) {
        if (Test-Path $d) {
            $f = Get-ChildItem -Path $d -Filter 'gah-*.exe' -File -ErrorAction SilentlyContinue |
                Sort-Object LastWriteTime -Descending | Select-Object -First 1
            if ($f) { return $f.FullName }
            $f2 = Get-ChildItem -Path $d -Filter 'gah.exe' -File -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($f2) { return $f2.FullName }
        }
    }
    $cmd = Get-Command gah -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return ''
}

# Invoke-Cli 起一次 gah 并收回 stdout+stderr(独立进程:崩溃/超时不能带走整个验收)。
function Invoke-Cli {
    param([string]$Exe, [string[]]$ArgList, [string]$Tag = 'cli', [int]$TimeoutSec = 120)
    $outFile = Join-Path $RawDir ($Tag + '.out.txt')
    $errFile = Join-Path $RawDir ($Tag + '.err.txt')
    $shown = ($ArgList | ForEach-Object { if ($_ -match '\s') { '"' + $_ + '"' } else { $_ } }) -join ' '
    $p = Start-Process -FilePath $Exe -ArgumentList $ArgList -NoNewWindow -PassThru -RedirectStandardOutput $outFile -RedirectStandardError $errFile
    if (-not $p.WaitForExit($TimeoutSec * 1000)) {
        try { $p.Kill() } catch { }
        return @{ Code = -1; Out = ('超时 ' + $TimeoutSec + 's'); Cmd = $shown }
    }
    $txt = ''
    foreach ($f in @($outFile, $errFile)) {
        if (Test-Path $f) { $txt = $txt + [IO.File]::ReadAllText($f) }
    }
    return @{ Code = $p.ExitCode; Out = $txt; Cmd = $shown }
}

function Has-Any {
    param([string]$Text, [string[]]$Words)
    foreach ($w in $Words) { if ($Text -like ('*' + $w + '*')) { return $true } }
    return $false
}

Write-Host ''
Write-Host '=== gah Windows 真机验收(自动段)===' -ForegroundColor Cyan
Write-Host ('输出目录: ' + $OutDir)
Write-Host ''

# —— 环境 ——
$osInfo = $null
try { $osInfo = Get-CimInstance Win32_OperatingSystem -ErrorAction Stop } catch { }
$osText = '取不到 WMI 信息'
if ($osInfo) { $osText = $osInfo.Caption + ' ' + $osInfo.Version + ' / ' + $env:PROCESSOR_ARCHITECTURE }
Add-Result 'W-ENV-1' '§0' 'auto' 'PASS' ('机器: ' + $osText) (Save-Raw 'env-os' $osText)

$gitCmd = Get-Command git -ErrorAction SilentlyContinue
$hasGit = $null -ne $gitCmd
if ($hasGit) {
    $gitVer = ''
    try { $gitVer = (git --version 2>$null) } catch { }
    Add-Result 'W-ENV-2' '§0' 'auto' 'PASS' ('已装 Git for Windows(' + $gitVer + ')⇒ 本机跑「环境 B」组:89 / 94') ''
} else {
    Add-Result 'W-ENV-2' '§0' 'auto' 'PASS' '未装 Git ⇒ 本机跑「环境 A」组:90 / A5(必须显式报错 + GAH_SHELL_PATH 指引)' ''
}

# —— 运行态:进程与端口必须一致(A9 的可自动判定部分) ——
$procs = @()
try { $procs = @(Get-Process -Name 'gah*' -ErrorAction SilentlyContinue) } catch { }
$nsText = ''
try { $nsText = ([string]::Join("`n", @(netstat -ano | Select-String ':2233' | ForEach-Object { $_.Line }))) } catch { }
$portUp = $nsText -match ':2233\s' -and $nsText -match 'LISTENING'
$null = Save-Raw 'netstat-2233' $nsText
$pids = ($procs | ForEach-Object { $_.Id }) -join ','
if ($procs.Count -gt 0 -and $portUp) {
    Add-Result 'W-ENV-3' 'A9' 'auto' 'PASS' ('运行中且端口已监听(进程 ' + $procs.Count + ' 个:' + $pids + ')') ''
} elseif ($procs.Count -eq 0 -and -not $portUp) {
    Add-Result 'W-ENV-3' 'A9' 'auto' 'PASS' '无 gah 进程且 2233 未占用(已退出的应有状态:端口已释放)' ''
} elseif ($procs.Count -gt 0 -and -not $portUp) {
    Add-Result 'W-ENV-3' 'A9' 'auto' 'FAIL' ('有 gah 进程(' + $pids + ')但 2233 未监听 ⇒ 服务没起来,查壳日志') (Join-Path $RawDir 'netstat-2233.txt')
} else {
    Add-Result 'W-ENV-3' 'A9' 'auto' 'FAIL' '无 gah 进程但 2233 仍被占用(残留)⇒ 用 netstat -ano 找 PID 处理' (Join-Path $RawDir 'netstat-2233.txt')
}

# —— 落点(便携纪律:A3 / 97) ——
if (-not $GahExe) { $GahExe = Find-GahExe }
if ($GahExe -and (Test-Path $GahExe)) {
    $binDir = Split-Path -Parent $GahExe
    $dataDir = Join-Path $binDir 'gah-data'
    $inShellBin = $binDir -like '*dev.gah.desktop\bin*'
    if ($inShellBin) {
        Add-Result 'W-ENV-4' 'A3' 'auto' 'PASS' ('gah.exe 落点正确: ' + $GahExe) ''
    } else {
        Add-Result 'W-ENV-4' 'A3' 'auto' 'SKIP' ('gah.exe 不在桌面壳落点(' + $GahExe + ');若验的是 CLI 安装形态,此项不适用') ''
    }
    if (Test-Path $dataDir) {
        Add-Result 'W-ENV-5' 'A3/97' 'auto' 'PASS' ('数据根在 gah.exe 同级: ' + $dataDir) ''
    } else {
        Add-Result 'W-ENV-5' 'A3/97' 'auto' 'FAIL' ('同级缺 gah-data/ ⇒ 首启未释放数据根或落错位置: ' + $dataDir) ''
    }
} else {
    Add-Result 'W-ENV-4' 'A3' 'auto' 'SKIP' '没找到 gah.exe(未安装 / 已卸载): 用 -GahExe 指定' ''
    Add-Result 'W-ENV-5' 'A3/97' 'auto' 'SKIP' '同上' ''
}

# 安装目录内不应有数据(NSIS currentUser 装到 %LOCALAPPDATA%\<Product>,从卸载注册表定位)
$inst = $null
try {
    $inst = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\*' -ErrorAction SilentlyContinue |
        Where-Object { $_.DisplayName -like '*gah*' } | Select-Object -First 1
} catch { }
if ($inst -and $inst.InstallLocation) {
    $loc = $inst.InstallLocation
    $bad = Join-Path $loc 'gah-data'
    if (Test-Path $bad) {
        Add-Result 'W-ENV-6' 'A3/97' 'auto' 'FAIL' ('安装目录内出现 gah-data/ ⇒ 数据没落到用户数据目录: ' + $bad) ''
    } else {
        Add-Result 'W-ENV-6' 'A3/97' 'auto' 'PASS' ('安装目录内无数据: ' + $loc) ''
    }
} else {
    Add-Result 'W-ENV-6' 'A3/97' 'auto' 'SKIP' '注册表里没有 gah 的卸载项(便携运行 / 未安装)' ''
}

# —— 卸载相位:数据必须残留(96 / 100 / A18) ——
if ($Phase -eq 'post-uninstall') {
    $dataPath = ''
    if ($env:LOCALAPPDATA) { $dataPath = Join-Path $env:LOCALAPPDATA 'dev.gah.desktop\bin\gah-data' }
    if ($dataPath -and (Test-Path $dataPath)) {
        Add-Result 'W-UN-1' 'A18/100' 'auto' 'PASS' ('卸载后数据仍在(卸载不删数据): ' + $dataPath) ''
    } else {
        Add-Result 'W-UN-1' 'A18/100' 'auto' 'FAIL' ('卸载后 gah-data 不见了 ⇒ 卸载删了用户数据: ' + $dataPath) ''
    }
    $exeGone = -not ($GahExe -and (Test-Path $GahExe))
    if ($exeGone) {
        Add-Result 'W-UN-2' 'A18' 'auto' 'PASS' '可执行文件已随卸载移除' ''
    } else {
        Add-Result 'W-UN-2' 'A18' 'auto' 'FAIL' ('卸载后 gah.exe 仍在: ' + $GahExe) ''
    }
}

# —— CLI 面 ——
if ($GahExe -and (Test-Path $GahExe)) {
    $v = Invoke-Cli $GahExe @('-version') 'cli-version' 30
    if ($v.Code -eq 0 -and $v.Out.Trim().Length -gt 0) {
        Add-Result 'W-CLI-1' '§9' 'auto' 'PASS' ('gah -version 可跑: ' + (($v.Out -split "`n")[0]).Trim()) ''
    } else {
        Add-Result 'W-CLI-1' '§9' 'auto' 'FAIL' ('gah -version 失败(退出码 ' + $v.Code + ')') (Join-Path $RawDir 'cli-version.out.txt')
    }

    # 94:外部插件的 windows 产物必须带 .exe 且能被列出(命名解析)
    $lp = Invoke-Cli $GahExe @('-list-plugins') 'cli-listplugins' 60
    if ($lp.Code -eq 0 -and (Has-Any $lp.Out @('.exe')) -and -not (Has-Any $lp.Out @('未安装', 'none'))) {
        Add-Result 'W-CLI-2' '§2-94' 'auto' 'PASS' '外部插件清单含 .exe 产物(命名解析可用)' ''
    } elseif ($lp.Code -eq 0) {
        Add-Result 'W-CLI-2' '§2-94' 'auto' 'SKIP' ('清单里没看到 .exe 条目(可能没装外部插件): ' + (($lp.Out -split "`n")[0]).Trim()) (Join-Path $RawDir 'cli-listplugins.out.txt')
    } else {
        Add-Result 'W-CLI-2' '§2-94' 'auto' 'FAIL' ('gah -list-plugins 失败(退出码 ' + $lp.Code + ')') (Join-Path $RawDir 'cli-listplugins.out.txt')
    }

    if (-not $hasGit) {
        # 90:无 Git 环境必须显式报错并给 GAH_SHELL_PATH 指引(文案是代码里写死的 ⇒ 强判据)
        $g = Invoke-Cli $GahExe @('-profile', 'headless', '-input', 'echo hi') 'cli-nogit-shell' 180
        if (Has-Any $g.Out @('GAH_SHELL_PATH')) {
            Add-Result 'W-CLI-3' '§2-90' 'auto' 'PASS' '无 Git 时给出 GAH_SHELL_PATH 指引(显式,不静默)' (Join-Path $RawDir 'cli-nogit-shell.out.txt')
        } else {
            Add-Result 'W-CLI-3' '§2-90' 'auto' 'FAIL' '无 Git 环境未见 GAH_SHELL_PATH 指引 ⇒ 违反「缺失依赖显式失败」' (Join-Path $RawDir 'cli-nogit-shell.out.txt')
        }
    } else {
        Add-Result 'W-CLI-3' '§2-90' 'auto' 'SKIP' '本机装了 Git ⇒ 90 需在「环境 A」机器上跑(或在 PATH 临时摘掉 Git 再跑)' ''
    }

    if ($SkipWeak) {
        Add-Result 'W-CLI-4' '§2-89' 'weak' 'SKIP' '-SkipWeak:跳过弱判据项' ''
    } else {
        # 89/A5:经模型触发 shell(弱判据 —— 输出形状依赖模型与供应商)
        $h = Invoke-Cli $GahExe @('-profile', 'headless', '-input', '用 shell 工具执行 echo hi,并原样报告它的输出') 'cli-shell-echo' 240
        if ($h.Code -eq 0 -and (Has-Any $h.Out @('hi'))) {
            Add-Result 'W-CLI-4' '§2-89' 'weak' 'PASS' '有 Git 时 shell 跑通(输出含 hi)' (Join-Path $RawDir 'cli-shell-echo.out.txt')
        } elseif ($h.Code -eq 0) {
            Add-Result 'W-CLI-4' '§2-89' 'weak' 'SKIP' '输出未见 hi ⇒ 弱判据不足定论,请人工核对原始输出' (Join-Path $RawDir 'cli-shell-echo.out.txt')
        } else {
            Add-Result 'W-CLI-4' '§2-89' 'weak' 'FAIL' ('headless 回合失败(退出码 ' + $h.Code + ')') (Join-Path $RawDir 'cli-shell-echo.out.txt')
        }

        # 91/A7:越界写必须被拒(弱判据;关键词只是辅助,原始输出要人看)
        $d = Invoke-Cli $GahExe @('-profile', 'headless', '-input', '用 file_write 把 hello 写到 C:\Windows\gah-deny-probe.txt,然后原样报告工具返回') 'cli-deny-write' 240
        if (Has-Any $d.Out @('拒绝', '被拒', 'denied', '不允许', '沙箱')) {
            Add-Result 'W-CLI-5' '§2-91' 'weak' 'PASS' '区外写在输出里呈现为拒绝' (Join-Path $RawDir 'cli-deny-write.out.txt')
        } else {
            Add-Result 'W-CLI-5' '§2-91' 'weak' 'SKIP' '输出未见拒绝类文案 ⇒ 弱判据不足定论(模型可能改写提示),请人工核对' (Join-Path $RawDir 'cli-deny-write.out.txt')
        }

        # 93:盘符大小写一致(弱判据)
        $c = Invoke-Cli $GahExe @('-profile', 'headless', '-input', '用 file_write 分别写 D:\gah-case-a.txt 与 d:\gah-case-b.txt,报告两次结果,不要改写盘符') 'cli-drive-case' 240
        if ($c.Code -eq 0) {
            Add-Result 'W-CLI-6' '§2-93' 'weak' 'SKIP' '盘符大小写判定需人工看两次结果是否同口径(弱判据;原始输出已存)' (Join-Path $RawDir 'cli-drive-case.out.txt')
        } else {
            Add-Result 'W-CLI-6' '§2-93' 'weak' 'FAIL' ('headless 回合失败(退出码 ' + $c.Code + ')') (Join-Path $RawDir 'cli-drive-case.out.txt')
        }
    }

    # A8:文档读取(不经模型 ⇒ 强判据;界面观感另见人工段)
    $dh = Invoke-Cli $GahExe @('doc', '--help') 'cli-doc-help' 30
    if ($dh.Code -eq 0 -and (Has-Any $dh.Out @('--json', '--text', '--tree'))) {
        Add-Result 'W-DOC-1' 'A8' 'auto' 'PASS' 'gah doc 可用(打印了用法)' ''
    } else {
        Add-Result 'W-DOC-1' 'A8' 'auto' 'FAIL' ('gah doc --help 不可用(退出码 ' + $dh.Code + ')') (Join-Path $RawDir 'cli-doc-help.out.txt')
    }
    if ($DocSample) {
        if (Test-Path $DocSample) {
            $ext = [IO.Path]::GetExtension($DocSample).ToLower()
            $dtxt = Invoke-Cli $GahExe @('doc', $DocSample, '--text') 'cli-doc-sample' 120
            if ($dtxt.Code -eq 0 -and $dtxt.Out.Trim().Length -gt 20) {
                Add-Result 'W-DOC-2' 'A8' 'auto' 'PASS' ('抽取成功(' + $ext + ', ' + $dtxt.Out.Length + ' 字符;中文是否乱码需人眼看 raw)') (Join-Path $RawDir 'cli-doc-sample.out.txt')
            } else {
                Add-Result 'W-DOC-2' 'A8' 'auto' 'FAIL' ('抽取失败(' + $ext + ', 退出码 ' + $dtxt.Code + ')') (Join-Path $RawDir 'cli-doc-sample.out.txt')
            }
        } else {
            Add-Result 'W-DOC-2' 'A8' 'auto' 'SKIP' ('-DocSample 指向的文件不存在: ' + $DocSample) ''
        }
    } else {
        Add-Result 'W-DOC-2' 'A8' 'auto' 'SKIP' '未给 -DocSample(用 -DocSample <docx|xlsx|pdf> 让本项自动跑)' ''
    }
} else {
    Add-Result 'W-CLI-1' '§9' 'auto' 'SKIP' '没找到 gah.exe,CLI 面整体跳过(用 -GahExe 指定)' ''
}

# —— 汇总 ——
$pass = @($script:R | Where-Object { $_.Status -eq 'PASS' }).Count
$fail = @($script:R | Where-Object { $_.Status -eq 'FAIL' }).Count
$skip = @($script:R | Where-Object { $_.Status -eq 'SKIP' }).Count

$md = New-Object System.Collections.ArrayList
$null = $md.Add('# gah Windows 真机验收 —— 自动段证据')
$null = $md.Add('')
$null = $md.Add('- 时间: ' + (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'))
$null = $md.Add('- 机器: ' + $osText)
$null = $md.Add('- 相位: ' + $Phase + ' / gah.exe: ' + $GahExe)
$null = $md.Add('- 结果: PASS ' + $pass + ' / FAIL ' + $fail + ' / SKIP ' + $skip)
$null = $md.Add('')
$null = $md.Add('| 项 | 组 | 判据强度 | 结果 | 说明 | 原始输出 |')
$null = $md.Add('| --- | --- | --- | --- | --- | --- |')
foreach ($r in $script:R) {
    $ev = [string]$r.Evidence
    if ($ev -and $ev.StartsWith($OutDir)) { $ev = $ev.Substring($OutDir.Length).TrimStart('\') }
    if (-not $ev) { $ev = '—' }
    $null = $md.Add('| ' + $r.Id + ' | ' + $r.Section + ' | ' + $r.Kind + ' | ' + $r.Status + ' | ' + ($r.Detail -replace '\|', '/') + ' | ' + $ev + ' |')
}
$null = $md.Add('')
$null = $md.Add('> weak = 判据依赖模型行为,SKIP 不等于通过;原始输出在 `raw/`,请人眼核对后再在人工段抄结论。')
[IO.File]::WriteAllText((Join-Path $OutDir 'evidence.md'), ($md -join "`r`n"), (New-Object Text.UTF8Encoding($false)))

# —— 人工段模板 ——
# 每项: 组 / 编号 / 做什么 / 判据要点。判据权威定义见 Plan/gah-Windows真机测试清单.md。
$manual = @(
    @('§1', 'A1', '双击 gah_<版本>_x64-setup.exe 安装', '装到 %LOCALAPPDATA%,生成快捷方式,**不弹 UAC**(不要管理员权限)'),
    @('§1', 'A2', '首启遇 SmartScreen → 更多信息 → 仍要运行', '窗口**非白屏**(白屏即失败,把壳日志发回来)'),
    @('§1', 'A4', '无 provider 首启', '自动开设置 + 中文引导;存 Key 后回「已连通,拉到 N 个模型」'),
    @('§1', 'A9', '托盘三项 + 退出后看任务管理器', '三项齐全;退出后无 gah*.exe 残留(自动段已查端口一致性)'),
    @('§1', 'A10', '再启动一次', '聚焦已有窗口,不新开第二个服务'),
    @('§1', 'A17', '装旧版 → 覆盖装新版', '数据/密钥/会话/计划全在,仍在上面的落点'),
    @('§2', '92', '越界路径经 git-bash 传给 shell', 'MSYS_NO_PATHCONV 生效(越界路径不被改写成区内路径后放行)'),
    @('§3', 'A7', '让模型写区外路径', '被拒且**原因对模型可见**;jail 内可写;shell 与 pty 两条路一致'),
    @('§3', 'A8', '打开 .xlsx / .pdf / .docx(可用 gah doc 抽同一份)', 'xlsx 可切工作表、PDF 可翻页、docx 中文不乱码'),
    @('§4', 'A11', '加 MCP server(stdio)→ 保存重载', '工具可用;切 mode: search 后只剩 mcp_search / mcp_call'),
    @('§4', 'A12', 'approval_tools 写真实工具名 → 经 mcp_call 调用', '弹确认(按**真实目标名**命中,防间接名整体绕过)'),
    @('§4', 'N1', '用小窗口模型或长会话逼超窗', '不中断,提示通道出现「已自动压缩上下文」,同回合继续;连续超窗给 /compact 出路'),
    @('§4', 'N2', '窗口内滚动', '整页**不可滚**:只有会话流 / 侧栏列表 / 抽屉各自内滚'),
    @('§4', 'N3', '侧栏「⤓」→ 导出 HTML / JSONL', '桌面端落 Downloads(或选择目录)+ 回执「已导出到 …」,HTML 自动用浏览器打开'),
    @('§4', 'N4', '必然失败的计划 → 看通知', '**系统横幅真弹**(Windows 无 mac 的签名限制);窗口在前台时不重复打扰'),
    @('§5', 'A13', '建 1 分钟后触发的计划', '触发并跑一轮,会话列表可见产出'),
    @('§5', 'A14', '必然失败的计划', '通知「计划「X」执行失败:…」,正文含任务 id'),
    @('§5', 'A15', '计划里跑 rm -rf 类危险命令', '**被拒**且原因对模型可见(即便审批档为 open)'),
    @('§5', 'A16', '非 UTC 建计划 + 跨 DST 切换点', '触发时间正确;跨 DST 行为一致'),
    @('§6', '95', '下载 → 安装 → 首启 → 能对话', 'Windows 侧全流程通'),
    @('§6', '96', '卸载后看数据目录', '残留符合文档(Win 与 mac 口径一致;自动段 W-UN-1 已查存在性)'),
    @('§6', '98', '旧版升级 / 换包', '复制到新位置 + 通知新旧路径'),
    @('§6', '99', '升级后检查', '数据/密钥/会话/计划全在'),
    @('§6', '101', '清 Zone.Identifier 后再装一次', '无二次 SmartScreen(mac quarantine 归 A-4,已随不做公证撤销)'),
    @('§6', '102', '把用户数据目录设为只读', '**通知 + 退回应用目录内运行**,不崩(需 ACL 操作,管理员权限)'),
    @('§6', '103', 'NOND-W2b 评估', '用户数据目录是否升级为可选/可迁移 —— **只给结论即可**'),
    @('§7', '104', '首启引导过场(Windows + 干净 gah-data)', '与 A4 同验'),
    @('§7', '105', '真机时区过场(非 UTC + 跨 DST 实际触发)', '与 A16 同验'),
    @('§7', '106', '设置面板改 MCP 配置 → 保存重载', '工具生效(Windows + 干净数据根)'),
    @('§7', '107', 'MCP 仍 stdio-only', 'Windows 上 stdio server 能否启动(**此前未验**)'),
    @('§8', '147', '设置面板:点遮罩空白 / 按 Esc', '点遮罩**不关闭**(出口只剩 ✕ / Esc);Esc 只关设置、不清空输入框草稿'),
    @('§8', '148', '设置里新增 provider', '主界面「还没有配置模型」**立即消失**(无需重启/刷新)'),
    @('§8', '149', '从资源管理器拖文件/图片到输入区', '出现 chip + 图片缩略图(拖到**窗口任意位置**也应收到,0.1.5 起)'),
    @('§8', '150', '提交图片/文本/中文名/含空格路径附件', '不再报 HTTP 400「附件路径非法」(这里验 Windows 盘符/大小写语义)'),
    @('§8', '151', '传超 20MB 附件', '提示可 **×** 关闭且 10s 自动消失;换文件后不残留旧提示'),
    @('§8', '152', '设置抽屉/侧栏滚动区内看 tooltip', '**不再被裁剪**;贴边不溢出视口(贴底翻到下方)'),
    @('§8', '153', '工作区「＋ 打开」输入绝对路径(D:\work\proj,可带复制产生的引号)', '切换成功 + 新建会话 + 列表出现该目录;Esc 取消不切换'),
    @('§8', '159', '桌面快捷方式(R13)三态', '① 全新安装出现图标且双击能起(不是白板);② 覆盖安装同样出现;③ 卸载后被删(用户改过目标的不误删)')
)

$mc = New-Object System.Collections.ArrayList
$null = $mc.Add('# gah Windows 真机验收 —— 人工段(逐项填写)')
$null = $mc.Add('')
$null = $mc.Add('判据权威定义: `Plan/gah-Windows真机测试清单.md`(本文件只给填写位)。')
$null = $mc.Add('证据写法: 截图文件名 `evidence/<组>-<编号>-<一句话>.png`;日志行贴原文(不要转述)。')
$null = $mc.Add('')
$null = $mc.Add('| 组 | # | 做什么 | 判据要点 | 结果 | 证据 |')
$null = $mc.Add('| --- | --- | --- | --- | --- | --- |')
foreach ($m in $manual) {
    $null = $mc.Add('| ' + $m[0] + ' | ' + $m[1] + ' | ' + $m[2] + ' | ' + $m[3] + ' | | |')
}
$null = $mc.Add('')
$null = $mc.Add('> 跑完把结论同步到 `docs/VERIFY.md`(§B 与「剩余验收任务」快照)与 `DESIGN.md §14.1`(Windows 行)。')
[IO.File]::WriteAllText((Join-Path $OutDir 'manual-checklist.md'), ($mc -join "`r`n"), (New-Object Text.UTF8Encoding($false)))

Write-Host ''
Write-Host ('=== 汇总: PASS ' + $pass + ' / FAIL ' + $fail + ' / SKIP ' + $skip + ' ===') -ForegroundColor Cyan
Write-Host ('证据: ' + (Join-Path $OutDir 'evidence.md'))
Write-Host ('人工段模板: ' + (Join-Path $OutDir 'manual-checklist.md'))
Write-Host ('原始输出: ' + $RawDir)
Write-Host ''
Write-Host '注:weak 项的 SKIP 需要人眼核对 raw/ 里的原始输出;本脚本无法判定「装得上 / SmartScreen / 托盘 / 界面交互」。'

if ($fail -gt 0) { exit 1 }
exit 0
