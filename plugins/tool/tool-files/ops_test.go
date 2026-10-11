package toolfiles

import (
	"encoding/json"
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

// ── 第二轮 review 逮到的五个真缺陷的回归用例(每条都是"改回去就红") ──────────

// TestCopyDoesNotDestroyOnFailure 覆盖语义的关键安全属性:
// overwrite: true 时若复制**失败**,旧目标必须原样还在 —— 一次失败的操作不该
// 把你原本就有的文件赔进去。(上一版在 prepDst 里先删旧目标,复制再失败 → 两头都没。)
//
// 失败注入选**源读不了**(chmod 000),而不是"目标目录只读":后者连删除也会失败,
// 于是"旧文件是否被赔掉"这件事根本测不到(第一版就这么写错了 —— 用例恒绿)。
func TestCopyDoesNotDestroyOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 在 Windows 不产生只读语义(走 ACL),构造不出这个失败")
	}
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	src := filepath.Join(ws, "src.txt")
	dst := filepath.Join(ws, "dst.txt")
	if err := os.WriteFile(src, []byte("new content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(src, 0o644)
	opsFail(t, c, "file_copy", `{"from":"src.txt","to":"dst.txt","overwrite":true}`, "")
	if b, err := os.ReadFile(dst); err != nil || string(b) != "precious" {
		t.Fatalf("复制失败后旧目标必须原样还在: %q(%v)", b, err)
	}
	// 移动:同设备走 rename(只动目录项、不读内容),所以源不可读**不会**让它失败 ——
	// 那是正常且想要的语义(原子替换)。要让"移动失败不能赔掉旧目标"这条可测,
	// 得走**跨设备兜底复制**那条路:注入 rename 失败,复制再失败。
	orig := renameHook
	renameHook = func(_, _ string) error { return &fsErrLink{msg: "invalid cross-device link"} }
	defer func() { renameHook = orig }()
	opsFail(t, c, "file_move", `{"from":"src.txt","to":"dst.txt","overwrite":true}`, "")
	if b, err := os.ReadFile(dst); err != nil || string(b) != "precious" {
		t.Fatalf("跨设备移动失败后旧目标必须原样还在: %q(%v)", b, err)
	}
}

// TestCopySymlinkExplicitlyRejected 符号链接/特殊文件**显式拒**:
// 上一版 os.Open 会跟到链接目标,把目标内容复制成普通文件,再被体积校验逮住,
// 报一句"源 12 字节、落地 34567 字节"这种与事实无关的话(fifo 更糟:直接永久阻塞)。
func TestCopySymlinkExplicitlyRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 建符号链接需要特权,跳过")
	}
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	target := filepath.Join(ws, "target.txt")
	if err := os.WriteFile(target, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("建符号链接失败(沙箱限制?):%v", err)
	}
	opsFail(t, c, "file_copy", `{"from":"link.txt","to":"copy.txt"}`, "符号链接")
	if _, err := os.Stat(filepath.Join(ws, "copy.txt")); !os.IsNotExist(err) {
		t.Fatal("被拒时不该产出任何文件")
	}
	// 目录里含符号链接同样显式拒(不能静默把链接内容拷成普通文件)
	dir := filepath.Join(ws, "tree")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "inner-link")); err != nil {
		t.Skip("目录内符号链接创建失败")
	}
	opsFail(t, c, "file_copy", `{"from":"tree","to":"tree2"}`, "符号链接")
}

