// B2 /export html:会话事件流渲染为自包含 HTML。
// 渲染器唯一实现在 internal/sessionhtml(web 侧栏「导出网页」共用同一份);
// 本文件只留包内名,避免多处调用点与测试跟着改名。
package hostintcmd

import (
	"github.com/nekoleamo/go-agent-harness/internal/sessionhtml"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// renderSessionHTML 事件流 → 自包含 HTML 文档。
func renderSessionHTML(evs []sdk.SessionEvent) string { return sessionhtml.Render(evs) }
