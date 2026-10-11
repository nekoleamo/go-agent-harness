package toolfiles

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// F1 文件操作(M1)用例。既有四件套用例在 files_test.go / audit_test.go。
//
// 三处最容易出错、因此必须有用例钉住的点:
//   - **跨卷移动**(D3):单测无法造真 EXDEV,但可以造"目标目录不存在/rename 失败"来验证兜底路径;
//     真 EXDEV 由真机(N5)验,这里用可注入的 rename 钩子模拟。
//   - **默认不覆盖**(D2):dst 已存在必须报错,除非 overwrite: true。
//   - **删除可恢复**:删了必须能从 file_trash_list 查到、file_restore 恢复回原路径。

// opsCall 调一个 F1 动作并断言没有 error 字段。
func opsCall(t *testing.T, c sdk.Ctx, name, args string) map[string]any {
	t.Helper()
	out, err := call(t, c, name, args)
	if err != nil {
		t.Fatalf("%s 调用失败: %v", name, err)
	}
	if e, ok := out["error"]; ok {
		t.Fatalf("%s 意外报错: %v(结果 %v)", name, e, out)
	}
	return out
}

// opsFail 调一个 F1 动作并断言 error 字段含子串(错误文案要给模型可行动的出路)。
func opsFail(t *testing.T, c sdk.Ctx, name, args, want string) {
	t.Helper()
	out, err := call(t, c, name, args)
	if err != nil {
		t.Fatalf("%s 调用失败: %v", name, err)
	}
	e, ok := out["error"].(string)
	if !ok {
		t.Fatalf("%s 应返回 error,得 %v", name, out)
	}
	if want != "" && !strings.Contains(e, want) {
		t.Fatalf("%s 的错误文案应含 %q,得 %q", name, want, e)
	}
}

func TestOpsListPagination(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(ws, string(rune('a'+i))+".txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := opsCall(t, c, "file_list", `{"path":".","limit":2}`)
	if got := out["count"].(float64); got != 2 {
		t.Fatalf("count = %v, want 2", got)
	}
	if out["next_cursor"] != "2" {
		t.Fatalf("next_cursor = %v, want \"2\"", out["next_cursor"])
	}
	if _, ok := out["note"]; !ok {
		t.Fatal("截断时应如实说明还有更多")
	}
	out = opsCall(t, c, "file_list", `{"path":".","limit":2,"cursor":"2"}`)
	if out["next_cursor"] != "4" {
		t.Fatalf("第二页 next_cursor = %v, want \"4\"", out["next_cursor"])
	}
	opsFail(t, c, "file_list", `{"path":".","cursor":"999"}`, "越界")
}

func TestOpsListRejectsEscape(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxWorkspace) // workspace-write:读也限区内
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 越界读必须被拒(裁决链与 file_read 同源)
	opsFail(t, c, "file_list", `{"path":"`+filepath.ToSlash(outside)+`"}`, "workspace")
}

