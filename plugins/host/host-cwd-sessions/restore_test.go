package hostcwdsessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestStartDirIsMeaningless 钉住「哪些启动目录会被当成无意义」。
//
// 这条判定的两面都有代价:判得太宽 → 用户在 home 下的项目目录里启动也被劫持;
// 判得太窄 → 重开还是回到 home(2026-10-05 用户反馈的原症状)。
func TestStartDirIsMeaningless(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", filepath.Join(home, "gah-data"))
	realHome, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("取不到用户 home:%v", err)
	}

	// 无意义:数据根本身与子目录、用户 home、文件系统根
	if !startDirIsMeaningless("") {
		t.Fatal("空目录应判为无意义")
	}
	if !startDirIsMeaningless(sdk.Home()) {
		t.Fatal("数据根应判为无意义")
	}
	if !startDirIsMeaningless(filepath.Join(sdk.Home(), "sessions")) {
		t.Fatal("数据根子目录应判为无意义(否则会把会话日志当工作区)")
	}
	if !startDirIsMeaningless(realHome) {
		t.Fatal("用户 home 应判为无意义")
	}
	if !startDirIsMeaningless(string(filepath.Separator)) {
		t.Fatal("文件系统根应判为无意义")
	}
	if !startDirIsMeaningless(filepath.Join(realHome, "..", filepath.Base(realHome))) {
		t.Fatal("未清洗的 home 形态也应判为无意义")
	}

	// 有意义:普通项目目录、home 下的项目目录、数据根**同级**的目录
	proj := filepath.Join(home, "proj")
	if startDirIsMeaningless(proj) {
		t.Fatal("普通项目目录不该判为无意义")
	}
	if startDirIsMeaningless(filepath.Join(realHome, "code", "my-app")) {
		t.Fatal("home 下的项目目录是明确意图,不该被劫持")
	}
	sibling := filepath.Dir(sdk.Home())
	if sibling != sdk.Home() && startDirIsMeaningless(sibling) {
		t.Fatalf("数据根同级目录不该判为无意义:%s", sibling)
	}
}

// TestRestoreLastWorkspaceSkipsMeaninglessDir 恢复逻辑的基本盘:
// 无意义 cwd + 有历史 → 切过去;有意义 cwd → 一个字节都不动。
func TestRestoreLastWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", filepath.Join(home, "gah-data"))
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := &Service{}
	svc.recordProject(ProjectKey(proj), proj)

	// 有意义的 cwd:不恢复
	t.Chdir(proj)
	if _, ok := restoreLastWorkspace(svc, currentDir()); ok {
		t.Fatal("从项目目录启动不该被劫持成历史里的目录")
	}
	if got := currentDir(); got != proj {
		t.Fatalf("cwd 被改了:%s", got)
	}

	// 无意义的 cwd(数据根):恢复到历史里那一个
	t.Chdir(sdk.Home())
	dir, ok := restoreLastWorkspace(svc, currentDir())
	if !ok {
		t.Fatal("从数据根启动应恢复到上次打开的目录")
	}
	if filepath.Clean(dir) != filepath.Clean(proj) {
		t.Fatalf("恢复到 %q,期望 %q", dir, proj)
	}
	// SwitchDir 会先 EvalSymlinks 归一(macOS 上 /var → /private/var),断言要比归一后的值
	want, err := filepath.EvalSymlinks(proj)
	if err != nil {
		want = proj
	}
	if got := currentDir(); filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("cwd 应已切到 %q,现为 %q", want, got)
	}
}

