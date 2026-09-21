// A-1 条目 43(跨渠道提问提示,G-E5-4)的 pty 真机验收。
//
// 手法:**同进程同时开 TUI + Web**(profile 引 confirm-fusion bundle,host-confirm-fusion
// 统一 Provide ctx.confirm/ctx.question 并广播两端;两 UI 插件检测到 fusion 只注册呈现者)。
// 提问在 TUI 呈现,作答从 **Web 渠道**回传 → 验 TUI 侧"已由其它渠道处理"提示 + 该作答
// 真的回填到工具/回合(模型可继续)。
package tests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// freePort 取一个空闲本机端口(避免与真机 2233 冲突)。
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// walkStr 在任意 JSON 结构里递归找第一个 key 的字符串值。
func walkStr(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x[key].(string); ok && s != "" {
			return s
		}
		for _, c := range x {
			if s := walkStr(c, key); s != "" {
				return s
			}
		}
	case []any:
		for _, c := range x {
			if s := walkStr(c, key); s != "" {
				return s
			}
		}
	}
	return ""
}

// watchQuestion 连 Web SSE(/api/events),抓到含 want 的 question 帧时把 question.id 传出。
func watchQuestion(ctx context.Context, port int, want string) <-chan string {
	out := make(chan string, 1)
	go func() {
		for ctx.Err() == nil {
			req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/api/events", port), nil)
			if err != nil {
				return
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			sc := bufio.NewScanner(resp.Body)
			sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
			for sc.Scan() {
				line := sc.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var f struct {
					Type    string `json:"type"`
					Payload any    `json:"payload"`
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f) != nil {
					continue
				}
				if !strings.Contains(f.Type, "question") {
					continue
				}
				if !strings.Contains(fmt.Sprintf("%v", f.Payload), want) {
					continue
				}
				if id := walkStr(f.Payload, "id"); id != "" {
					select {
					case out <- id:
					default:
					}
				}
			}
			resp.Body.Close()
			time.Sleep(300 * time.Millisecond)
		}
	}()
	return out
}

// TestTUIAcceptCrossChannelQuestion 条目 43:同进程 TUI+Web 并存,Web 作答, TUI 提示跨渠道。
func TestTUIAcceptCrossChannelQuestion(t *testing.T) {
	bin := buildGahCurrent(t)
	root := probeDataDir(t, bin)
	cfg := filepath.Join(root, "config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	writeAcceptFile(t, cfg, "provider.yaml",
		"active: mock\nproviders:\n  - name: mock\n    base_url: http://127.0.0.1:9/v1\n    api_key: k\n    model: mock-model\n")
	writeAcceptFile(t, cfg, "patch-accx.yaml", fmt.Sprintf(`entries:
  - id: llm-openai-compat
    enabled: false
  - id: llm-mock
    enabled: true
    data:
      script: '[{"tool":{"name":"ask_user_question","args":"{\"prompt\":\"跨渠道提问:选一个\",\"options\":[{\"value\":\"甲\",\"desc\":\"选项甲\"},{\"value\":\"乙\"}]}"}},{"text":"跨渠道回答已生效。"}]'
  - id: ui-web-app
    enabled: true
    data:
      addr: 127.0.0.1:%d
      open_browser: false
`, port))
	writeAcceptFile(t, cfg, "profile-accx.yaml",
		"name: accx\nbundles:\n  - base\n  - tui\n  - web\n  - confirm-fusion\npatches:\n  - patch-accx.yaml\n")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ids := watchQuestion(ctx, port, "跨渠道提问")

	s := newTuiSess(t, bin, probeEnv(), "--profile", "accx")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("请提问\r")
	if !s.waitScreen("跨渠道提问:选一个", 45*time.Second) {
		t.Fatalf("条目 43:TUI 未呈现提问;屏尾 %q", firstN(tailS(s.screen(), 400), 400))
	}
	t.Logf("条目 43:TUI 侧提问已呈现;待答行=%q", s.statusLineWith("待答"))

	var id string
	select {
	case id = <-ids:
	case <-time.After(40 * time.Second):
		t.Fatalf("条目 43:Web 渠道未收到提问事件(SSE);屏尾 %q", firstN(tailS(s.screen(), 400), 400))
	}
	t.Logf("条目 43:Web 侧拿到同一提问 id=%s", id)

	body, _ := json.Marshal(map[string]any{"id": id, "values": []string{"甲"}})
	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/api/question", port), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("条目 43:Web 作答请求失败: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("条目 43:Web 作答接口状态 %d", resp.StatusCode)
	}

	if !s.waitScreen("提问已由其它渠道(web)处理", 40*time.Second) {
		t.Errorf("条目 43:TUI 未提示「提问已由其它渠道(web)处理」;屏尾 %q", firstN(tailS(s.screen(), 500), 500))
	}
	if !s.waitScreen("跨渠道回答已生效", 60*time.Second) {
		t.Errorf("条目 43:跨渠道作答未回填到工具(回合未继续);屏尾 %q", firstN(tailS(s.screen(), 500), 500))
	}
}