func TestOpsGlobAndGrep(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	write := func(rel, content string) {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a/one.go", "package a\nfunc One() {}\n")
	write("a/b/two.go", "package b\nfunc Two() {}\n")
	write("a/b/notes.md", "# notes\nfunc One in md\n")

	out := opsCall(t, c, "file_glob", `{"path":".","pattern":"*.go"}`)
	if got := out["count"].(float64); got != 2 {
		t.Fatalf("glob 应命中 2 个 .go,得 %v(%v)", got, out["matches"])
	}
	// 通配符本身要校验(错模式要有可行动文案,不是空结果)
	opsFail(t, c, "file_glob", `{"path":".","pattern":"[a-"}`, "通配符")

	out = opsCall(t, c, "file_grep", `{"path":".","pattern":"func\\s+One"}`)
	if got := out["count"].(float64); got != 2 {
		t.Fatalf("grep 应命中 2 处,得 %v", got)
	}
	if out["files_scanned"].(float64) != 3 {
		t.Fatalf("files_scanned = %v, want 3", out["files_scanned"])
	}
	// glob 过滤
	out = opsCall(t, c, "file_grep", `{"path":".","pattern":"func","glob":"*.md"}`)
	if got := out["count"].(float64); got != 1 {
		t.Fatalf("glob=*.md 应只剩 1 处,得 %v", got)
	}
	// 无效正则要报错而不是"零命中"
	opsFail(t, c, "file_grep", `{"path":".","pattern":"func("}`, "正则")
	// 大文件跳过要如实报数
	big := filepath.Join(ws, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", (2<<20)+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	out = opsCall(t, c, "file_grep", `{"path":".","pattern":"xxxx"}`)
	if out["skipped_large"].(float64) != 1 {
		t.Fatalf("应如实报出跳过的大文件数: %v", out)
	}
}

func TestOpsMkdir(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	opsCall(t, c, "file_mkdir", `{"path":"a/b/c"}`)
	if fi, err := os.Stat(filepath.Join(ws, "a", "b", "c")); err != nil || !fi.IsDir() {
		t.Fatalf("应已建目录: %v", err)
	}
	// 已存在要报错(不静默"成功")
	opsFail(t, c, "file_mkdir", `{"path":"a/b/c"}`, "已存在")
	// parents: false 时父目录不存在要给出路
	opsFail(t, c, "file_mkdir", `{"path":"x/y","parents":false}`, "父目录不存在")
}

func TestOpsMoveCopyRenameAndOverwrite(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	src := filepath.Join(ws, "src.txt")
	dst := filepath.Join(ws, "dst.txt")
	existing := filepath.Join(ws, "existing.txt")
	for p, content := range map[string]string{src: "hello", dst: "world", existing: "old"} {
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 默认不覆盖
	opsFail(t, c, "file_copy", `{"from":"src.txt","to":"dst.txt"}`, "目标已存在")
	opsFail(t, c, "file_move", `{"from":"src.txt","to":"dst.txt"}`, "目标已存在")
	// overwrite 才覆盖
	opsCall(t, c, "file_copy", `{"from":"src.txt","to":"dst.txt","overwrite":true}`)
	if b, err := os.ReadFile(dst); err != nil || string(b) != "hello" {
		t.Fatalf("覆盖后内容应为 hello: %q(%v)", b, err)
	}
	// 移动后源消失、目标内容一致
	opsCall(t, c, "file_move", `{"from":"src.txt","to":"moved.txt"}`)
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("移动后源应不存在: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "moved.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("移动后目标内容应为 hello: %q(%v)", b, err)
	}
	// 重命名:只改名字,同目录
	opsCall(t, c, "file_rename", `{"path":"moved.txt","name":"中文名.txt"}`)
	if _, err := os.Stat(filepath.Join(ws, "中文名.txt")); err != nil {
		t.Fatalf("重命名后应存在: %v", err)
	}
	// name 不许带路径(否则就是 move,口径要清楚)
	opsFail(t, c, "file_rename", `{"path":"中文名.txt","name":"sub/x.txt"}`, "分隔符")
	// 同名默认拒绝
	opsCall(t, c, "file_copy", `{"from":"existing.txt","to":"dup.txt"}`)
	opsFail(t, c, "file_rename", `{"path":"中文名.txt","name":"dup.txt"}`, "目标已存在")
	// 不存在的源要给出路
	opsFail(t, c, "file_copy", `{"from":"nope.txt","to":"x.txt"}`, "源不存在")
}

// TestOpsCopyDirRecursively 目录复制要递归(手机/桌面都会遇到整目录搬移)。
func TestOpsCopyDirRecursively(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	src := filepath.Join(ws, "tree")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "deep.txt"), []byte("deep"), 0o644); err != nil {
		t.Fatal(err)
	}
	opsCall(t, c, "file_copy", `{"from":"tree","to":"tree2"}`)
	if b, err := os.ReadFile(filepath.Join(ws, "tree2", "sub", "deep.txt")); err != nil || string(b) != "deep" {
		t.Fatalf("递归复制后内容应为 deep: %q(%v)", b, err)
	}
}

// TestMoveAnyFallsBackToCopy D3 的核心:rename 失败(EXDEV/只读目标等)必须走
// copy+fsync+校验+删源,**绝不两头都没**。这里用注入的 rename 钩子模拟跨卷。
func TestMoveAnyFallsBackToCopy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("注入钩子的模拟路径与 Windows 的 rename 语义无关")
	}
	base := t.TempDir()
	src := filepath.Join(base, "src.bin")
	dst := filepath.Join(base, "dst.bin")
	body := []byte("cross-device payload")
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatal(err)
	}
	orig := renameHook
	renameHook = func(_, _ string) error { return &fsErrLink{msg: "invalid cross-device link"} }
	defer func() { renameHook = orig }()

	cross, err := moveAny(src, dst)
	if err != nil {
		t.Fatalf("跨卷移动应成功(走复制兜底): %v", err)
	}
	if !cross {
		t.Fatal("cross 应为 true(走的是复制路径,工具结果要如实告诉模型)")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("复制成功后源应已删除: %v", err)
	}
	if b, err := os.ReadFile(dst); err != nil || string(b) != string(body) {
		t.Fatalf("目标内容应与源一致: %q(%v)", b, err)
	}
	// 目标目录里不该留下临时文件
	ents, _ := os.ReadDir(base)
	for _, e := range ents {
		if strings.Contains(e.Name(), "gah-tmp") {
			t.Fatalf("不该留下临时文件: %s", e.Name())
		}
	}
}

// fsErrLink 模拟 EXDEV(link 跨设备失败);errors.Is(err, syscall.EXDEV) 为真。
type fsErrLink struct{ msg string }

func (e *fsErrLink) Error() string { return e.msg }

