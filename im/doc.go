// IM 侧文档预览意图处理(D 组 D5):doc/open 的第四触点(= 文本降级)。
//
// 通道(微信/QQ)不能渲染块模型/TUI pager → 用文本摘要回推(前 N 行 + 页事实/截断提示),
// 尾部给「完整内容请在 Web 端打开」;分块复用 im.Sender(出站预算层,与回合输出同一套)。
package im

import (
	"context"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// docDegrade 文本降级预算(前 N 行 / 总字符上限;超出由 Sender 再分块)。
const (
	docDegradeLines = 40
	docDegradeChars = 1800
)

// LastRoute 返回最近一次通过访问控制的入站会话(离线/无交互时 ok=false)。
// 用于宿主主动推送(如文档预览意图)定向到"最后一个跟机器人说过话的人"。
func (b *Bridge) LastRoute() (Route, bool) {
	b.lastMu.Lock()
	defer b.lastMu.Unlock()
	return b.lastRoute, b.hasLast
}

// setLastRoute 记录最近活跃会话(gate 通过后调用)。
func (b *Bridge) setLastRoute(r Route) {
	b.lastMu.Lock()
	b.lastRoute, b.hasLast = r, true
	b.lastMu.Unlock()
}

// HandleDocOpen 文档预览意图的通道侧呈现:P3 融合下 TUI/Web 各自弹层,
// IM 侧走文本降级(无活跃会话/未授权 → 静默跳过,不打扰)。
func (b *Bridge) HandleDocOpen(ctx context.Context, ev sdk.DocOpenEvent) {
	if strings.TrimSpace(ev.Path) == "" {
		return
	}
	route, ok := b.LastRoute()
	if !ok {
		b.diagf("doc/open: 无活跃 IM 会话,跳过文本降级(path=%s)", ev.Path)
		return
	}
	if b.c == nil {
		return // 无宿主上下文(单测/裸装配):无文档能力,静默
	}
	var doc sdk.DocService
	if err := b.c.Inject("ctx.doc", &doc); err != nil {
		return // host-docview 未装配:静默(该 profile 不含文档能力)
	}
	tx, err := doc.Text(ctx, sdk.DocRequest{
		Path:  ev.Path,
		Page:  ev.Page,
		Sheet: ev.Sheet,
		Limit: docDegradeLines,
	})
	if err != nil {
		_ = b.sendText(ctx, route, "文档预览失败: "+err.Error())
		return
	}
	_ = b.sendText(ctx, route, docDegradeText(tx, ev))
}

// docDegradeText 文档文本降级摘要(纯文本;无 markdown 语法假设,微信/QQ 均可读)。
func docDegradeText(tx *sdk.DocText, ev sdk.DocOpenEvent) string {
	var sb strings.Builder
	name := tx.Path
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	fmt.Fprintf(&sb, "[文档预览] %s", name)
	if tx.Format != "" {
		fmt.Fprintf(&sb, "(%s", tx.Format)
		if tx.TotalLines > 0 {
			fmt.Fprintf(&sb, ",%d 行", tx.TotalLines)
		}
		sb.WriteString(")")
	}
	if ev.Page > 0 {
		fmt.Fprintf(&sb, " 起自第 %d 页", ev.Page)
	}
	if ev.Sheet > 0 {
		fmt.Fprintf(&sb, " 工作表 #%d", ev.Sheet)
	}
	sb.WriteString("\n")
	if tx.PDF != nil {
		fmt.Fprintf(&sb, "PDF:%d 页,类型 %s", tx.PDF.PageCount, tx.PDF.Kind)
		if len(tx.PDF.PagesNeedingOCR) > 0 {
			fmt.Fprintf(&sb, ",%d 页需 OCR", len(tx.PDF.PagesNeedingOCR))
		}
		sb.WriteString("\n")
	}
	used := 0
	shown := 0
	for _, l := range tx.Lines {
		if shown >= docDegradeLines || used+len(l.Text) > docDegradeChars {
			break
		}
		used += len(l.Text) + 1
		shown++
		sb.WriteString(l.Text)
		sb.WriteString("\n")
	}
	if shown < tx.TotalLines {
		fmt.Fprintf(&sb, "…(共 %d 行,已显示 %d 行;完整内容请在 Web 端「文档预览」面板打开)\n", tx.TotalLines, shown)
	}
	for _, w := range tx.Warnings {
		fmt.Fprintf(&sb, "提示:%s\n", w)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// EmitConnect 广播 IM 连接相位变化(im/connect;各端订阅更新连接卡)。
// 事件载荷不含凭证(E0 契约纪律)。
func (b *Bridge) EmitConnect(st sdk.IMConnectStatus) {
	if b == nil || b.c == nil {
		return
	}
	_, _ = b.c.Emit(context.Background(), sdk.EventIMConnect, st, sdk.Emit)
}
