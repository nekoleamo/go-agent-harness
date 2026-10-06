package toolschedule

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeSvc 替身:真存一份计划,并按 host-schedule 的语义(Update = 整组覆盖)实现,
// 好让「部分更新不能抹空字段」这条被测出来。
type fakeSvc struct {
	plans  []sdk.Schedule
	addErr error
	next   int
	ran    []string
}

func (f *fakeSvc) List() []sdk.Schedule { return f.plans }

func (f *fakeSvc) Add(p sdk.Schedule) (sdk.Schedule, error) {
	if f.addErr != nil {
		return sdk.Schedule{}, f.addErr
	}
	f.next++
	p.ID = "p" + string(rune('0'+f.next))
	p.CreatedAt = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	p.NextRun = p.CreatedAt.Add(time.Hour)
	f.plans = append(f.plans, p)
	return p, nil
}

func (f *fakeSvc) Update(p sdk.Schedule) (sdk.Schedule, error) {
	for i, cur := range f.plans {
		if cur.ID == p.ID {
			// 与 host-schedule 一致:Name/Cron/Prompt/Enabled 整组覆盖(所以调用方必须先读回)
			cur.Name, cur.Cron, cur.Prompt, cur.Enabled = p.Name, p.Cron, p.Prompt, p.Enabled
			cur.Once, cur.OnceDate = p.Once, p.OnceDate
			f.plans[i] = cur
			return cur, nil
		}
	}
	return sdk.Schedule{}, errors.New("计划不存在: " + p.ID)
}

func (f *fakeSvc) Remove(id string) error {
	for i, cur := range f.plans {
		if cur.ID == id {
			f.plans = append(f.plans[:i], f.plans[i+1:]...)
			return nil
		}
	}
	return errors.New("计划不存在: " + id)
}

func (f *fakeSvc) RunNow(id string) error {
	for _, cur := range f.plans {
		if cur.ID == id {
			f.ran = append(f.ran, id)
			return nil
		}
	}
	return errors.New("计划不存在: " + id)
}

