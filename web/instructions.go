// web 全局指令端点(第八十一批 · 形态 A)。
//
// 为什么单独一对端点:全局 $GAH_HOME/AGENTS.md 对**所有**角色生效(角色未声明
// exclude_global 时),此前只能在文件系统上手改 —— 面板有角色级 AGENTS.md 编辑器,
// 却没有这一份全局的。文件读写细节在 internal/instructions(路径/上限单一出口)。
//
// 与角色级编辑器的差别只有一处:全局指令是**启动/`/reload` 时读进内存**的(角色身份槽
// 则每轮现算),所以写完必须触发 ReloadInstructions —— 否则文件变了、本轮提示还是旧的,
// 就是"保存了但不生效"的悬案(重载失败 → 200 + warning 如实说明,文件已写)。
package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nekoleamo/go-agent-harness/internal/instructions"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 重载不可用的三种原因分开说:用户看到的"为什么没生效"不该是一句笼统的失败。
var (
	errNoCtxForInstructions = errors.New("宿主上下文未就绪")
	errSystemPromptMissing  = errors.New("ctx.systemPrompt 未装配(缺 host-system-prompt 插件)")
	errReloadUnsupported    = errors.New("该 SystemPromptService 不支持指令热重载")
)

// instructionsView GET /api/instructions 的响应体。
type instructionsView struct {
	Path     string `json:"path"`
	Text     string `json:"text"`
	Bytes    int    `json:"bytes"`
	Exists   bool   `json:"exists"`
	MaxBytes int    `json:"max_bytes"`
	Over     bool   `json:"over"` // 手改超限的文件:能读、能看,但面板保存会被拒
}

// handleInstructions GET /api/instructions —— 全局指令只读视图(缺文件 = 空 + exists:false)。
func (s *Server) handleInstructions(w http.ResponseWriter, r *http.Request) {
	text, exists, err := instructions.Read()
	if err != nil {
		http.Error(w, "全局指令读取失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, instructionsView{
		Path:     instructions.Path(),
		Text:     text,
		Bytes:    len(text),
		Exists:   exists,
		MaxBytes: instructions.MaxBytes,
		Over:     len(text) > instructions.MaxBytes,
	})
}

// handleInstructionsSave PUT /api/instructions {text} —— 覆盖写全局指令 + 让宿主重读。
func (s *Server) handleInstructionsSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if err := instructions.Write(body.Text); err != nil {
		// 超限 = 内容问题(400,面板会就地显示"请精简");写盘失败 = 环境问题(500,别让
		// 用户以为是自己内容不对)。
		code := http.StatusInternalServerError
		if errors.Is(err, instructions.ErrTooLarge) {
			code = http.StatusBadRequest
		}
		http.Error(w, err.Error(), code)
		return
	}
	out := map[string]any{"ok": true, "bytes": len(body.Text), "applied": false}
	// 写盘成功只是"文件变了";跑在进程里的提示要不要跟着变,取决于重载能不能成。
	if err := s.reloadInstructions(); err != nil {
		out["warning"] = "已写入文件,但重载失败(本轮提示仍是旧内容):" + err.Error()
	} else {
		out["applied"] = true
	}
	writeJSON(w, http.StatusOK, out)
}

// reloadInstructions 让宿主重读指令文件(未装配/未实现该可选接口 → 显式错误,不假装成功)。
func (s *Server) reloadInstructions() error {
	if s.ctx == nil {
		return errNoCtxForInstructions
	}
	var sp sdk.SystemPromptService
	if err := s.ctx.Inject("ctx.systemPrompt", &sp); err != nil || sp == nil {
		return errSystemPromptMissing
	}
	rl, ok := sp.(sdk.ReloadableInstructions)
	if !ok {
		return errReloadUnsupported
	}
	return rl.ReloadInstructions()
}
