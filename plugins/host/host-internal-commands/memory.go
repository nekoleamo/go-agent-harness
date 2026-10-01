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
//   - 候选池(第一百一十三批):`propose` 提、`candidates` 看、`accept`/`accept-all` 转正、
//     `reject`/`reject-all` 丢弃。**候选不进上下文**,限流在写入口显式报错。

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
	case "candidates":
		return h.memoryCandidates(ms)
	case "propose":
		if rest == "" {
			return "", errString("用法:/memory propose <内容>(先进候选池,确认后才进上下文)")
		}
		cs, err := h.memoryCandidates2(ms)
		if err != nil {
			return "", err
		}
		if err := cs.Propose(rest, ""); err != nil {
			return "", errString(err.Error())
		}
		return h.memoryCandidates(ms)
	case "accept", "reject":
		return h.memoryAcceptReject(ms, sub, args[1:], rest)
	case "accept-all":
		cs, err := h.memoryCandidates2(ms)
		if err != nil {
			return "", err
		}
		n, err := cs.AcceptAllCandidates()
		if err != nil {
			return "", errString(fmt.Sprintf("已转正 %d 条后出错(半截状态,如上):%s", n, err))
		}
		if n == 0 {
			return "候选池是空的,没东西可转正", nil
		}
		return fmt.Sprintf("已转正 %d 条候选为记忆(之后每轮都会带进上下文)", n), nil
	case "reject-all":
		cs, err := h.memoryCandidates2(ms)
		if err != nil {
			return "", err
		}
		n, err := cs.RejectAllCandidates()
		if err != nil {
			return "", errString("驳回失败:" + err.Error())
		}
		return fmt.Sprintf("已丢弃 %d 条候选(没有进记忆)", n), nil
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
	cand := ""
	if cs, ok := ms.(sdk.MemoryCandidates); ok {
		used, limit, today, todayLimit := cs.CandidateQuota()
		if used > 0 {
			cand = fmt.Sprintf(",待确认候选 %d 条(上限 %d;今日已提 %d/%d)", used, limit, today, todayLimit)
		}
	}
	return fmt.Sprintf("记忆:%s;用户级 %d 条,本项目 %d 条%s;注入预算 %d 字节\n  /memory add <内容>  记住一条\n  /memory list       看全部(新的在前)\n  /memory rm <序号>  删一条\n  /memory rm --from <会话>  按来源整段删\n  /memory on|off     开/关注入\n  /memory propose <内容>  提一条候选(确认后才进上下文)\n  /memory candidates / accept <序号> / accept-all / reject <序号>",
		onoff(ms.Enabled()), len(ms.List()), len(h.memoryProjectLines(ms)), cand, ms.Budget()), nil
}

// memoryCandidates2 取候选能力(未实现 ⇒ 显式错误,不假装"没有候选")。
func (h *Host) memoryCandidates2(ms sdk.MemoryService) (sdk.MemoryCandidates, error) {
	cs, ok := ms.(sdk.MemoryCandidates)
	if !ok {
		return nil, errString("该构建的 ctx.memory 不支持候选池(候选功能未装配)")
	}
	return cs, nil
}

// memoryCandidates 列出候选池 + 用量。回执里带上「能不能提」的额度,免得提了才知道。
func (h *Host) memoryCandidates(ms sdk.MemoryService) (string, error) {
	cs, err := h.memoryCandidates2(ms)
	if err != nil {
		return "", err
	}
	used, limit, today, todayLimit := cs.CandidateQuota()
	lines := cs.ListCandidates()
	if used == 0 {
		return fmt.Sprintf("候选池是空的。用 /memory propose <内容> 提一条 —— 候选**不进上下文**,"+
			"确认(accept)之后才生效。\n额度:今日 %d/%d,池子 %d/%d", today, todayLimit, used, limit), nil
	}
	return fmt.Sprintf("待确认候选 %d 条(新的在前;**都不进上下文**):\n  %s\n"+
		"  /memory accept <序号>   转正为记忆\n  /memory accept-all      全部转正\n"+
		"  /memory reject <序号>   丢弃\n额度:今日 %d/%d,池子 %d/%d",
		used, strings.Join(lines, "\n  "), today, todayLimit, used, limit), nil
}

// memoryAcceptReject /memory accept <序号>|reject <序号>|accept-all|reject-all
func (h *Host) memoryAcceptReject(ms sdk.MemoryService, sub string, args []string, rest string) (string, error) {
	cs, err := h.memoryCandidates2(ms)
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(rest)
	if raw == "" && len(args) > 0 {
		raw = strings.TrimSpace(args[0])
	}
	// accept-all / reject-all 不带序号也合法(第一次敲 sub 时 rest 为空)
	if raw == "" {
		if sub == "accept" {
			return "", errString("用法:/memory accept <序号>(序号见 /memory candidates)")
		}
		return "", errString("用法:/memory reject <序号>(序号见 /memory candidates)")
	}
	idx, cerr := strconv.Atoi(raw)
	if cerr != nil {
		return "", errString("序号要是一个数字(见 /memory candidates)")
	}
	if sub == "accept" {
		row, err := cs.AcceptCandidate(idx)
		if err != nil {
			return "", errString(err.Error())
		}
		return "已转正为记忆:" + row, nil
	}
	row, err := cs.RejectCandidate(idx)
	if err != nil {
		return "", errString(err.Error())
	}
	return "已丢弃候选(没有进记忆):" + row, nil
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
