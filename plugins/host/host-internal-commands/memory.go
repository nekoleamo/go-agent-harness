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

	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
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
	case "extract":
		return h.memoryExtract(ms, rest)
	case "status":
		// 显式 status 子命令:没有它的话 `/memory status` 会掉到 default 分支,被当成
		// 「用户要记的内容」写进记忆 —— 一个看起来成功、实际记了句 "status" 的静默失效。
		return h.memoryStatus(ms)
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
		// 回执先说清「还没生效」再列候选:用户刚提一条时最想知道的是这条**进没进上下文**,
		// 而不是一屏编号(只列编号的话,提完与没提看起来一样)。
		return "已放进候选池(**还没进上下文**;确认之后才生效)。\n" +
			mustMemoryCandidates(h, cs), nil
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

// memoryStatus 概览:开关 + 两条记忆的数量 + 预算 + **最近一次自动调取**。
//
// 为什么要带自动调取段:记忆系统最难的判断不是"有没有记忆",而是三种空结果的区别 ——
// 没记忆 / 没命中 / 命中但被预算截掉。三种的下一步动作完全不同,所以必须可见。
func (h *Host) memoryStatus(ms sdk.MemoryService) (string, error) {
	cand := ""
	if cs, ok := ms.(sdk.MemoryCandidates); ok {
		used, limit, today, todayLimit := cs.CandidateQuota()
		if used > 0 {
			cand = fmt.Sprintf(",待确认候选 %d 条(上限 %d;今日已提 %d/%d)", used, limit, today, todayLimit)
		}
	}
	return fmt.Sprintf("记忆:%s;用户级 %d 条,本项目 %d 条%s;注入预算 %d 字节%s%s\n  /memory status     本页概览(含最近一次自动调取)\n  /memory add <内容>  记住一条\n  /memory list       看全部(新的在前)\n  /memory rm <序号>  删一条\n  /memory rm --from <会话>  按来源整段删\n  /memory on|off     开/关注入\n  /memory propose <内容>  提一条候选(确认后才进上下文)\n  /memory candidates / accept <序号> / accept-all / reject <序号>\n  /memory extract on|off  开/关自动提取(规则闸命中才花一次小请求;状态用 /memory status)",
		onoff(ms.Enabled()), len(ms.List()), len(h.memoryProjectLines(ms)), cand, ms.Budget(),
		retrieveStatusLine(ms), extractStatusLine(ms)), nil
}

// extractStatusLine status 里的自动提取段(未实现窄接口的构建不显示)。
func extractStatusLine(ms sdk.MemoryService) string {
	es, ok := ms.(sdk.MemoryExtractStats)
	if !ok {
		return ""
	}
	return "\n  " + extractStatusText(es)
}

// memoryExtract 自动提取的开关与状态:`/memory extract on|off|status`。
//
// 为什么要能单独关提取(而不是只关注入):注入是"读"、提取是"写"。想保留已记的东西
// 但不想再自动攒新的,只关这一个。
func (h *Host) memoryExtract(ms sdk.MemoryService, arg string) (string, error) {
	es, ok := ms.(sdk.MemoryExtractStats)
	if !ok {
		return "", errString("该构建不支持自动提取(ctx.memory 未实现 MemoryExtractStats)")
	}
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "on":
		prefs.Update(func(p *prefs.Prefs) { p.MemoryExtractOff = false })
		return "自动提取已开启:规则闸命中时每次收尾最多一次小请求(每日上限 " +
			strconv.Itoa(memory.ExtractDailyLimit) + " 次)", nil
	case "off":
		prefs.Update(func(p *prefs.Prefs) { p.MemoryExtractOff = true })
		return "自动提取已关闭(已记下的记忆仍然生效;恢复用 /memory extract on)", nil
	case "", "status":
		return extractStatusText(es), nil
	default:
		return "", errString("用法:/memory extract on|off|status")
	}
}

