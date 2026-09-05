// P4-6 外部编辑器整段编辑(Ctrl+G):$VISUAL/$EDITOR/nano 打开当前输入。
// tea.ExecProcess 阻塞期间自动 releaseTerminal(临时退出 alt-screen),编辑器
// 获得完整终端交互;退出后恢复 TUI 并收到 editorDoneMsg 回填(ApplyExternal)。
package tui

import (
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// editorDoneMsg 外部编辑器进程结束:path 临时文件(待读回),err 非空 = 运行失败
// (用户已改未存/编辑器崩溃等;此时保留原输入并提示,不读回)。
type editorDoneMsg struct {
	path string
	err  error
}

// externalEdit Ctrl+G 入口:编辑器可用且选择器未激活时,将当前输入写入临时文件
// 并以 ExecProcess 启动编辑器(命令可带参数,如 "code -w")。返回 tea.Cmd。
func (m *Model) externalEdit() tea.Cmd {
	if m.state.Pick != nil {
		return nil
	}
	ed := editorCommand()
	if ed == nil {
		m.state.SetError("未找到外部编辑器: 请设置 $EDITOR 或 $VISUAL(安装 nano 亦可)")
		return nil
	}
	f, err := os.CreateTemp("", "gah-input-*.txt")
	if err != nil {
		m.state.SetError("创建临时文件失败: " + err.Error())
		return nil
	}
	name := f.Name()
	_, werr := f.WriteString(m.state.Input)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(name)
		if werr != nil {
			m.state.SetError("写入临时文件失败: " + werr.Error())
		} else {
			m.state.SetError("关闭临时文件失败: " + cerr.Error())
		}
		return nil
	}
	args := append(append([]string{}, ed[1:]...), name)
	cmd := exec.Command(ed[0], args...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorDoneMsg{path: name, err: err}
	})
}

// finishExternal 编辑器进程结束处理:读回临时文件内容回填输入(无变化不动/undo 一步)。
func (m *Model) finishExternal(msg editorDoneMsg) {
	defer os.Remove(msg.path) // 临时文件用完即焚
	if msg.err != nil {
		m.state.SetError("外部编辑器退出异常: " + msg.err.Error() + "(原输入保留)")
		return
	}
	b, err := os.ReadFile(msg.path)
	if err != nil {
		m.state.SetError("读取编辑器结果失败: " + err.Error() + "(原输入保留)")
		return
	}
	m.state.ApplyExternal(string(b))
	m.syncHints()
}

// editorCommand 解析外部编辑器命令:顺序 $VISUAL → $EDITOR → nano(环境未设时兜底)。
// 支持带参数命令(如 "code -w");仅返回首个可执行项;全部不可用返回 nil。
// 引号包裹的空格参数不做展开(罕见场景,文档说明)。
func editorCommand() []string {
	for _, v := range []string{os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if v == "" {
			continue
		}
		f := strings.Fields(v)
		if len(f) > 0 {
			if _, err := exec.LookPath(f[0]); err == nil {
				return f
			}
		}
	}
	if _, err := exec.LookPath("nano"); err == nil {
		return []string{"nano"}
	}
	return nil
}
