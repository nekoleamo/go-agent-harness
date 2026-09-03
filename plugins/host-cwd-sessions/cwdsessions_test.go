// 项目 key 派生与会话目录列表测试。
package hostcwdsessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectKey(t *testing.T) {
	cases := map[string]string{
		"/Users/nekoleamo/Documents/Working/go-agent-harness": "Users-nekoleamo-Documents-Working-go-agent-harness",
		`C:\Users\dev\proj`: "C--Users-dev-proj", // 对齐 dsc 风格:C:\ → C--...

		"/":         "default",
		"/tmp/a/b/": "tmp-a-b",
	}
	for in, want := range cases {
		if got := ProjectKey(in); got != want {
			t.Errorf("ProjectKey(%s) = %q,want %q", in, got, want)
		}
	}
}

func TestServiceListEmpty(t *testing.T) {
	tmp := t.TempDir()
	// 无 sessions 目录 → 空列表
	service := &Service{key: "k", path: filepath.Join(tmp, "k.jsonl")}
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	if list := service.List(); list != nil {
		t.Fatalf("无目录应为空,got %v", list)
	}
}

func TestServiceListWithSession(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// 造两个会话文件
	os.WriteFile(filepath.Join(root, "key-a.jsonl"), []byte("line1\n"), 0o644)
	os.WriteFile(filepath.Join(root, "key-b.jsonl"), []byte("line1\n"), 0o644)
	os.WriteFile(filepath.Join(root, "ignored.txt"), []byte("x"), 0o644)
	if err := os.Setenv("GAH_HOME", tmp); err != nil {
		t.Fatal(err)
	}
	service := &Service{key: "key-a", path: filepath.Join(root, "key-a.jsonl")}
	list := service.List()
	if len(list) != 2 || list[0] != "key-a" || list[1] != "key-b" {
		t.Fatalf("列表应含排序后的两个会话,got %v", list)
	}
	if service.Current() != "key-a" || !strings.HasSuffix(service.Path(), "key-a.jsonl") {
		t.Fatalf("Current/Path 不符: %s %s", service.Current(), service.Path())
	}
}