func TestOpsDeleteToTrashAndRestore(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("GAH_HOME", t.TempDir()) // 回收站落数据根(便携纪律)
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	victim := filepath.Join(ws, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := opsCall(t, c, "file_delete", `{"paths":["victim.txt"]}`)
	first0 := out["results"].([]any)[0].(map[string]any)
	if first0["deleted"] != "trash" {
		t.Fatalf("默认删除应进回收站,得 %v", out)
	}
	if _, err := os.Stat(victim); !os.IsNotExist(err) {
		t.Fatalf("原文件应已移走: %v", err)
	}
	list := opsCall(t, c, "file_trash_list", `{}`)
	if list["count"].(float64) != 1 {
		t.Fatalf("回收站应有 1 条,得 %v", list)
	}
	entry, _ := list["entries"].([]any)
	first := entry[0].(map[string]any)
	if first["original"] != victim {
		t.Fatalf("回收站条目应记原路径 %q,得 %v", victim, first["original"])
	}
	name := first["entry"].(string)
	opsCall(t, c, "file_restore", `{"entry":"`+name+`"}`)
	if b, err := os.ReadFile(victim); err != nil || string(b) != "keep me" {
		t.Fatalf("恢复后内容应回到原处: %q(%v)", b, err)
	}
	// permanent:true 才是真删,且不进回收站
	opsCall(t, c, "file_delete", `{"paths":["victim.txt"],"permanent":true}`)
	list = opsCall(t, c, "file_trash_list", `{}`)
	if list["count"].(float64) != 0 {
		t.Fatalf("永久删除不该进回收站: %v", list)
	}
	// 批量逐条报结果:一半不存在也要把成功的那条做完
	if err := os.WriteFile(filepath.Join(ws, "b1.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	out = opsCall(t, c, "file_delete", `{"paths":["b1.txt","missing.txt"]}`)
	if out["failed"].(float64) != 1 {
		t.Fatalf("应报出 1 条失败: %v", out)
	}
	if _, err := os.Stat(filepath.Join(ws, "b1.txt")); !os.IsNotExist(err) {
		t.Fatalf("批量里成功的那条应已生效: %v", err)
	}
	// 恢复不存在的条目要给出路
	opsFail(t, c, "file_restore", `{"entry":"nope"}`, "没有条目")
}

func TestOpsTrashPruneKeepsRecent(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("GAH_HOME", t.TempDir())
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	for i := 0; i < trashKeep+3; i++ {
		p := filepath.Join(ws, "f"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		opsCall(t, c, "file_delete", `{"paths":["`+filepath.Base(p)+`"]}`)
	}
	list := opsCall(t, c, "file_trash_list", `{}`)
	if got := list["count"].(float64); got != trashKeep {
		t.Fatalf("回收站应只保留最近 %d 份,得 %v", trashKeep, got)
	}
}

// TestOpsWriteOutsideWorkspaceRejected 写目标裁决不可漏:所有写动作都要过沙箱。
func TestOpsWriteOutsideWorkspaceRejected(t *testing.T) {
	ws := t.TempDir()
	outside := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxWorkspace)
	outAbs := filepath.ToSlash(outside)
	for _, tc := range []struct{ tool, args string }{
		{"file_mkdir", `{"path":"` + outAbs + `/newdir"}`},
		{"file_move", `{"from":"a.txt","to":"` + outAbs + `/a.txt"}`},
		{"file_copy", `{"from":"a.txt","to":"` + outAbs + `/a.txt"}`},
		{"file_rename", `{"path":"` + outAbs + `/a.txt","name":"b.txt"}`},
	} {
		opsFail(t, c, tc.tool, tc.args, "workspace")
	}
	// 批量删除按条给错(整体成功、逐条 error),这里查它的逐条结果
	out, _ := call(t, c, "file_delete", `{"paths":["`+outAbs+`/a.txt"]}`)
	if out["failed"].(float64) != 1 {
		t.Fatalf("越界删除应记为失败: %v", out)
	}
	item := out["results"].([]any)[0].(map[string]any)
	if e, _ := item["error"].(string); !strings.Contains(e, "workspace") {
		t.Fatalf("逐条错误文案应说明被沙箱拒绝: %v", item)
	}
	// read-only 档下连区内写都要拒
	ro := buildEnv(t, ws, sdk.SandboxReadOnly)
	opsFail(t, ro, "file_mkdir", `{"path":"nope"}`, "")
}

// TestOpsDefinitionsDeclarePathParams 每个 F1 动作都必须显式声明路径参数
// (声明优先;漏声明 = 宿主 pre-execute 少一次裁决)。
func TestOpsDefinitionsDeclarePathParams(t *testing.T) {
	f := &FilesTool{}
	for _, n := range opsNames {
		tool := &fileTool{name: n, f: f, schema: opsSchema(n)}
		d := tool.Definition()
		if d.Name != n {
			t.Fatalf("定义名 = %q, want %q", d.Name, n)
		}
		if d.Description == "" {
			t.Fatalf("%s 缺描述(模型看不到上限与破坏性语义)", n)
		}
		if len(d.PathParams) == 0 {
			t.Fatalf("%s 未声明 PathParams", n)
		}
		for _, p := range d.PathParams {
			if p.Access != sdk.PathRead && p.Access != sdk.PathWrite {
				t.Fatalf("%s 的 %s 访问口径非法: %q", n, p.Arg, p.Access)
			}
		}
	}
	// 外部进程表也要含全部 F1 工具(默认发行态是外部化的,漏一个等于桌面端没有)
	tools := NewTools()
	for _, n := range opsNames {
		if tools[n] == nil {
			t.Fatalf("外部化工具表缺 %s", n)
		}
	}
}