// TestCopyDirReplacesInsteadOfMerging 覆盖一个已存在的目录应当是**替换**而不是合并:
// 合并会让目标里"源没有的旧文件"原样留下,而调用方以为自己拿到的是源的一份拷贝。
func TestCopyDirReplacesInsteadOfMerging(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	mk := func(dir string, files map[string]string) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for n, b := range files {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(b), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk(filepath.Join(ws, "src"), map[string]string{"a.txt": "a", "keep.txt": "new"})
	mk(filepath.Join(ws, "dst"), map[string]string{"old-only.txt": "stale", "keep.txt": "old"})
	opsCall(t, c, "file_copy", `{"from":"src","to":"dst","overwrite":true}`)
	if _, err := os.Stat(filepath.Join(ws, "dst", "old-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("覆盖目录后,源里没有的旧文件不该还在(合并语义):%v", err)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "dst", "keep.txt")); err != nil || string(b) != "new" {
		t.Fatalf("同名文件应被覆盖: %q(%v)", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "dst", "a.txt")); err != nil || string(b) != "a" {
		t.Fatalf("源里的新文件应到位: %q(%v)", b, err)
	}
	// 临时目录不留痕
	ents, _ := os.ReadDir(ws)
	for _, e := range ents {
		if strings.Contains(e.Name(), "gah-tmp") {
			t.Fatalf("不该留下临时目录:%s", e.Name())
		}
	}
}

// TestTrashListFiltersByItsOwnProject 按路径筛选回收站必须命中**那个路径自己的分区**。
// 上一版用当前工作根的 key 去比,筛选恒为空的场景极其误导 —— 模型会看到"回收站里什么都
// 没有",而条目明明就在里面(最容易被读成"数据丢了"的一类 bug)。
//
// 判别力来自**两个分区的条目数不同**:从 A 的工作区去筛 B 的路径时,
// 正确实现给 1 条、按"当前工作根"筛的旧实现给 2 条(筛错了分区)。
// 第一版用例只筛自己的分区 —— 那时两个 key 恰好相等,改错了也测不出来。
func TestTrashListFiltersByItsOwnProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GAH_HOME", t.TempDir())
	for _, d := range []string{"proj-a", "proj-b"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a := buildEnv(t, filepath.Join(root, "proj-a"), sdk.SandboxFullAccess)
	b := buildEnv(t, filepath.Join(root, "proj-b"), sdk.SandboxFullAccess)
	// A 删 2 个、B 删 1 个(数量不同才有判别力)
	for _, n := range []string{"a1.txt", "a2.txt"} {
		if err := os.WriteFile(filepath.Join(root, "proj-a", n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "proj-b", "b1.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	opsCall(t, a, "file_delete", `{"paths":["a1.txt","a2.txt"]}`)
	opsCall(t, b, "file_delete", `{"paths":["b1.txt"]}`)

	if got := opsCall(t, a, "file_trash_list", `{}`)["count"].(float64); got != 3 {
		t.Fatalf("不筛选应看到全部 3 条,得 %v", got)
	}
	// 筛自己的分区:2 条
	mine := opsCall(t, a, "file_trash_list", `{"path":"`+filepath.ToSlash(filepath.Join(root, "proj-a"))+`"}`)
	if got := mine["count"].(float64); got != 2 {
		t.Fatalf("按 A 自己的分区筛应得 2 条,得 %v", got)
	}
	// **判别用例**:从 A 去筛 B 的路径 → 必须给 B 的 1 条(旧实现会给 A 的 2 条)
	other := opsCall(t, a, "file_trash_list", `{"path":"`+filepath.ToSlash(filepath.Join(root, "proj-b"))+`"}`)
	if got := other["count"].(float64); got != 1 {
		t.Fatalf("按 B 的分区筛应得 1 条(得 %v 说明用了当前工作根的 key,筛错了分区)", got)
	}
	first := other["entries"].([]any)[0].(map[string]any)
	if !strings.HasSuffix(first["original"].(string), "b1.txt") {
		t.Fatalf("筛出来的应是 B 的条目:%v", first)
	}
}

// TestRestoreRejectsPathsWithSeparators entry 只是条目名:带分隔符/.. 一律拒。
// 上一版接受"回收站内的路径",等于给了模型一条"../ 拼出任意路径再被移动"的口子,
// 而这个动作不经过沙箱裁决(回收站是工具内部区域)。
func TestRestoreRejectsPathsWithSeparators(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("GAH_HOME", t.TempDir())
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	// 用 json.Marshal 构造参数:手写 JSON 字符串在反斜杠那条上极易把"转义地狱"
	// 写进测试本身(第一版就踩了:Go 串 → JSON 串 → 反序列化,三层转义)
	for _, bad := range []string{"../escape", "/abs/path", "a/b", `x\y`, "..", "."} {
		raw, err := json.Marshal(map[string]string{"entry": bad})
		if err != nil {
			t.Fatal(err)
		}
		opsFail(t, c, "file_restore", string(raw), "条目名")
	}
}

// TestGlobPatternsMatchDocumentedForms 描述里推荐的写法必须真的能用。
// 上一版 `**/*.go` 永远不中(path.Match 的 `*` 不跨 `/`),而描述里正写着它 ——
// 模型照文档写、拿到空结果,只会得出"这里没有这类文件"的错误结论。
func TestGlobPatternsMatchDocumentedForms(t *testing.T) {
	ws := t.TempDir()
	c := buildEnv(t, ws, sdk.SandboxFullAccess)
	for _, rel := range []string{"one.go", "a/two.go", "a/b/three.go", "a/b/notes.md"} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		pattern string
		want    int
	}{
		{"*.go", 3},         // 直觉写法:所有 .go(含子目录)
		{"**/*.go", 3},      // 描述里推荐的写法
		{"a/*.go", 1},       // 限定一层
		{"a/**/*.go", 2},    // `**` = 任意深度(与 shell globstar 同义):a/two.go + a/b/three.go
		{"a/b/*.go", 1},     // 精确一层
		{"*.md", 1},         // 另一类扩展名
		{"a/b/notes.md", 1}, // 精确路径
		{"*.rs", 0},         // 真没有
	} {
		out := opsCall(t, c, "file_glob", `{"path":".","pattern":"`+tc.pattern+`"}`)
		if got := out["count"].(float64); int(got) != tc.want {
			t.Fatalf("pattern %q 应命中 %d 条,得 %v(%v)", tc.pattern, tc.want, got, out["matches"])
		}
	}
	// grep 的 glob 过滤同一口径
	out := opsCall(t, c, "file_grep", `{"path":".","pattern":"package","glob":"**/*.go"}`)
	if got := out["count"].(float64); got != 3 {
		t.Fatalf("grep glob=**/*.go 应命中 3 处,得 %v", got)
	}
	// 坏通配符仍然显式报错(展开不能把错误吞掉)
	opsFail(t, c, "file_glob", `{"path":".","pattern":"[a-"}`, "通配符")
}
