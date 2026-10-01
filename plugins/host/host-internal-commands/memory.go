package hostintcmd

// 跨会话记忆的命令面(第一百零九批):`/memory`。
//
// 形态与技能/角色包命令不同:**子命令 + 自由文本**(记忆正文是自然语言,不是标识符),
// 所以走 `/memory add <内容>` 这种"第一段是子命令、后面全是正文"的分派。
//
// 纪律:
//   - 写入只由**人**发起(自动提取是 M2,且必须先有候选 + 批量确认 + 限流);
//   - 每条可 `rm` 删,`rm --from <会话>` 按来源整段删;
//   - `off` 一键关闭注入(偏好位 memory_off,跨端共享)。

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// cmdMemory /memory list|add|rm|on|off|project [内容]
func (h *Host) cmdMemory(args []string) (string, error) {
	var ms sdk.MemoryService
	if err := h.c.Inject("ctx.memory", &ms); err != nil {
		return "", errString("记忆功能未装配(该构建没装 host-memory 插件)")
	}
	if len(args) == 0 {
		return h.memoryStatus(ms)
	}
	sub := strings.TrimSpace(args[0])
	rest := strings.TrimSpace(strings.Join(args[1:], " "))
	switch sub {
	case "list":
		return h.memoryList(ms)
	case "add":
		if rest == "" {
			return "", errString("用法:/memory add <要记住的内容>(一句话即可,写清「是什么、为什么」)")
		}
		if err := ms.Add(rest, ""); err != nil {
			return "", errString("记不下来:" + err.Error())
		}
		return "已记住(之后每轮都会带进上下文,最多 " + itoa(ms.Budget()) + " 字节):" + rest, nil
	case "rm":
		return h.memoryRemove(ms, args[1:], rest)
	case "on":
		return "记忆注入:" + onoff(ms.SetEnabled(true)), nil
	case "off":
		return "记忆注入:" + onoff(ms.SetEnabled(false)), nil
	case "project":
		// 项目级记忆当前只展示(M1 的写入入口仍走 add;项目级自动提取属 M2)
		lines := h.memoryProjectLines(ms)
		if len(lines) == 0 {
			return "本项目还没有项目级记忆(用户级用 /memory list 看)", nil
		}
		return "本项目记忆:\n  " + strings.Join(lines, "\n  "), nil
	default:
		// 不是已知子命令 ⇒ 当成「用户直接说了要记的内容」(最自然的用法:
		// `/memory 周报里先写结论`,不必先打 add)。
		// 注意不能在这里回落到 memoryStatus:rest 为空恰恰是**只给了一句内容**的
		// 常态,回落会让最自然的用法静默失效(用户以为记住了,其实只看到一段状态)。
		content := strings.TrimSpace(sub + " " + rest)
		if content == "" {
			return h.memoryStatus(ms)
		}
		if err := ms.Add(content, ""); err != nil {
			return "", errString("记不下来:" + err.Error())
		}
		return "已记住:" + content, nil
	}
}

// memoryStatus 概览:开关 + 两条记忆的数量 + 预算。
func (h *Host) memoryStatus(ms sdk.MemoryService) (string, error) {
	return fmt.Sprintf("记忆:%s;用户级 %d 条,本项目 %d 条;注入预算 %d 字节\n  /memory add <内容>  记住一条\n  /memory list       看全部(新的在前)\n  /memory rm <序号>  删一条\n  /memory rm --from <会话>  按来源整段删\n  /memory on|off     开/关注入",
		onoff(ms.Enabled()), len(ms.List()), len(h.memoryProjectLines(ms)), ms.Budget()), nil
}

func (h *Host) memoryList(ms sdk.MemoryService) (string, error) {
	lines := ms.List()
	if len(lines) == 0 {
		return "还没有记忆。用 /memory add <内容> 记一条(比如「报告里的图表用蓝灰配色,不要渐变」)。", nil
	}
	out := "记忆(新的在前,共 " + itoa(len(lines)) + " 条):\n  " + strings.Join(lines, "\n  ")
	if p := h.memoryProjectLines(ms); len(p) > 0 {
		out += "\n本项目:\n  " + strings.Join(p, "\n  ")
	}
	return out, nil
}

// memoryRemove /memory rm <序号> | /memory rm --from <会话>
func (h *Host) memoryRemove(ms sdk.MemoryService, args []string, rest string) (string, error) {
	// --from <会话>:按来源整段删
	if strings.HasPrefix(rest, "--from") {
		src := strings.TrimSpace(strings.TrimPrefix(rest, "--from"))
		if src == "" && len(args) > 1 {
			src = strings.TrimSpace(args[1])
		}
		if src == "" {
			return "", errString("用法:/memory rm --from <会话标识>")
		}
		n, err := ms.RemoveBySource(src)
		if err != nil {
			return "", errString(err.Error())
		}
		if n == 0 {
			return "没有来自该会话的记忆(" + src + ")", nil
		}
		return fmt.Sprintf("已删掉来自会话 %s 的 %d 条记忆(其他来源不受影响)", src, n), nil
	}
	if rest == "" {
		return "", errString("用法:/memory rm <序号>(序号见 /memory list)")
	}
	idx, err := strconv.Atoi(rest)
	if err != nil {
		return "", errString("序号要是一个数字(见 /memory list)")
	}
	line, err := ms.Remove(idx)
	if err != nil {
		return "", errString(err.Error())
	}
	return line, nil
}

func (h *Host) memoryProjectLines(ms sdk.MemoryService) []string {
	type withProject interface {
		ListProject() []string
	}
	if p, ok := ms.(withProject); ok {
		return p.ListProject()
	}
	return nil
}

func onoff(on bool) string {
	if on {
		return "开启(每轮带进上下文)"
	}
	return "已关闭(记忆仍留着,只是不注入)"
}

func itoa(n int) string { return strconv.Itoa(n) }
