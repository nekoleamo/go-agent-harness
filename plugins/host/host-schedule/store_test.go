// store 单测:落盘/读回、原子写、损坏文件容忍、路径穿越防护、权限收紧。
package hostschedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
	"gopkg.in/yaml.v3"
)

// setupHome 把数据根指向临时目录(便携纪律:计划只落 $GAH_HOME/schedules)。
func setupHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GAH_HOME", dir)
	return dir
}

func TestStoreRoundTrip(t *testing.T) {
	home := setupHome(t)
	p := sdk.Schedule{
		ID: "sched-abc12345", Name: "每日对账", Cron: "0 8 * * *", Prompt: "生成昨日对账",
		Enabled: true, CreatedAt: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC),
		LastRunAt: time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC), LastStatus: sdk.ScheduleRunOK,
		NextRun: time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC), // 派生值:不得落盘
	}
	if err := savePlan(p); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "schedules", p.ID+".yaml")
	if fi, err := os.Stat(path); err != nil {
		t.Fatalf("计划文件应存在: %v", err)
	} else if testutil.PosixPerm() && fi.Mode().Perm() != 0o600 {
		t.Fatalf("计划文件权限应为 0600,得 %v", fi.Mode().Perm())
	}
	if fi, err := os.Stat(filepath.Join(home, "schedules")); err != nil {
		t.Fatal(err)
	} else if testutil.PosixPerm() && fi.Mode().Perm() != 0o700 {
		t.Fatalf("计划目录权限应为 0700,得 %v", fi.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "# gah 定时计划") {
		t.Fatalf("文件头应声明维护方,得: %q", string(raw[:40]))
	}
	if strings.Contains(string(raw), "next_run") {
		t.Fatal("派生字段 NextRun 不得落盘(yaml:\"-\")")
	}

	plans, warnings, err := loadPlans()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("读回应无警告: %v %v", warnings, err)
	}
	if len(plans) != 1 {
		t.Fatalf("应读回 1 条,得 %d", len(plans))
	}
	got := plans[0]
	if got.ID != p.ID || got.Name != p.Name || got.Cron != p.Cron || got.Prompt != p.Prompt || !got.Enabled {
		t.Fatalf("字段未原样往返: %+v", got)
	}
	if !got.CreatedAt.Equal(p.CreatedAt) || !got.LastRunAt.Equal(p.LastRunAt) || got.LastStatus != sdk.ScheduleRunOK {
		t.Fatalf("运行记录/创建时间未保留: %+v", got)
	}
	if !got.NextRun.IsZero() {
		t.Fatal("NextRun 落盘后应为零值(每次启动重算)")
	}
}

func TestLoadPlansNoDir(t *testing.T) {
	setupHome(t)
	plans, warnings, err := loadPlans()
	if err != nil || len(plans) != 0 || len(warnings) != 0 {
		t.Fatalf("无目录应返回空且不报错: %v %v", plans, err)
	}
}

// TestLoadPlansToleratesBadFile 单文件损坏/非法 id 只跳过该条(其余照常可用)。
func TestLoadPlansToleratesBadFile(t *testing.T) {
	home := setupHome(t)
	good := sdk.Schedule{ID: "sched-good0001", Name: "好的", Cron: "0 8 * * *", Prompt: "跑", Enabled: true}
	if err := savePlan(good); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "schedules")
	if err := os.WriteFile(filepath.Join(dir, "sched-broken01.yaml"), []byte("{{ not yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 目录名不合规(如手工放进去的带空格/大写/id 为空)
	b, _ := yaml.Marshal(sdk.Schedule{ID: "../evil", Name: "x", Cron: "* * * * *", Prompt: "y"})
	if err := os.WriteFile(filepath.Join(dir, "sched-evil01.yaml"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	// 隐藏文件/非 yaml 文件必须忽略(临时文件、README 之类)
	if err := os.WriteFile(filepath.Join(dir, ".tmp.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	plans, warnings, err := loadPlans()
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].ID != good.ID {
		t.Fatalf("只应读回好的那条: %+v", plans)
	}
	if len(warnings) != 2 {
		t.Fatalf("坏文件与非法 id 各应产生 1 条警告,得 %d: %v", len(warnings), warnings)
	}
}

func TestSavePlanRejectsBadID(t *testing.T) {
	setupHome(t)
	for _, id := range []string{"", "../x", "A-B", "sched_x", strings.Repeat("a", 65)} {
		if err := savePlan(sdk.Schedule{ID: id, Name: "n", Cron: "* * * * *", Prompt: "p"}); err == nil {
			t.Fatalf("ID %q 应被拒绝(目录名即 ID,必须防路径穿越)", id)
		}
	}
}

func TestRemovePlanFileIdempotent(t *testing.T) {
	home := setupHome(t)
	if err := savePlan(sdk.Schedule{ID: "sched-rr000001", Name: "n", Cron: "* * * * *", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := removePlanFile("sched-rr000001"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "schedules", "sched-rr000001.yaml")); !os.IsNotExist(err) {
		t.Fatalf("文件应已删除: %v", err)
	}
	if err := removePlanFile("sched-rr000001"); err != nil {
		t.Fatalf("重复删除应幂等成功: %v", err)
	}
	if err := removePlanFile("../evil"); err == nil {
		t.Fatal("非法 ID 应被拒绝")
	}
}

func TestNewPlanID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		id, err := newPlanID()
		if err != nil {
			t.Fatal(err)
		}
		if !validID(id) {
			t.Fatalf("生成的 ID 不合规: %q", id)
		}
		if seen[id] {
			t.Fatalf("ID 重复: %q", id)
		}
		seen[id] = true
	}
}