// TestStartDirIsMeaninglessSystemDirs 系统目录两个平台口径都要能拦住。
//
// 它们在真实启动里都出现过(Windows 计划任务/桌面快捷方式给的是 C:\Windows;
// macOS 上经 /System 与 /usr 启动的也不少见),而漏掉的后果是「重开回到系统目录」——
// 比回到 home 更糟,因为那还允许 agent 在那儿写文件。
func TestStartDirIsMeaninglessSystemDirs(t *testing.T) {
	t.Setenv("GAH_HOME", filepath.Join(t.TempDir(), "gah-data"))
	t.Setenv("SystemRoot", filepath.Join(t.TempDir(), "Windows"))
	if !startDirIsMeaningless(os.Getenv("SystemRoot")) {
		t.Fatal("Windows 的 %SystemRoot% 应判为无意义")
	}
	// SystemRoot 只在 Windows 才有;非 Windows 上置了也不该改变别的判定
	if !startDirIsMeaningless("/System/Library/Extensions") {
		t.Fatal("macOS 的 /System/** 应判为无意义")
	}
	if !startDirIsMeaningless("/usr") {
		t.Fatal("/usr 本身应判为无意义")
	}
	// **反向钉**: /usr 下的项目目录(容器与 CI 里很常见:/usr/src/app、/usr/local/src/x)
	// 是真实工作目录,通配拦截会把用户的项目从启动落点上劫持走。
	if startDirIsMeaningless("/usr/src/app") {
		t.Fatal("/usr 下的项目目录是明确意图,不该判为无意义")
	}
	if startDirIsMeaningless("/usr/local/share/gah-proj") {
		t.Fatal("/usr/local 下的项目目录不该判为无意义")
	}
}

// TestRestoreLastWorkspaceSkipsUnusableRecords 历史里的空目录与「当前目录本身」要跳过,
// 而不是被当成落点。空目录来自手改 workspaces.json 或旧版本残留 —— 它不该拦住启动,
// 也不该把会话开到一个不存在的地方。
func TestRestoreLastWorkspaceSkipsUnusableRecords(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", filepath.Join(home, "gah-data"))
	live := filepath.Join(home, "live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := &Service{}
	svc.recordProject(ProjectKey(live), live)
	dataRoot := sdk.Home()
	t.Chdir(dataRoot)
	cur := currentDir() // 归一后的真值(macOS 上 /var → /private/var)
	// 直接落盘两条「不该被选中的记录」:空目录(手改 workspaces.json 或旧版残留会留下)
	// 与「当前目录本身」(上一次就是从数据根启动的)。recordProject 不接受空 dir,
	// 所以这里绕过它构造 —— 那正是坏数据的来源。
	recs := loadWorkspaces(workspacesPath())
	recs = append(recs,
		sdk.ProjectInfo{Key: "empty", Dir: "", TS: time.Now().Unix() + 60},
		sdk.ProjectInfo{Key: ProjectKey(cur), Dir: cur, TS: time.Now().Unix() + 30},
	)
	if err := saveWorkspaces(workspacesPath(), recs); err != nil {
		t.Fatal(err)
	}

	dir, ok := restoreLastWorkspace(svc, currentDir())
	if !ok {
		t.Fatal("应跳过空目录与当前目录本身,恢复到可用的那个")
	}
	if filepath.Clean(dir) != filepath.Clean(live) {
		t.Fatalf("恢复到 %q,期望 %q", dir, live)
	}

	// svc 为 nil 时安静返回(单元测试里 Loop 不带探针的常态)
	if _, ok := restoreLastWorkspace(nil, currentDir()); ok {
		t.Fatal("nil 服务不该发生恢复")
	}
}

// 要顺延到下一个可用的 —— 不能因为一条脏记录就不开机会。
func TestRestoreLastWorkspaceSkipsGoneDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", filepath.Join(home, "gah-data"))
	live := filepath.Join(home, "live")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(home, "gone")

	svc := &Service{}
	svc.recordProject(ProjectKey(gone), gone) // 先记(更近)
	svc.recordProject(ProjectKey(live), live) // 后记(更远)
	// recordProject 用秒级时间戳,同秒时靠加载顺序保证稳定 —— 显式改时间戳更稳:
	recs := loadWorkspaces(workspacesPath())
	for i := range recs {
		if recs[i].Key == ProjectKey(gone) {
			recs[i].TS = time.Now().Unix()
		} else {
			recs[i].TS = time.Now().Unix() - 60
		}
	}
	if err := saveWorkspaces(workspacesPath(), recs); err != nil {
		t.Fatal(err)
	}

	t.Chdir(sdk.Home())
	dir, ok := restoreLastWorkspace(svc, currentDir())
	if !ok {
		t.Fatal("应跳过已删除的目录、恢复到可用的那个")
	}
	if filepath.Clean(dir) != filepath.Clean(live) {
		t.Fatalf("恢复到 %q,期望 %q", dir, live)
	}
}
