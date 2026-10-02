package policyguard

// PowerShell 侧的写目标扫描与危险词表(NOND-W1b)。
//
// 为什么这些用例**必须**存在:这套扫描是 Windows 上唯一的协作层防线(Windows 无内核级
// 沙箱)。一个漏识别的写动词 = 一个静默的越界写通道,而且这类洞**只在真机上暴露**
// —— 这里的表驱动就是把它提前变成 CI 里的红灯。
//
// 反向验证提示:把 psWriteVerbs 里的某个动词删掉,对应用例必须变红。

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func writesOf(cmd string) []string {
	var out []string
	for _, p := range powershellCmdPaths(cmd) {
		if p.Write {
			out = append(out, p.Path)
		}
	}
	return out
}

func TestPowerShellWriteTargets(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string // 期望的写目标(顺序不敏感)
	}{
		{"Set-Content 位置参数", `Set-Content C:\Users\me\notes.txt -Value hi`, []string{`C:\Users\me\notes.txt`}},
		{"Add-Content", `Add-Content .\log.txt "x"`, []string{`.\log.txt`}},
		{"-Path 旗标形式", `Set-Content -Path D:\a\b.txt -Value x`, []string{`D:\a\b.txt`}},
		{"-LiteralPath", `Set-Content -LiteralPath 'C:\my file.txt' -Value x`, []string{`C:\my file.txt`}},
		{"重定向", `Get-Date > C:\out.txt`, []string{`C:\out.txt`}},
		{"追加重定向", `"x" >> ./append.log`, []string{"./append.log"}},
		{"分号多语句", `Get-Process; Remove-Item C:\tmp\a.txt`, []string{`C:\tmp\a.txt`}},
		{"管道后的写", `Get-ChildItem | Out-File C:\list.txt`, []string{`C:\list.txt`}},
		{"New-Item 目录", `New-Item -ItemType Directory -Path C:\newdir`, []string{`C:\newdir`}},
		{"Copy-Item 源", `Copy-Item a.txt C:\dest.txt`, []string{`C:\dest.txt`}},
		{"带值旗标的值不能被当路径", `Set-Content -Path C:\t.txt -Encoding UTF8 -Value x`, []string{`C:\t.txt`}},
		{"读动词不产写目标", `Get-Content C:\secret\id_rsa`, nil},
		{"Test-Path 不产写目标", `Test-Path C:\Users\me\.ssh`, nil},
		// 非目标旗标的值不该被当路径
		{"-Filter 的值不是路径", `Get-ChildItem -Path C:\x -Filter *.log`, nil},
	}
	for _, c := range cases {
		got := writesOf(c.cmd)
		if len(got) != len(c.want) {
			t.Errorf("%s:写目标 %v,期望 %v", c.name, got, c.want)
			continue
		}
		for _, w := range c.want {
			found := false
			for _, g := range got {
				if g == w {
					found = true
				}
			}
			if !found {
				t.Errorf("%s:写目标里缺 %q(got %v)", c.name, w, got)
			}
		}
	}
}

// 不可裁决的写目标必须被标出来(Windows 上没有内核层兜底,这条纪律是唯一防线)。
func TestPowerShellUnresolvableWriteTargets(t *testing.T) {
	cases := []string{
		`Set-Content $target -Value x`,
		`Remove-Item (Get-ChildItem | Where-Object Name -eq 'a') -Recurse -Force`,
		`Set-Content C:\*.txt -Value x`,
		`New-Item -Path "$(Split-Path $x)"`,
	}
	for _, cmd := range cases {
		found := false
		for _, p := range powershellCmdPaths(cmd) {
			if p.Write && p.Unresolvable {
				found = true
			}
		}
		if !found {
			t.Errorf("应标为不可裁决却没标:%q → %+v", cmd, powershellCmdPaths(cmd))
		}
	}
}

