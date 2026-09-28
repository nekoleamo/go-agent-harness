// 全局指令端点单测(第八十一批 · 形态 A)。
//
// 判据是"面板改的东西真落到了文件 + 真进了系统提示":所以一条走"未装配 systemPrompt"
// (只写文件 + 如实回 warning),一条走**真实** host-system-prompt 链(写完必须重载,
// 系统提示里得看得见新内容)。
package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/instructions"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// instrView GET 响应解析。
type instrView struct {
	Path     string `json:"path"`
	Text     string `json:"text"`
	Bytes    int    `json:"bytes"`
	Exists   bool   `json:"exists"`
	MaxBytes int    `json:"max_bytes"`
	Over     bool   `json:"over"`
}

func TestInstructionsReadMissing(t *testing.T) {
	s, home := rolesServer(t, false)
	code, body := do(t, s, http.MethodGet, "/api/instructions", "")
	if code != 200 {
		t.Fatalf("缺文件不该是错误: %d %s", code, body)
	}
	var v instrView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	if v.Exists || v.Text != "" || v.Bytes != 0 {
		t.Fatalf("缺文件应回空: %+v", v)
	}
	if v.MaxBytes != instructions.MaxBytes {
		t.Fatalf("上限应由服务端下发: %d != %d", v.MaxBytes, instructions.MaxBytes)
	}
	if v.Path != filepath.Join(home, instructions.FileName) {
		t.Fatalf("路径不对: %s", v.Path)
	}
}

// TestInstructionsSaveWithoutPromptService 未装配 systemPrompt:文件照写,warning 如实说明没生效。
func TestInstructionsSaveWithoutPromptService(t *testing.T) {
	s, home := rolesServer(t, false)
	code, body := do(t, s, http.MethodPut, "/api/instructions", `{"text":"只说结论。\n"}`)
	if code != 200 {
		t.Fatalf("写入应成功: %d %s", code, body)
	}
	var resp struct {
		OK      bool   `json:"ok"`
		Bytes   int    `json:"bytes"`
		Applied bool   `json:"applied"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Applied {
		t.Fatalf("未装配提示服务时不该声称已生效: %+v", resp)
	}
	if !strings.Contains(resp.Warning, "重载失败") {
		t.Fatalf("warning 应说明原因: %q", resp.Warning)
	}
	// 文件真的落了盘(面板说"已写入"就必须真写入)
	raw, err := os.ReadFile(filepath.Join(home, instructions.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "只说结论。\n" {
		t.Fatalf("落盘内容不对: %q", raw)
	}
	// 再 GET:能看回来
	_, body = do(t, s, http.MethodGet, "/api/instructions", "")
	var v instrView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Exists || v.Text != "只说结论。\n" || v.Bytes != len([]byte("只说结论。\n")) {
		t.Fatalf("写后读回不一致: %+v", v)
	}
	// 有 ctx 但缺 host-system-prompt:原因要说得更具体(不是笼统的"重载失败")
	logger := slog.New(slog.DiscardHandler)
	s.ctx = ctx.New(logger, event.New(logger))
	_, body = do(t, s, http.MethodPut, "/api/instructions", `{"text":"x"}`)
	var resp2 struct {
		Applied bool   `json:"applied"`
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal([]byte(body), &resp2); err != nil {
		t.Fatal(err)
	}
	if resp2.Applied || !strings.Contains(resp2.Warning, "ctx.systemPrompt 未装配") {
		t.Fatalf("缺提示服务时应指名道姓: %+v", resp2)
	}
}

// TestInstructionsSaveReloadsPrompt 装配真实提示服务:写完必须重载,系统提示里能看到新指令。
func TestInstructionsSaveReloadsPrompt(t *testing.T) {
	s, _ := rolesServer(t, true)
	marker := "全局标记-第八十一批-abc"
	code, body := do(t, s, http.MethodPut, "/api/instructions", `{"text":"`+marker+`"}`)
	if code != 200 {
		t.Fatalf("写入应成功: %d %s", code, body)
	}
	if !strings.Contains(body, `"applied":true`) {
		t.Fatalf("装配提示服务时应报已生效: %s", body)
	}
	// 提示服务里真的换了内容(不是只写了文件)
	var sp sdk.SystemPromptService
	if err := s.ctx.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	msgs := sp.Assemble(nil, nil)
	if len(msgs) == 0 || !strings.Contains(msgs[0].Content, marker) {
		t.Fatalf("系统提示未包含新全局指令(重载没做?): %d 段", len(msgs))
	}
}

// TestInstructionsSaveRejectsOverMax 超上限:400 + 文件不被写坏(旧内容原样保留)。
func TestInstructionsSaveRejectsOverMax(t *testing.T) {
	s, home := rolesServer(t, false)
	if code, body := do(t, s, http.MethodPut, "/api/instructions", `{"text":"原内容"}`); code != 200 {
		t.Fatalf("预置写入失败: %d %s", code, body)
	}
	big := strings.Repeat("a", instructions.MaxBytes+1)
	code, body := do(t, s, http.MethodPut, "/api/instructions", `{"text":"`+big+`"}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "超上限") {
		t.Fatalf("超限应 400 且说明原因: %d %s", code, strings.TrimSpace(body))
	}
	if raw, _ := os.ReadFile(filepath.Join(home, instructions.FileName)); string(raw) != "原内容" {
		t.Fatalf("超限失败不该改动文件: %q", raw)
	}
	// 手改出的超限文件:GET 要如实标 over(能看,保存会被拒)
	if err := instructions.Write(strings.Repeat("b", instructions.MaxBytes-1)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, instructions.FileName), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	_, body = do(t, s, http.MethodGet, "/api/instructions", "")
	var v instrView
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Over || !v.Exists {
		t.Fatalf("超限文件应标 over=true 且仍可读: %+v", v)
	}
}

// TestInstructionsBadBody 坏请求体:400(不 panic、不写盘)。
func TestInstructionsBadBody(t *testing.T) {
	s, home := rolesServer(t, false)
	if code, _ := do(t, s, http.MethodPut, "/api/instructions", `{`); code != http.StatusBadRequest {
		t.Fatalf("坏请求体应 400,got %d", code)
	}
	if instructions.Exists() {
		t.Fatalf("坏请求体不该写盘: %s", home)
	}
}
