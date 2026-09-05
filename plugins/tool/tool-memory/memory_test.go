// tool-memory 单测:存储三特性(追加 jsonl/坏行容忍/人工可编辑)+ 全 action 分发 + 并发。
package toolmemory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// newStore 临时目录注入 root(不依赖 GAH_HOME/env)。
func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

// entriesOf 读取文件全部行并解析(校验落盘内容)。
func entriesOf(t *testing.T, path string) []Entry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []Entry
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("落盘行应可解析: %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

// TestRememberListLifecycle remember → list 可见(最新在前)→ forget 墓碑后不可见。
func TestRememberListLifecycle(t *testing.T) {
	s := newStore(t)
	ids := make([]string, 0, 3)
	for _, txt := range []string{"决策:沙箱 mode 由 data 配置驱动", "踩坑:滚轮风暴是 bubbletea 鼠标转发死循环", "偏好:中文回复"} {
		e, err := s.Remember(txt, []string{"go"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, e.ID)
	}
	got := s.List(0)
	if len(got) != 3 {
		t.Fatalf("应 3 条: %d", len(got))
	}
	// 倒序:最新在前
	if got[0].ID != ids[2] || got[2].ID != ids[0] {
		t.Fatalf("list 应按时间倒序: %v", got)
	}
	if err := s.Forget(ids[1]); err != nil {
		t.Fatal(err)
	}
	got = s.List(0)
	if len(got) != 2 {
		t.Fatalf("forget 后应剩 2 条: %v", got)
	}
	for _, e := range got {
		if e.ID == ids[1] {
			t.Fatal("forget 的记忆不应再出现在 list")
		}
	}
	// Forget 幂等:不存在 id 不报错
	if err := s.Forget("no-such-id"); err != nil {
		t.Fatalf("forget 不存在 id 应幂等: %v", err)
	}
}

// TestRecallTextAndTag recall 大小写不敏感,命中 text 或 tags;无命中返回空。
func TestRecallTextAndTag(t *testing.T) {
	s := newStore(t)
	if _, err := s.Remember("Go 适配器流式聚合丢参", []string{"openai", "bug"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remember("kitty 真机验证", []string{"tui"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Recall("流式", 0); len(got) != 1 || !strings.Contains(got[0].Text, "流式") {
		t.Fatalf("text 子串命中失败: %v", got)
	}
	if got := s.Recall("OPENAI", 0); len(got) != 1 {
		t.Fatalf("tags 命中应大小写不敏感: %v", got)
	}
	if got := s.Recall("不存在词", 0); len(got) != 0 {
		t.Fatalf("无命中应为空: %v", got)
	}
	// limit 生效
	if got := s.Recall("适配", 1); len(got) != 1 {
		t.Fatalf("limit 应生效: %d", len(got))
	}
}

// TestBadLineTolerance 坏行(乱码/手工注释)容忍跳过,有效行不受影响。
func TestBadLineTolerance(t *testing.T) {
	s := newStore(t)
	if _, err := s.Remember("有效记忆", nil); err != nil {
		t.Fatal(err)
	}
	// 人工追加坏行 + 手工注释行(人工可编辑特性:直接删行/加注释)
	path := filepath.Join(s.root, sdk.ProjectKeyFromCwd()+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "{broken json")
	fmt.Fprintln(f, "# 手工注释:这条人工写的说明应被容忍")
	f.Close()

	got := s.List(0)
	if len(got) != 1 || got[0].Text != "有效记忆" {
		t.Fatalf("坏行/注释行应被跳过,有效记忆保留: %v", got)
	}
}

// TestConcurrentRemember 并发追加:唯一 id、全部落盘、无损坏。
func TestConcurrentRemember(t *testing.T) {
	s := newStore(t)
	const n = 30
	var wg sync.WaitGroup
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e, err := s.Remember(fmt.Sprintf("并发条目 %d", i), nil)
			if err != nil {
				t.Errorf("并发 remember 失败: %v", err)
				return
			}
			ids[i] = e.ID
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			t.Fatalf("并发下 id 应唯一且非空: %v", ids)
		}
		seen[id] = true
	}
	if got := s.List(50); len(got) != n {
		t.Fatalf("应全部落盘 %d 条: %d", n, len(got))
	}
}

// TestToolExecute 工具面 action 分发与参数校验。
func TestToolExecute(t *testing.T) {
	tl := &Tool{store: newStore(t)}

	// remember:缺 text 报业务错误;带 tags 成功返回 id
	if out, _ := tl.Execute(nil, `{"action":"remember"}`); out == nil {
		t.Fatal("remember 缺 text 应返回业务错误")
	} else if m, ok := out.(map[string]any); !ok || m["error"] == nil {
		t.Fatalf("缺 text 应 error: %v", out)
	}
	out, err := tl.Execute(nil, `{"action":"remember","text":"记录一条测试","tags":["t"]}`)
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["id"].(string)

	// recall / list / forget / 未知 action
	if out, _ := tl.Execute(nil, `{"action":"recall","query":"测试"}`); len(out.([]Entry)) != 1 {
		t.Fatalf("recall 应命中 1 条: %v", out)
	}
	if out, _ := tl.Execute(nil, `{"action":"list"}`); len(out.([]Entry)) != 1 {
		t.Fatalf("list 应 1 条: %v", out)
	}
	if out, _ := tl.Execute(nil, `{"action":"forget","id":"`+id+`"}`); out.(map[string]any)["forgotten"] != id {
		t.Fatalf("forget 应返回 id: %v", out)
	}
	if out, _ := tl.Execute(nil, `{"action":"recall","query":"测试"}`); len(out.([]Entry)) != 0 {
		t.Fatalf("forget 后 recall 应为空: %v", out)
	}
	if out, _ := tl.Execute(nil, `{"action":"noop"}`); out == nil {
		t.Fatal("未知 action 应返回业务错误")
	}
	// Definition:工具名与 action 枚举
	def := tl.Definition()
	if def.Name != "memory" {
		t.Fatalf("工具名应为 memory: %s", def.Name)
	}
	if !strings.Contains(def.Description, "remember") {
		t.Fatal("描述应引导 action 用法")
	}
}
