// shell 命令路径裁决单测(R10 ①):词法扫描表 + CheckShellCommand 三档语义。
//
// 断言的是"能否指认写目标"这一不变量:写目标必须可裁决且落在档位允许范围;
// 无法裁决的写形态(变量/通配前缀不可知/cd 出工作区)必须显式拒绝,不得乐观放行。
package policyguard

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fmtWrites 只列写语义目标(w?: 前缀表示不可裁决)。
func fmtWrites(ps []shellPath) string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if !p.Write {
			continue
		}
		if p.Unresolvable {
			out = append(out, "w?:"+p.Path)
			continue
		}
		out = append(out, "w:"+p.Path)
	}
	return strings.Join(out, ",")
}

// fmtReads 只列读语义目标(r?: 前缀表示含变量/命令替换)。
func fmtReads(ps []shellPath) string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.Write {
			continue
		}
		if p.Unresolvable {
			out = append(out, "r?:"+p.Path)
			continue
		}
		out = append(out, "r:"+p.Path)
	}
	return strings.Join(out, ",")
}

// TestShellCmdPathsWrites 写目标识别(重定向 / 写命令 / 嵌套 / 无法裁决形态)。
func TestShellCmdPathsWrites(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"重定向写", `echo hi > out.txt`, "w:out.txt"},
		{"追加重定向绝对路径", `echo hi >> /tmp/o`, "w:/tmp/o"},
		{"fd 复制无目标", `echo hi 2>&1`, ""},
		{"fd 前缀重定向", `echo hi 2>err.log`, "w:err.log"},
		{"重定向加命令序列", `echo hi >> a.txt && cat b.txt`, "w:a.txt"},
		{"rm 多目标", `rm -rf /tmp/x ./sub`, "w:/tmp/x,w:./sub"},
		{"tee 追加", `echo x | tee -a /tmp/log`, "w:/tmp/log"},
		{"dd of", `dd if=/dev/zero of=/tmp/img bs=1M`, "w:/tmp/img"},
		{"sed -i", `sed -i s/a/b/ /tmp/f`, "w:/tmp/f"},
		{"sed 非就地不写", `sed s/a/b/ /tmp/f`, ""},
		{"cp 末位是写", `cp -r /etc/hosts ./h`, "w:./h"},
		{"mv 源与目标都是写", `mv ./a /tmp/b`, "w:./a,w:/tmp/b"},
		{"ln 末位是写", `ln -s /tmp/target ./link`, "w:./link"},
		{"tar -C 越界可识别", `tar -xzf a.tgz -C /tmp/x`, "w:/tmp/x"},
		{"tar 创建态 -f 是写", `tar -czf /tmp/o.tgz ./dir`, "w:/tmp/o.tgz"},
		{"unzip -d", `unzip -d /tmp/x a.zip`, "w:/tmp/x"},
		{"嵌套 bash -c", `bash -c "echo x > /tmp/y"`, "w:/tmp/y"},
		{"特权前缀剥离", `sudo -u root rm -rf /tmp/x`, "w:/tmp/x"},
		{"env 前缀剥离", `env FOO=1 rm -rf /tmp/x`, "w:/tmp/x"},
		{"xargs 前缀剥离", `xargs rm -rf /tmp/x`, "w:/tmp/x"},
		{"变量写不可裁决", `echo x > $HOME/f`, "w?:$HOME/f"},
		{"命令替换写不可裁决", `echo hi > $(mktemp)`, "w?:$(mktemp)"},
		{"通配前缀越界可裁决", `rm -rf /tmp/*`, "w:/tmp"},
		{"通配在末段", `rm -rf ./build/*`, "w:./build"},
		{"纯通配按当前目录", `rm -rf *.log`, "w:."},
		{"伪设备不裁决", `echo hi > /dev/null`, ""},
		{"cd 出工作区后相对写不可裁决", `cd /tmp && rm -rf x`, "w?:x"},
		{"cd 进子目录后相对写可裁决", `cd sub && rm -rf x`, "w:x"},
		{"子 shell 内 cd 出工作区", `( cd /tmp && rm x )`, "w?:x"},
		{"引用文本不是重定向", `echo "a > /tmp/b"`, ""},
		{"heredoc 正文不误判", "cat <<EOF\nfoo > /tmp/bar\nEOF\n", ""},
		{"heredoc 前的重定向", "cat > /tmp/o <<EOF\nhello\nEOF\n", "w:/tmp/o"},
		{"空命令", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmtWrites(shellCmdPaths(tc.cmd)); got != tc.want {
				t.Fatalf("shellCmdPaths(%q) 写目标 = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestShellCmdPathsReads 读目标识别(仅用于凭据类判定,不做 workspace 归属限制)。
func TestShellCmdPathsReads(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"普通读", `cat /etc/passwd`, "r:/etc/passwd"},
		{"标志参数不进操作数", `grep -rn foo /usr/include`, "r:foo,r:/usr/include"},
		{"读重定向", `wc -l < in.txt`, "r:in.txt"},
		{"多操作数", `python3 script.py arg1`, "r:script.py,r:arg1"},
		{"无操作数", `git log --oneline`, "r:log"},
		{"读含变量", `cat $HOME/.ssh/id_rsa`, "r?:$HOME/.ssh/id_rsa"},
		{"读留字面量波浪号", `cat ~/.ssh/id_rsa`, "r:~/.ssh/id_rsa"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmtReads(shellCmdPaths(tc.cmd)); got != tc.want {
				t.Fatalf("shellCmdPaths(%q) 读目标 = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestCheckShellCommandWorkspaceWrite workspace-write 档:写目标限工作区内 + 关键误拦豁免。
func TestCheckShellCommandWorkspaceWrite(t *testing.T) {
	ws := t.TempDir()
	p := DefaultSandbox(ws)
	inside := filepath.Join(ws, "out.txt")
	cases := []struct {
		name    string
		cmd     string
		wantErr bool
	}{
		{"写工作区内", `echo hi > ` + inside, false},
		{"相对路径写在工作区内", `echo hi > ./out.txt`, false},
		{"写工作区外", `echo hi > /tmp/out.txt`, true},
		{"写伪设备放行", `echo hi > /dev/null`, false},
		{"rm 工作区内", `rm -rf ` + filepath.Join(ws, "sub"), false},
		{"rm 工作区外", `rm -rf /tmp/sub`, true},
		{"cp 外部读入工作区放行", `cp /etc/hosts ` + inside, false},
		{"mv 目标越界", `mv ` + filepath.Join(ws, "a") + ` /tmp/b`, true},
		{"dd of 越界", `dd if=/dev/zero of=/tmp/x bs=1`, true},
		{"sed -i 越界", `sed -i s/a/b/ /tmp/f`, true},
		{"sed 非就地放行", `sed s/a/b/ /tmp/f`, false},
		{"tee 越界", `echo x 2>&1 | tee /tmp/log`, true},
		{"mkdir 越界", `mkdir -p /tmp/zz`, true},
		{"tar -C 越界", `tar -xzf a.tgz -C /tmp/x`, true},
		{"unzip -d 越界", `unzip -d /tmp/x a.zip`, true},
		{"sudo 前缀写越界", `sudo rm -rf /tmp/x`, true},
		{"嵌套 bash -c 越界", `bash -c "echo x > /tmp/y"`, true},
		{"嵌套 bash -c 工作区内", `bash -c "echo x > ` + inside + `"`, false},
		{"变量写目标显式拒绝", `echo x > $HOME/f`, true},
		{"通配前缀越界", `rm -rf /tmp/*`, true},
		{"通配前缀工作区内", `rm -rf ` + ws + `/*`, false},
		{"cd 出工作区后相对写拒绝", `cd /tmp && rm -rf x`, true},
		{"cd 子目录后相对写在区内", `cd sub && rm -rf x`, false},
		{"cd 工作区内绝对路径后相对写", `cd ` + ws + ` && rm -rf x`, false},
		{"读系统文件放行", `cat /etc/passwd`, false},
		{"读常规目录放行", `ls -la /usr/include`, false},
		{"读凭据拒", `cat ~/.ssh/id_rsa`, true},
		{"读凭据(变量形态)拒", `cat $HOME/.ssh/id_rsa`, true},
		{"读工作区内凭据拒", `cat .env`, true},
		{"heredoc 正文不误判", "cat <<EOF\nfoo > /tmp/bar\nEOF\n", false},
		{"heredoc 前重定向判定", "cat > /tmp/o <<EOF\nhello\nEOF\n", true},
		{"引用文本不误判", `echo "a > /tmp/b"`, false},
		{"空命令放行", "  ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := p.CheckShellCommand(tc.cmd)
			if tc.wantErr && err == nil {
				t.Fatalf("CheckShellCommand(%q) 应拒绝,却放行", tc.cmd)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("CheckShellCommand(%q) 应放行,却拒绝: %v", tc.cmd, err)
			}
		})
	}
}

// TestCheckShellCommandModes read-only 与 full-access 档语义。
func TestCheckShellCommandModes(t *testing.T) {
	ws := t.TempDir()
	p := DefaultSandbox(ws)

	p.SetMode(sdk.SandboxReadOnly)
	if err := p.CheckShellCommand(`echo hi > ./out.txt`); err == nil {
		t.Fatal("read-only 档应拒绝任何写")
	}
	if err := p.CheckShellCommand(`rm -rf ./x`); err == nil {
		t.Fatal("read-only 档应拒绝删除")
	}
	if err := p.CheckShellCommand(`cat /etc/hosts`); err != nil {
		t.Fatalf("read-only 档应放行常规读,got %v", err)
	}
	if err := p.CheckShellCommand(`cat ~/.ssh/id_rsa`); err == nil {
		t.Fatal("read-only 档应拒绝凭据读")
	}

	p.SetMode(sdk.SandboxFullAccess)
	for _, cmd := range []string{`echo hi > /tmp/x`, `rm -rf /tmp/x`, `dd of=/tmp/x`, `cat ~/.ssh/id_rsa`} {
		if err := p.CheckShellCommand(cmd); err != nil {
			t.Fatalf("full-access 档应放行 %q,got %v", cmd, err)
		}
	}
}

// TestCheckShellCommandUnresolvableMessage 不可裁决写的报错要说清原因与出路(可操作)。
func TestCheckShellCommandUnresolvableMessage(t *testing.T) {
	p := DefaultSandbox(t.TempDir())
	err := p.CheckShellCommand(`echo x > $HOME/f`)
	if err == nil {
		t.Fatal("变量写目标应拒绝")
	}
	if !strings.Contains(err.Error(), "无法裁决") || !strings.Contains(err.Error(), "/sandbox full") {
		t.Fatalf("报错应说明原因与出路,got %v", err)
	}
	if err := p.CheckShellCommand(`echo x > /tmp/o`); err == nil ||
		!strings.Contains(err.Error(), "写目标被拒") {
		t.Fatalf("越界写报错应标明写目标被拒,got %v", err)
	}
}

// TestShellCmdPathsQuotingAndOperators 引号/转义/运算符形态:引用文本不得误当命令，真重定向不得漏。
func TestShellCmdPathsQuotingAndOperators(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"双引号内重定向不是重定向", `echo "a > /tmp/b"`, ""},
		{"单引号内重定向不是重定向", `echo 'a > /tmp/b'`, ""},
		{"双引号路径保留空格", `rm -rf "/tmp/quoted dir"`, "w:/tmp/quoted dir"},
		{"单引号路径保留空格", `rm -rf '/tmp/sq dir'`, "w:/tmp/sq dir"},
		{"转义空格", `rm -rf /tmp/x\ y`, "w:/tmp/x y"},
		{"命令替换不可裁决", `echo hi > /tmp/$(date +%s)/f`, `w?:/tmp/$(date +%s)/f`},
		{"反引号不可裁决", "rm -rf `pwd`/x", "w?:`pwd`/x"},
		{"追加到文件与 stderr", `echo hi &>> /tmp/log`, "w:/tmp/log"},
		{"读写重定向是写目标", `echo hi <> f`, "w:f"},
		{"fd 复制无目标", `echo hi >&2`, ""},
		{"fd 前缀加重定向", `echo hi 2>&1 >/tmp/o`, "w:/tmp/o"},
		{"注释内不判定", `rm -rf /tmp/x # > /tmp/y`, "w:/tmp/x"},
		{"行连接继续", "rm -rf /tmp/x \\\n", "w:/tmp/x"},
		{"后台符分隔命令", `nohup rm -rf /tmp/x &`, "w:/tmp/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmtWrites(shellCmdPaths(tc.cmd)); got != tc.want {
				t.Fatalf("shellCmdPaths(%q) 写目标 = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestShellCmdPathsNestedAndPrefixes 嵌套 shell 只展开一层 + 前缀命令剥离。
func TestShellCmdPathsNestedAndPrefixes(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"sh -c", `sh -c "rm -rf /tmp/x"`, "w:/tmp/x"},
		{"bash -c 单引号", `bash -c 'rm -rf /tmp/x'`, "w:/tmp/x"},
		{"eval", `eval "rm -rf /tmp/x"`, "w:/tmp/x"},
		{"command 前缀", `command rm -rf /tmp/x`, "w:/tmp/x"},
		{"sudo 双横线", `sudo -u root -- rm -rf /tmp/x`, "w:/tmp/x"},
		{"xargs 占位符不可裁决", `echo /tmp/x | xargs -I{} rm -rf {}`, "w?:{}"},
		{"嵌套不再深层展开", `bash -c "bash -c \"rm -rf /tmp/x\""`, ""},
		{"非嵌套命令不展开", `python3 -c "print(1)"`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmtWrites(shellCmdPaths(tc.cmd)); got != tc.want {
				t.Fatalf("shellCmdPaths(%q) 写目标 = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestShellCmdPathsCdState cd 后的相对路径可定位性(唯一依据:是否可能离开工作区)。
func TestShellCmdPathsCdState(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"裸 cd 到主目录", `cd && rm -rf x`, "w?:x"},
		{"cd 上级目录", `cd .. && rm -rf x`, "w?:x"},
		{"cd 带 flag 绝对路径", `cd -P /tmp && rm x`, "w?:x"},
		{"cd 变量", `cd "$DIR" && rm x`, "w?:x"},
		{"cd 通配", `cd /tmp/* && rm x`, "w?:x"},
		{"子 shell 结束后恢复", `( cd /tmp && rm x ) && rm -rf y`, "w?:x,w:y"},
		{"子 shell 内判定不受外层影响", `cd /tmp && ( rm -rf x )`, "w?:x"},
		{"cd 后 cd 去子目录仍不可定位", `cd /tmp && cd sub && rm x`, "w?:x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmtWrites(shellCmdPaths(tc.cmd)); got != tc.want {
				t.Fatalf("shellCmdPaths(%q) 写目标 = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestShellCmdPathsArchives tar/unzip 取值形态(含长 flag 与紧贴取值)。
func TestShellCmdPathsArchives(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"tar 长 flag 目标", `tar --directory=/tmp/x -xf a.tgz`, "w:/tmp/x"},
		{"tar -C 紧贴取值", `tar -C/tmp/x -xf a.tgz`, "w:/tmp/x"},
		{"tar --file 创建态是写", `tar --file=/tmp/o.tgz -c ./dir`, "w:/tmp/o.tgz"},
		{"tar 组合短 flag 创建", `tar -czf /tmp/o.tgz ./dir`, "w:/tmp/o.tgz"},
		{"tar 解包态 -f 是读", `tar -xzf a.tgz`, ""},
		{"tar 长 flag 无取值", `tar --list -f a.tgz`, ""},
		{"unzip -d 紧贴取值", `unzip -d/tmp/x a.zip`, "w:/tmp/x"},
		{"unzip 无 -d", `unzip a.zip`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fmtWrites(shellCmdPaths(tc.cmd)); got != tc.want {
				t.Fatalf("shellCmdPaths(%q) 写目标 = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// TestCheckShellCommandTildeAndDevices `~` 展开与伪设备豁免(误拦豁免必须成立,否则常规命令不可用)。
func TestCheckShellCommandTildeAndDevices(t *testing.T) {
	ws := t.TempDir()
	p := DefaultSandbox(ws)
	deny := []string{
		`echo x > ~/f`,
		`echo x > ~`,
		`echo x > ~root/f`,
		`cat ~/.ssh/known_hosts`,
		`cat ~/.aws/config`,
		`cat ~/.config/gcloud/x`,
	}
	for _, cmd := range deny {
		if err := p.CheckShellCommand(cmd); err == nil {
			t.Fatalf("%q 应被拒(主目录/密钥目录/不可裁决写)", cmd)
		}
	}
	allow := []string{
		`echo x > /dev/tty`,
		`echo x > /dev/stdout`,
		`cat /dev/urandom`,
		`echo credentials`,       // 裸词不当路径判(避免把普通词误判成凭据)
		`grep -rn credentials .`, // 搜索词不是路径
		`grep -rn id_rsa .`,      // 密钥文件名作搜索词同样不误拦
		`tar -xf -`,
	}
	for _, cmd := range allow {
		if err := p.CheckShellCommand(cmd); err != nil {
			t.Fatalf("%q 应放行,却拒绝: %v", cmd, err)
		}
	}
}