// call 执行一次工具调用并解成 map(业务失败走 {"error":...},不中断 turn)。
func call(t *testing.T, tool *Tool, raw string) map[string]any {
	t.Helper()
	out, err := tool.Execute(t.Context(), raw)
	if err != nil {
		t.Fatalf("Execute 不应返回 error(业务失败走 error 字段): %v", err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func errOf(m map[string]any) string {
	s, _ := m["error"].(string)
	return s
}

// TestAddRequiresFields add 三项缺一不可(cron 错误由服务层拒绝并原样回传)。
func TestAddRequiresFields(t *testing.T) {
	svc := &fakeSvc{}
	tool := &Tool{svc: svc}
	for _, raw := range []string{
		`{"action":"add","cron":"0 9 * * 1","prompt":"x"}`,
		`{"action":"add","name":"n","prompt":"x"}`,
		`{"action":"add","name":"n","cron":"0 9 * * 1"}`,
		`{"action":"add","name":"n","cron":"0 9 * * 1","prompt":"  "}`,
	} {
		if got := errOf(call(t, tool, raw)); !strings.Contains(got, "name/cron/prompt") {
			t.Fatalf("%s 应被拒: %q", raw, got)
		}
	}
	if len(svc.plans) != 0 {
		t.Fatal("被拒的 add 不应落库")
	}
	// 服务层错误(如非法 cron)原样回给模型,不吞成成功
	svc.addErr = errors.New("host-schedule: cron 非法")
	if got := errOf(call(t, tool, `{"action":"add","name":"n","cron":"bad","prompt":"x"}`)); !strings.Contains(got, "cron 非法") {
		t.Fatalf("服务层错误应回传: %q", got)
	}
}

// TestAddDefaultsEnabled 模型新建的计划默认启用(默认停用是插件级开关,不是每条计划);
// 显式 enabled:false 则尊重。
func TestAddDefaultsEnabled(t *testing.T) {
	svc := &fakeSvc{}
	tool := &Tool{svc: svc}
	call(t, tool, `{"action":"add","name":"晨报","cron":"0 9 * * *","prompt":"总结昨天的提交"}`)
	call(t, tool, `{"action":"add","name":"挂着","cron":"0 9 * * *","prompt":"x","enabled":false}`)
	if !svc.plans[0].Enabled || svc.plans[1].Enabled {
		t.Fatalf("enabled 默认值/显式值处理错误: %+v", svc.plans)
	}
}

// TestUpdatePartialKeepsFields 部分更新不得抹空未提字段 —— 服务层 Update 是整组覆盖,
// 工具必须先读回再合并(否则「只改 cron」会把 name/prompt 清空)。
func TestUpdatePartialKeepsFields(t *testing.T) {
	svc := &fakeSvc{}
	tool := &Tool{svc: svc}
	call(t, tool, `{"action":"add","name":"晨报","cron":"0 9 * * *","prompt":"总结昨天的提交"}`)
	id := svc.plans[0].ID

	call(t, tool, `{"action":"update","id":"`+id+`","cron":"30 8 * * *"}`)
	got := svc.plans[0]
	if got.Cron != "30 8 * * *" || got.Name != "晨报" || got.Prompt != "总结昨天的提交" || !got.Enabled {
		t.Fatalf("改 cron 不应影响其它字段: %+v", got)
	}
	// 停用:显式 false 必须能生效(指针区分「没给」与「显式 false」)
	call(t, tool, `{"action":"update","id":"`+id+`","enabled":false}`)
	if svc.plans[0].Enabled {
		t.Fatalf("显式 enabled:false 应生效: %+v", svc.plans[0])
	}
	if svc.plans[0].Cron != "30 8 * * *" || svc.plans[0].Prompt != "总结昨天的提交" {
		t.Fatalf("停用不应抹掉其它字段: %+v", svc.plans[0])
	}
	// 不存在的 id = 显式错误
	if got := errOf(call(t, tool, `{"action":"update","id":"nope","name":"x"}`)); !strings.Contains(got, "不存在") {
		t.Fatalf("不存在的 id 应报错: %q", got)
	}
	if got := errOf(call(t, tool, `{"action":"update"}`)); !strings.Contains(got, "需要 id") {
		t.Fatalf("缺 id 应报错: %q", got)
	}
}

// TestRemoveAndRun remove 删掉即不在列表;run 透传到服务层且要求 id。
func TestRemoveAndRun(t *testing.T) {
	svc := &fakeSvc{}
	tool := &Tool{svc: svc}
	call(t, tool, `{"action":"add","name":"晨报","cron":"0 9 * * *","prompt":"x"}`)
	id := svc.plans[0].ID

	if m := call(t, tool, `{"action":"run","id":"`+id+`"}`); m["started"] != id {
		t.Fatalf("run 应回 started: %+v", m)
	}
	if len(svc.ran) != 1 || svc.ran[0] != id {
		t.Fatalf("run 应透传到服务层: %+v", svc.ran)
	}
	if got := errOf(call(t, tool, `{"action":"run"}`)); !strings.Contains(got, "需要 id") {
		t.Fatalf("缺 id 应报错: %q", got)
	}
	if m := call(t, tool, `{"action":"remove","id":"`+id+`"}`); m["removed"] != id {
		t.Fatalf("remove 应回 removed: %+v", m)
	}
	if len(svc.plans) != 0 {
		t.Fatal("remove 后不应还在列表里")
	}
	if got := errOf(call(t, tool, `{"action":"remove","id":""}`)); !strings.Contains(got, "需要 id") {
		t.Fatalf("缺 id 应报错: %q", got)
	}
}

// TestListView list 吐稳定字段:时间格式化、last_status 与 next_run 按零值省略。
func TestListView(t *testing.T) {
	svc := &fakeSvc{}
	tool := &Tool{svc: svc}
	call(t, tool, `{"action":"add","name":"晨报","cron":"0 9 * * *","prompt":"x"}`)
	svc.plans[0].LastRunAt = time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	svc.plans[0].LastStatus = sdk.ScheduleRunSkipped
	svc.plans[0].LastError = "有回合在跑"

	m := call(t, tool, `{"action":"list"}`)
	plans, ok := m["plans"].([]any)
	if !ok || len(plans) != 1 {
		t.Fatalf("list 应回 plans 数组: %+v", m)
	}
	p := plans[0].(map[string]any)
	if p["id"] != svc.plans[0].ID || p["name"] != "晨报" || p["cron"] != "0 9 * * *" || p["enabled"] != true {
		t.Fatalf("list 字段不符: %+v", p)
	}
	if p["next_run"] != "2026-09-18 11:00" || p["last_run_at"] != "2026-09-18 09:00" {
		t.Fatalf("时间应格式化输出: %+v", p)
	}
	if p["last_status"] != "skipped" || p["last_error"] != "有回合在跑" {
		t.Fatalf("运行记录应回传: %+v", p)
	}
	// 空列表:plans 为 [] 而不是 null(前端/模型侧都不必再判 nil)
	svc.plans = nil
	if m := call(t, tool, `{"action":"list"}`); m["plans"] == nil {
		t.Fatalf("空列表应为空数组: %+v", m)
	}
}

// TestBadArgs 非法/缺失 action 与坏 JSON 都是显式错误(不静默成功)。
func TestBadArgs(t *testing.T) {
	tool := &Tool{svc: &fakeSvc{}}
	if got := errOf(call(t, tool, `{}`)); !strings.Contains(got, "缺少 action") {
		t.Fatalf("缺 action 应报错: %q", got)
	}
	if got := errOf(call(t, tool, `{"action":"explode"}`)); !strings.Contains(got, "未知 action") {
		t.Fatalf("未知 action 应报错: %q", got)
	}
	tool2 := &Tool{svc: &fakeSvc{}}
	out, err := tool2.Execute(t.Context(), `{bad json`)
	if err != nil {
		t.Fatalf("坏 JSON 也不应中断 turn: %v", err)
	}
	m := out.(map[string]any)
	if !strings.Contains(errOf(m), "合法 JSON") {
		t.Fatalf("坏 JSON 应回错误: %+v", m)
	}
	// action 大小写/空白容错
	if m := call(t, tool, `{"action":" LIST "}`); m["plans"] == nil {
		t.Fatalf("action 应容错大小写与空白: %+v", m)
	}
}

// TestDefinition 定义自洽:五 action 齐备,描述不误导(无人值守红线必须写明)。
func TestDefinition(t *testing.T) {
	d := (&Tool{}).Definition()
	if d.Name != "schedule" {
		t.Fatalf("工具名不符: %s", d.Name)
	}
	props := d.InputSchema["properties"].(map[string]any)
	enum := props["action"].(map[string]any)["enum"].([]string)
	if strings.Join(enum, ",") != "list,add,update,remove,run" {
		t.Fatalf("action 集不符: %v", enum)
	}
	for _, want := range []string{"无人值守", "cron", "5 字段", "审批"} {
		if !strings.Contains(d.Description, want) {
			t.Fatalf("描述缺少要点 %q: %s", want, d.Description)
		}
	}
	if strings.Contains(d.Description, "**") {
		t.Fatal("模型可见文案不用 markdown 加粗")
	}
}

// 一次性:模型必须同时给 once 与 once_date —— cron 没有年字段,只写 cron 会变成
// 「每年那一天」,那是静默的语义漂移(比报错糟得多)。
func TestToolOnce(t *testing.T) {
	svc := &fakeSvc{}
	tool := &Tool{svc: svc}
	ctx := context.Background()

	got, err := tool.Execute(ctx, `{"action":"add","name":"提醒","cron":"0 9 20 11 *","prompt":"跑","once":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !hasErr(got, "once_date") {
		t.Fatalf("once=true 缺 once_date 应报错(否则会被排成每年),得 %v", got)
	}
	got, err = tool.Execute(ctx, `{"action":"add","name":"提醒","cron":"0 9 20 11 *","prompt":"跑","once":true,"once_date":"2026-11-20"}`)
	if err != nil {
		t.Fatal(err)
	}
	if hasErr(got, "") {
		t.Fatalf("给全 once/once_date 应成功,得 %v", got)
	}
	added := svc.plans[len(svc.plans)-1]
	if !added.Once || added.OnceDate != "2026-11-20" {
		t.Fatalf("once/once_date 未落到计划: %+v", added)
	}

	// update 改回循环:once 归零且 once_date 清空(否则重启后语义反了)
	got, err = tool.Execute(ctx, `{"action":"update","id":"`+added.ID+`","once":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if hasErr(got, "") {
		t.Fatalf("update 应成功,得 %v", got)
	}
	after := svc.plans[len(svc.plans)-1]
	if after.Once || after.OnceDate != "" {
		t.Fatalf("改回循环应清空 once/once_date,得 %+v", after)
	}
}

// hasErr 结果 map 里是否带 error;sub 非空时还要求包含它。
func hasErr(got any, sub string) bool {
	m, ok := got.(map[string]any)
	if !ok {
		return false
	}
	e, _ := m["error"].(string)
	if e == "" {
		return false
	}
	return sub == "" || strings.Contains(e, sub)
}

// 农历年度:模型必须把日期放在 lunar_date 上,cron 只能写时分 ——
// 写错了在线一定会被拒,不如在这里就拒并说清原因。
func TestToolLunar(t *testing.T) {
	svc := &fakeSvc{}
	tool := &Tool{svc: svc}
	ctx := context.Background()

	got, err := tool.Execute(ctx, `{"action":"add","name":"中秋","cron":"0 20 15 8 *","prompt":"打电话","lunar_date":"08-15"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !hasErr(got, "只写时分") {
		t.Fatalf("cron 限定了日/月时应拒,得 %v", got)
	}
	got, err = tool.Execute(ctx, `{"action":"add","name":"中秋","cron":"0 20 * * *","prompt":"打电话","lunar_date":"08-15"}`)
	if err != nil {
		t.Fatal(err)
	}
	if hasErr(got, "") {
		t.Fatalf("合法农历计划应成功,得 %v", got)
	}
	added := svc.plans[len(svc.plans)-1]
	if added.LunarDate != "08-15" {
		t.Fatalf("lunar_date 未落到计划: %+v", added)
	}
	// 一次性与农历互斥
	got, err = tool.Execute(ctx, `{"action":"add","name":"x","cron":"0 20 * * *","prompt":"跑","once":true,"once_date":"2026-09-25","lunar_date":"08-15"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !hasErr(got, "不能同时") {
		t.Fatalf("once 与 lunar_date 并用应拒,得 %v", got)
	}
}
