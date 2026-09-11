// host-backup 装配与命令路径补测:Start(服务+命令注册+keep 配置+启动自动备份)、
// 命令各分支(立即/list/restore/自定义路径)、轮转保留份数、服务名。
// 既有 backup_test.go 覆盖备份/恢复数据路径;这里覆盖插件入口与命令分发。
package hostbackup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeCtx 最小 Ctx 桩(Provide/Inject 有实体;其余 no-op)。
type fakeCtx struct {
	mu   sync.Mutex
	svcs map[string]any
}

func newFakeCtx() *fakeCtx { return &fakeCtx{svcs: map[string]any{}} }

func (c *fakeCtx) Provide(key string, svc any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.svcs[key]; ok {
		return errors.New("fakeCtx: 重复注册 " + key)
	}
	c.svcs[key] = svc
	return nil
}

func (c *fakeCtx) Inject(key string, out any) error {
	c.mu.Lock()
	svc, ok := c.svcs[key]
	c.mu.Unlock()
	if !ok {
		return errors.New("fakeCtx: 未装配 " + key)
	}
	rv := reflect.ValueOf(out)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("fakeCtx: Inject 目标应为非 nil 指针")
	}
	ev := rv.Elem()
	if !reflect.TypeOf(svc).AssignableTo(ev.Type()) {
		return errors.New("fakeCtx: " + key + " 类型不符")
	}
	ev.Set(reflect.ValueOf(svc))
	return nil
}

func (c *fakeCtx) Subscribe(string, sdk.AnyListener) sdk.Disposer { return func() {} }

func (c *fakeCtx) Emit(context.Context, string, any, sdk.DispatchMode) (any, error) { return nil, nil }

func (c *fakeCtx) Logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeCmds 最小命令注册表桩。
type fakeCmds struct {
	mu    sync.Mutex
	specs map[string]sdk.CommandSpec
}

func newFakeCmds() *fakeCmds { return &fakeCmds{specs: map[string]sdk.CommandSpec{}} }

func (r *fakeCmds) Register(spec sdk.CommandSpec) (sdk.Disposer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.specs[spec.Name]; ok {
		return nil, fmt.Errorf("命令已注册: %s", spec.Name)
	}
	r.specs[spec.Name] = spec
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.specs, spec.Name)
	}, nil
}

func (r *fakeCmds) List() []sdk.CommandSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sdk.CommandSpec, 0, len(r.specs))
	for _, s := range r.specs {
		out = append(out, s)
	}
	return out
}

func (r *fakeCmds) Get(name string) (sdk.CommandSpec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.specs[name]
	return s, ok
}

func (r *fakeCmds) get(t *testing.T, name string) sdk.CommandSpec {
	t.Helper()
	s, ok := r.Get(name)
	if !ok {
		t.Fatalf("命令 %s 未注册", name)
	}
	return s
}

// startBackup 在隔离 GAH_HOME 下装配插件,返回备份服务、命令注册表与 disposer。
func startBackup(t *testing.T, data map[string]any) (*Backup, *fakeCmds, sdk.Disposer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, "config", "provider.yaml"), "api_key: sk-test\n")

	c := newFakeCtx()
	cmds := newFakeCmds()
	if err := c.Provide("ctx.commands", sdk.CommandRegistry(cmds)); err != nil {
		t.Fatal(err)
	}
	d, err := (&Plugin{}).Start(c, &sdk.Manifest{ID: "host-backup", Data: data})
	if err != nil {
		t.Fatalf("Start 应成功: %v", err)
	}
	var svc sdk.BackupService
	if err := c.Inject("ctx.backup", &svc); err != nil {
		t.Fatalf("ctx.backup 应可注入: %v", err)
	}
	b, ok := svc.(*Backup)
	if !ok {
		t.Fatalf("ctx.backup 应由本插件实现: %T", svc)
	}
	return b, cmds, d
}

// TestPluginNameAndServiceName 插件名与服务名同源(装配/展示一致)。
func TestPluginNameAndServiceName(t *testing.T) {
	if got := (&Plugin{}).Name(); got != "host-backup" {
		t.Fatalf("Plugin.Name()=%q", got)
	}
	if got := (&Backup{}).Name(); got != "host-backup" {
		t.Fatalf("Backup.Name()=%q", got)
	}
}