// 明确的字面路径不该被误标成不可裁决(否则正常命令全被推去确认 = 用户对审批框脱敏)。
func TestPowerShellLiteralPathsNotUnresolvable(t *testing.T) {
	for _, cmd := range []string{
		`Set-Content C:\Users\me\a.txt -Value x`,
		`Remove-Item .\build\out.o`,
	} {
		for _, p := range powershellCmdPaths(cmd) {
			if p.Write && p.Unresolvable {
				t.Errorf("字面路径不该被标不可裁决:%q → %q", cmd, p.Path)
			}
		}
	}
}

func TestMatchDangerousPowerShell(t *testing.T) {
	cases := []struct {
		cmd  string
		hit  bool
		note string
	}{
		{`Remove-Item C:\x -Recurse -Force`, true, "递归强删"},
		{`Get-ChildItem C:\Users`, false, "只读命令不该命中"},
		{`Get-Process | Select-Object -First 5`, false, "管道读"},
		{`Set-ExecutionPolicy Bypass -Scope CurrentUser`, true, "改执行策略"},
		{`IEX (Get-Content .\payload.txt)`, true, "动态求值"},
		{`Invoke-Expression "rm -rf"`, true, "IEX 另一拼法"},
		{`Set-MpPreference -DisableRealtimeMonitoring $true`, true, "关 Defender"},
		{`Clear-Disk -Number 1`, true, "清盘"},
		{`Remove-Item HKLM:\SOFTWARE\Foo`, true, "注册表"},
		{"'x' | ConvertTo-Json", false, "纯转换"},
	}
	for _, c := range cases {
		_, hit := matchDangerousCmd("powershell", c.cmd)
		if hit != c.hit {
			t.Errorf("%q:命中=%v,期望 %v(%s)", c.cmd, hit, c.hit, c.note)
		}
	}
}

// 同一串命令在两个工具下走**不同的词表** —— 这正是新增这张表的理由。
func TestPowerShellAndShellUseSeparateTables(t *testing.T) {
	ps := `Remove-Item C:\x -Recurse -Force`
	if _, hit := matchDangerousCmd("powershell", ps); !hit {
		t.Error("PowerShell 词表应命中 Remove-Item -Recurse -Force")
	}
	// PowerShell **专属** cmdlet 不能只靠 POSIX 词表兜住(那才是「看起来在管」):
	// 若把 PS 命令交给 POSIX 词表,这一条必须命中不了。
	if _, hit := matchDangerousCmd("shell", `Clear-Disk -Number 1`); hit {
		t.Error("POSIX 词表不该命中 PowerShell 专属 cmdlet(否则说明两边共用了一张表)")
	}
	if _, hit := matchDangerousCmd("powershell", `Clear-Disk -Number 1`); !hit {
		t.Error("PowerShell 词表必须命中 Clear-Disk")
	}
	// 反向:POSIX 的经典危险命令仍由 POSIX 词表命中
	if _, hit := matchDangerousCmd("shell", "rm -rf /"); !hit {
		t.Error("POSIX 词表丢了原有的命中能力")
	}
}

// 派生审批:PowerShell 的写目标落在受保护位置时也要能派生出来。
func TestDerivedApprovalTargetPowerShell(t *testing.T) {
	// 注意用**跨平台都能判**的凭据路径:Windows 的 `C:\…` 在 macOS 上不是绝对路径,
	// 派生逻辑会走相对分支而判不出受保护位置 —— 那是平台语义,不是这条逻辑的问题。
	if _, hit := derivedApprovalTarget("powershell", `Set-Content /home/me/.ssh/authorized_keys -Value x`); !hit {
		t.Error("写凭据路径应派生出审批项")
	}
	if _, hit := derivedApprovalTarget("powershell", `Get-Content /home/me/.ssh/id_rsa`); hit {
		t.Error("纯读不该派生写审批项")
	}
}

