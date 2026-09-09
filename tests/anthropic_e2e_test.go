package tests

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestAnthropicRouteE2E claude 模型回合走 Anthropic 适配器(模型前缀路由端到端):
// mock Messages 服务 → host-llm 按模型名 claude-* 路由到 llm-anthropic-compat。
func TestAnthropicRouteE2E(t *testing.T) {
	// mock Anthropic Messages 端点:文本流,记录请求模型名
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			http.Error(w, "bad path", 404)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(b, &req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n"))
		w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"claude-round-ok\"}}\n\n"))
		w.Write([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"))
		w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	t.Cleanup(srv.Close)

	tree := config.NewTree()
	// 仅装配与 LLM 回合相关的插件(不启 openai/mock;anthropic 指向 mock)
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "llm-anthropic-compat", Data: map[string]any{"base_url": srv.URL, "model": "claude-sonnet-4-5"}},
		{ID: "host-agent-loop"},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "workspace-write", "sync": true}},
		{ID: "llm-openai-compat", Enabled: func() *bool { f := false; return &f }()},
		{ID: "llm-mock", Enabled: func() *bool { f := false; return &f }()},
	})

	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := base.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reg.DisposeAll)

	// 当前模型设为 claude(路由触发)
	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		t.Fatal(err)
	}
	llm.SetModel("claude-sonnet-4-5")

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	// 无工具调用:mock 流直接给文本收尾
	if err := loop.Run(context.Background(), "测试 Anthropic 支持"); err != nil {
		t.Fatalf("回合失败: %v", err)
	}
	if gotModel != "claude-sonnet-4-5" {
		t.Fatalf("请求应使用 claude 模型名,got %q", gotModel)
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	msgs := sessions.DeriveMessages()
	var last string
	for _, m := range msgs {
		if m.Role == sdk.RoleAssistant {
			last = m.Content
		}
	}
	if !strings.Contains(last, "claude-round-ok") {
		t.Fatalf("回合应收到 Anthropic 文本: %q", last)
	}
}