// TestStartRegistersCommandAndService Start 提供 ctx.backup 并注册 /backup(层级选项齐备)。
func TestStartRegistersCommandAndService(t *testing.T) {
	b, cmds, d := startBackup(t, map[string]any{"keep": 3})
	if b.keepN() != 3 {
		t.Fatalf("data.keep 应生效: %d", b.keepN())
	}
	spec := cmds.get(t, "backup")
	if len(spec.Args) != 2 {
		t.Fatalf("/backup 应有 2 级参数: %+v", spec.Args)
	}
	// 一级:立即备份 / list / restore / 自定义路径
	opts := spec.Args[0].Options(nil)
	vals := make([]string, 0, len(opts))
	for _, o := range opts {
		vals = append(vals, o.Value)
	}
	for _, want := range []string{backupNowSentinel, "list", "restore", backupCustomSentinel} {
		if !contains(vals, want) {
			t.Fatalf("一级选项缺 %s: %v", want, vals)
		}
	}
	if spec.Args[0].FreeArgs != nil {
		t.Fatal("一级应为纯枚举")
	}
	// 二级:restore 才枚举备份、自定义路径才要自由参数
	if got := spec.Args[1].Options([]string{"backup", "list"}); len(got) != 0 {
		t.Fatalf("非 restore 不限枚举: %+v", got)
	}
	if got := spec.Args[1].Options([]string{"backup", backupNowSentinel}); len(got) != 0 {
		t.Fatalf("立即备份不限枚举: %+v", got)
	}
	if _, err := b.Backup(""); err != nil {
		t.Fatal(err)
	}
	restoreOpts := spec.Args[1].Options([]string{"backup", "restore"})
	if len(restoreOpts) != 1 || !strings.HasPrefix(restoreOpts[0].Value, "gah-backup-") {
		t.Fatalf("restore 二级应枚举已有归档: %+v", restoreOpts)
	}
	if !strings.Contains(restoreOpts[0].Desc, "KB") {
		t.Fatalf("选项说明应含大小: %+v", restoreOpts[0])
	}
	if fa := spec.Args[1].FreeArgs([]string{"backup", backupCustomSentinel}); len(fa) != 1 || fa[0] != "目标路径" {
		t.Fatalf("自定义路径二级要自由输入: %v", fa)
	}
	if fa := spec.Args[1].FreeArgs([]string{"backup", "restore"}); len(fa) != 0 {
		t.Fatalf("restore 二级不要自由输入: %v", fa)
	}
	// 撤销:命令与服务随 Disposer 一并撤销
	d()
	if _, ok := cmds.Get("backup"); ok {
		t.Fatal("卸载后 /backup 应撤销")
	}
}

// TestStartDefaultKeep 未配置 keep 时用缺省轮转份数。
func TestStartDefaultKeep(t *testing.T) {
	b, _, _ := startBackup(t, nil)
	if b.keepN() != defaultKeep {
		t.Fatalf("缺省 keepN=%d,期望 %d", b.keepN(), defaultKeep)
	}
	zero := &Backup{}
	if zero.keepN() != defaultKeep {
		t.Fatalf("keep<=0 应回落缺省: %d", zero.keepN())
	}
}

// TestStartWithoutCommandRegistry ctx.commands 未装配时跳过命令注册(服务仍可用)。
func TestStartWithoutCommandRegistry(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c := newFakeCtx()
	d, err := (&Plugin{}).Start(c, &sdk.Manifest{ID: "host-backup"})
	if err != nil {
		t.Fatalf("缺 ctx.commands 不应导致装配失败: %v", err)
	}
	if d == nil {
		t.Fatal("应返回 disposer")
	}
	d()
	var svc sdk.BackupService
	if err := c.Inject("ctx.backup", &svc); err != nil {
		t.Fatalf("ctx.backup 应仍可用: %v", err)
	}
}