// extractStatusText 提取开关 + 最近一次提取的观测值 + 当日账本。
func extractStatusText(es sdk.MemoryExtractStats) string {
	state := "开"
	if !es.ExtractEnabled() {
		state = "关"
	}
	snap := es.LastExtract()
	var sb strings.Builder
	sb.WriteString("自动提取:" + state)
	if snap.At.IsZero() {
		sb.WriteString(";本会话还没触发过(规则闸未命中或还没跑回合)")
	} else {
		switch {
		case snap.Skipped != "":
			sb.WriteString(";最近一次**没做成**(" + snap.Skipped + ")")
			if snap.Err != "" {
				sb.WriteString(":" + snap.Err)
			}
		case snap.RuleHit == "":
			sb.WriteString(";最近一次规则闸未命中(未花费模型请求)")
		default:
			fmt.Fprintf(&sb, ";最近一次命中「%s」:产出 %d 条,直写 %d,候选 %d,丢弃 %d",
				snap.RuleHit, snap.Produced, snap.Written, snap.Candidate, snap.Dropped)
			if snap.Model != "" {
				sb.WriteString(";模型 " + snap.Model)
			}
		}
	}
	if st, err := memory.LoadExtractState(); err == nil {
		sb.WriteString("\n  " + st.FormatState())
	} else {
		sb.WriteString("\n  (账本读取失败:" + err.Error() + ")")
	}
	return sb.String()
}

// retrieveStatusLine 最近一次自动调取的观测值(未实现该窄接口的构建不显示这一段)。
//
// 口径要点:系统提示里的"稳定块"(最近 N 条)与本轮"尾部追加"(按话题检索、已去重)是两件事。
// 只报后者会让人误以为"记忆没生效",所以两个数都要在。
func retrieveStatusLine(ms sdk.MemoryService) string {
	rs, ok := ms.(sdk.MemoryRetrieveStats)
	if !ok {
		return ""
	}
	st := rs.LastInject()
	mode := rs.RetrieveMode()
	if st.At.IsZero() {
		return fmt.Sprintf("\n  自动调取:%s(本次会话还没跑过回合,暂无观测值)", mode)
	}
	stable := fmt.Sprintf("稳定块 %d 条", st.Stable)
	switch {
	case st.Items > 0 && st.Truncated:
		return fmt.Sprintf("\n  自动调取:%s;%s;本轮追加 %d 条(相关 %d 条,其余超预算),%d 字节;查询:%s",
			st.Mode, stable, st.Items, st.Total, st.Bytes, clipQuery(st.Query))
	case st.Items > 0:
		return fmt.Sprintf("\n  自动调取:%s;%s;本轮追加 %d 条,%d 字节;查询:%s",
			st.Mode, stable, st.Items, st.Bytes, clipQuery(st.Query))
	case st.Total > 0:
		return fmt.Sprintf("\n  自动调取:%s;%s;命中 %d 条但都已在稳定块里或放不进预算(未追加);查询:%s",
			st.Mode, stable, st.Total, clipQuery(st.Query))
	default:
		return fmt.Sprintf("\n  自动调取:%s;%s;本轮未命中(有记忆但与本轮查询无关);查询:%s",
			st.Mode, stable, clipQuery(st.Query))
	}
}

// clipQuery 回显查询时截短(命令回执不是日志,不需要整段粘贴)。
func clipQuery(q string) string {
	q = strings.TrimSpace(q)
	r := []rune(q)
	if len(r) > 40 {
		return string(r[:40]) + "…"
	}
	if q == "" {
		return "(无查询:回退全量注入)"
	}
	return q
}

// mustMemoryCandidates 列候选(出错时回一句说明,不吞错误 —— 命令回执是唯一反馈面)。
func mustMemoryCandidates(h *Host, cs sdk.MemoryCandidates) string {
	out, err := h.memoryCandidatesVia(cs)
	if err != nil {
		return "(候选列表读取失败:" + err.Error() + ")"
	}
	return out
}

// memoryCandidatesVia 列出候选(已取到能力后的实现)。
func (h *Host) memoryCandidatesVia(cs sdk.MemoryCandidates) (string, error) {
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
	return h.memoryCandidatesVia(cs)
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
	proj := h.memoryProjectLines(ms)
	// 「还没有记忆」只在**两级都空**时才能说:只有项目级记忆时说这句,用户会以为
	// `/memory project` 里看到的那条不存在(它确实存在,只是用户级为空)。
	if len(lines) == 0 {
		if len(proj) == 0 {
			return "还没有记忆。用 /memory add <内容> 记一条(比如「报告里的图表用蓝灰配色,不要渐变」)。", nil
		}
		return "用户级还没有记忆;本项目有:\n  " + strings.Join(proj, "\n  "), nil
	}
	out := "记忆(新的在前,共 " + itoa(len(lines)) + " 条):\n  " + strings.Join(lines, "\n  ")
	if len(proj) > 0 {
		out += "\n本项目:\n  " + strings.Join(proj, "\n  ")
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