// 执行器工具集合:read-only 档必须拒绝它们;值级兜底不得把命令文本当路径。
func TestPowerShellIsExecutorTool(t *testing.T) {
	if !isExecutor("powershell") {
		t.Fatal("powershell 必须算执行器类工具")
	}
	// DefaultSandbox 默认是 workspace-write(见该构造函数注释),read-only 要显式构造。
	ro := &SandboxPolicy{root: t.TempDir(), mode: sdk.SandboxReadOnly}
	if err := ro.CheckTool("powershell"); err == nil {
		t.Error("read-only 档应拒绝执行器类工具")
	}
	// workspace-write 下:工作区内的写目标放行,工作区外拒绝
	root := t.TempDir()
	p2 := &SandboxPolicy{root: root, mode: sdk.SandboxWorkspace}
	// 路径用 filepath.Join:Windows 风格的 `root+"\\a.txt"` 在 macOS 上不是子路径
	// (反斜杠不是分隔符),那是平台语义,不是裁决逻辑的问题。
	if err := p2.CheckExecutorCommandAt(root, "powershell", `Set-Content `+filepath.Join(root, "a.txt")+` -Value x`); err != nil {
		t.Errorf("工作区内写应放行:%v", err)
	}
	if err := p2.CheckExecutorCommandAt(root, "powershell", `Set-Content `+filepath.Join(filepath.Dir(root), "outside.txt")+` -Value x`); err == nil {
		t.Error("工作区外写应被拒")
	}
	if err := p2.CheckExecutorCommandAt(root, "powershell", `Set-Content $t -Value x`); err == nil {
		t.Error("不可裁决的写目标应被拒(Windows 无内核层兜底,不能放行)")
	} else if !strings.Contains(err.Error(), "无法裁决") {
		t.Errorf("错误应说清是「无法裁决」:%v", err)
	}
}

// 两条命令路径的分派:同一个工具名决定用哪套语法扫描。
// 这条断言钉的是「新增执行器时,策略层按名字分派」这件事本身 ——
// 两边共用一张扫描器(初版的隐患)会让 PowerShell 的写目标整个漏掉。
func TestExecutorCommandRoutingByToolName(t *testing.T) {
	root := t.TempDir()
	p := &SandboxPolicy{root: root, mode: sdk.SandboxWorkspace}
	// 同一个命令文本:对 shell 是「未知命令」(不产写目标),对 powershell 是写目标
	const cmd = `Set-Content ` + "/outside/authorized_keys" + ` -Value x`
	if err := p.CheckExecutorCommandAt(root, "shell", cmd); err != nil {
		t.Errorf("shell 走 POSIX 扫描器,不该把 PowerShell 写法认成写目标却报错了:%v", err)
	}
	if err := p.CheckExecutorCommandAt(root, "powershell", cmd); err == nil {
		t.Error("powershell 走自己的扫描器,应识别出越界写目标并拒绝")
	}
}

// 覆盖剩余分支:引号 / 目标标记 / 带值旗标 / 空语句 / 坏引号。
// 这些是词法扫描器的边角,但**正是它们在真机上出事**的地方(引号里的路径被切碎、
// 旗标值被当路径),所以逐条钉住而不是留给覆盖率数字。
func TestPowerShellScannerEdges(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want []string
	}{
		{"重定向目标是引号包起来的", `Get-Date > "C:\out dir\a.txt"`, []string{`C:\out dir\a.txt`}},
		{"重定向目标是单引号", `Get-Date > 'C:\b.txt'`, []string{`C:\b.txt`}},
		{"fd 复制不是路径", `cmd 2>&1 | Out-Null`, nil},
		{"重定向到行尾没东西", `Get-Date >`, nil},
		{"只有标记没给值", `Set-Content -Path`, nil},
		{"目标标记后是引号值", `Remove-Item -LiteralPath "C:\a b\c.txt"`, []string{`C:\a b\c.txt`}},
		{"Move-Item 目标是最后一个", `Move-Item a.txt b.txt C:\dest\`, []string{`C:\dest\`}},
		{"Rename-Item -NewName 不是路径", `Rename-Item C:\a.txt -NewName b.txt`, []string{`C:\a.txt`}},
		{"纯旗标串(无路径)", `Get-ChildItem -Force`, nil},
		{"空语句与空白", ` ;; ; `, nil},
		{"Invoke-WebRequest 的 -OutFile", `Invoke-WebRequest https://x -OutFile C:\d.exe`, []string{`C:\d.exe`}},
	}
	for _, c := range cases {
		got := writesOf(c.cmd)
		if len(got) != len(c.want) {
			t.Errorf("%s:写目标 %v,期望 %v", c.name, got, c.want)
			continue
		}
		for _, w := range c.want {
			found := false
			for _, g := range got {
				if g == w {
					found = true
				}
			}
			if !found {
				t.Errorf("%s:缺 %q(got %v)", c.name, w, got)
			}
		}
	}
}