// TestBackupCmdListAndRestore /backup 命令分发:list 空/非空、restore 缺参、自定义路径缺路径。
func TestBackupCmdListAndRestore(t *testing.T) {
	b, cmds, _ := startBackup(t, nil)
	run := cmds.get(t, "backup").Run

	out, err := run([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "无") {
		t.Fatalf("空备份应提示无: %q", out)
	}

	name, err := b.Backup("")
	if err != nil {
		t.Fatal(err)
	}
	out, err = run([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, name) || !strings.Contains(out, "最新在前") {
		t.Fatalf("list 应列出归档: %q", out)
	}

	// restore 缺归档名:显式报错(提示 list)
	if _, err := run([]string{"restore"}); err == nil || !strings.Contains(err.Error(), "restore <name>") {
		t.Fatalf("restore 缺参应报错: %v", err)
	}
	// restore 不存在的归档
	if _, err := run([]string{"restore", "gah-backup-nope.tar.gz"}); err == nil {
		t.Fatal("不存在的归档应报错")
	}
	// restore 成功
	mustWrite(t, filepath.Join(b.home, "config", "provider.yaml"), "api_key: sk-BAD\n")
	out, err = run([]string{"restore", name})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, name) || !strings.Contains(out, "重启后完全生效") {
		t.Fatalf("restore 成功应回报: %q", out)
	}
	got, _ := os.ReadFile(filepath.Join(b.home, "config", "provider.yaml"))
	if string(got) != "api_key: sk-test\n" {
		t.Fatalf("restore 应回滚内容: %q", got)
	}

	// 哨兵 + 空路径:显式报错(不静默落到默认目录)
	if _, err := run([]string{backupCustomSentinel}); err == nil || !strings.Contains(err.Error(), "未给目标路径") {
		t.Fatalf("自定义哨兵无路径应报错: %v", err)
	}
}

// TestBackupCmdNowAndRelativeDest 哨兵=立即备份;相对 dest 落到默认备份目录并回报该位置。
func TestBackupCmdNowAndRelativeDest(t *testing.T) {
	b, cmds, _ := startBackup(t, nil)
	run := cmds.get(t, "backup").Run

	out, err := run([]string{backupNowSentinel})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, b.backupDir()) {
		t.Fatalf("立即备份应回报默认目录: %q", out)
	}
	if len(b.List()) != 1 {
		t.Fatalf("应产生一份归档: %+v", b.List())
	}

	// 相对路径:归档按相对路径(相对 cwd)写出,回报默认目录拼接名
	wd := t.TempDir()
	t.Chdir(wd)
	out, err = run([]string{backupCustomSentinel + "rel-out.tar.gz"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Join(b.backupDir(), "rel-out.tar.gz")) {
		t.Fatalf("相对 dest 应回报默认目录路径: %q", out)
	}
	if _, err := os.Stat(filepath.Join(wd, "rel-out.tar.gz")); err != nil {
		t.Fatalf("相对 dest 应写出归档: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.home, "rel-out.tar.gz")); err == nil {
		t.Fatal("相对 dest 不应写进 GAH_HOME")
	}
}

// TestBackupCmdAbsoluteDest 绝对路径 dest:回报完整路径(不拼默认目录)。
func TestBackupCmdAbsoluteDest(t *testing.T) {
	b, cmds, _ := startBackup(t, nil)
	dest := filepath.Join(t.TempDir(), "abs.tar.gz")
	out, err := cmds.get(t, "backup").Run([]string{backupCustomSentinel + dest})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, dest) || strings.Contains(out, b.backupDir()) {
		t.Fatalf("绝对 dest 应只回报该路径: %q", out)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatal(err)
	}
}

// TestRotateKeepsNewest keep 份数轮转:超出部分按时间删旧留新。
func TestRotateKeepsNewest(t *testing.T) {
	b := buildHome(t) // buildHome 已在 backup_test.go 定义
	b.keep = 2
	names := []string{"gah-backup-20260101-000000.tar.gz", "gah-backup-20260102-000000.tar.gz",
		"gah-backup-20260103-000000.tar.gz", "gah-backup-20260104-000000.tar.gz"}
	base := time.Now().Add(-24 * time.Hour)
	for i, n := range names {
		p := filepath.Join(b.backupDir(), n)
		mustWrite(t, p, "junk")
		ts := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	b.rotate()
	got := map[string]bool{}
	for _, bi := range b.List() {
		got[bi.Name] = true
	}
	if len(got) != 2 {
		t.Fatalf("应只保留 2 份: %v", got)
	}
	for _, n := range names[2:] {
		if !got[n] {
			t.Fatalf("应保留较新归档 %s: %v", n, got)
		}
	}
	for _, n := range names[:2] {
		if got[n] {
			t.Fatalf("较旧归档 %s 应被轮转删除: %v", n, got)
		}
	}
}

// TestBackupOnStartCreatesAndRotates data.backup_on_start=true:启动即异步备份并按 keep 轮转。
func TestBackupOnStartCreatesAndRotates(t *testing.T) {
	// 先放 3 份旧归档:启动备份后轮转应只剩 keep=1 份(新的那份)
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < 3; i++ {
		p := filepath.Join(home, "backups", fmt.Sprintf("gah-backup-2026010%d-000000.tar.gz", i+1))
		mustWrite(t, p, "junk")
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, "config", "provider.yaml"), "api_key: sk-test\n")

	c := newFakeCtx()
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{ID: "host-backup", Data: map[string]any{
		"backup_on_start": true, "keep": 1,
	}}); err != nil {
		t.Fatalf("Start 应成功: %v", err)
	}
	var svc sdk.BackupService
	if err := c.Inject("ctx.backup", &svc); err != nil {
		t.Fatal(err)
	}
	b := svc.(*Backup)
	// 启动备份 + 轮转都在后台 goroutine:轮询到收敛(只剩 keep=1 份)再断言
	deadline := time.Now().Add(5 * time.Second)
	for len(b.List()) != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	list := b.List()
	if len(list) != 1 {
		t.Fatalf("轮转应保留 keep=1 份: %+v", list)
	}
	if strings.HasPrefix(list[0].Name, "gah-backup-2026010") {
		t.Fatalf("保留的应是新备份而非旧归档: %s", list[0].Name)
	}
	if list[0].Size <= 4 {
		t.Fatalf("新备份不应是占位内容: %+v", list[0])
	}
}