// 引号没闭合 ⇒ 目标判成不可裁决(不能把半截路径当正常路径放过去)。
func TestPowerShellUnclosedQuoteIsUnresolvable(t *testing.T) {
	for _, p := range powershellCmdPaths(`Set-Content -LiteralPath "C:\a b\c.txt -Value x`) {
		if p.Write && !p.Unresolvable {
			t.Errorf("引号没闭合应判为不可裁决,got %+v", p)
		}
	}
}

// 多行命令(换行分隔)也要逐行扫 —— PowerShell 脚本天然是多行的。
func TestPowerShellMultilineCommand(t *testing.T) {
	got := writesOf("Get-Date\r\nSet-Content C:\\a.txt -Value x\r\nGet-Process")
	if len(got) != 1 || got[0] != `C:\a.txt` {
		t.Fatalf("多行命令应逐行扫,got %v", got)
	}
}

// 词法扫描器的剩余分支:空输入、调用前缀(`&`/`.`/模块限定)、旗标没有值、
// psLastPathArg 的 -Destination/-Filter 组合。
// 这些不是「为了覆盖率」写的:它们各自对应一种真实输入形态,漏了就会在真机上
// 表现成「某条命令的写目标没被识别」。
func TestPowerShellScannerMoreEdges(t *testing.T) {
	// 空 / 纯空白 ⇒ 没有目标
	if got := powershellCmdPaths(""); got != nil {
		t.Errorf("空命令应无目标,got %v", got)
	}
	if got := writesOf("    "); len(got) != 0 {
		t.Errorf("空白命令应无目标,got %v", got)
	}
	// 调用前缀:`& Remove-Item` / `Microsoft.PowerShell.Management\Remove-Item`
	// 前者带 `&`,后者是模块限定名(反斜杠)—— 两者都要剥掉前缀再取动词
	if got := writesOf(`& Remove-Item C:\a\b.txt`); len(got) != 1 || got[0] != `C:\a\b.txt` {
		t.Errorf("带 & 前缀应仍识别写目标,got %v", got)
	}
	if got := writesOf(`Microsoft.PowerShell.Management\Remove-Item C:\c\d.txt`); len(got) != 1 {
		t.Errorf("模块限定名应仍识别写目标,got %v", got)
	}
	// 旗标给了但没给值 ⇒ 没有目标(不能把下一个旗标当路径)
	if got := writesOf(`Set-Content -LiteralPath -Force`); len(got) != 0 {
		t.Errorf("标记后没有值时不该有目标,got %v", got)
	}
	// Copy-Item 的 -Destination 形式
	if got := writesOf(`Copy-Item -Path C:\src\a.txt -Destination D:\dst\a.txt`); len(got) != 1 || got[0] != `D:\dst\a.txt` {
		t.Errorf("Copy-Item -Destination 应取目标路径,got %v", got)
	}
	// 带值旗标在位置参数之后也成立
	if got := writesOf(`Move-Item C:\a\b.txt -Filter *.log`); len(got) != 1 || got[0] != `C:\a\b.txt` {
		t.Errorf("Move-Item 目标应是最后一个路径参数,got %v", got)
	}
	// 未知旗标:只跳过它自己,后面的路径仍然算目标
	if got := writesOf(`Set-Content C:\a.txt -NoSuchFlag x -Value y`); len(got) != 1 || got[0] != `C:\a.txt` {
		t.Errorf("未知旗标不应吃掉后面的路径,got %v", got)
	}
}
