// store.go:计划落盘($GAH_HOME/schedules/<id>.yaml,一条计划一个文件)。
//
// 便携纪律:目录只从 sdk.Home()(运行期 = GAH_HOME)派生,不落 cwd/系统根;
// 目录 0700、文件 0600(计划里可能含业务描述);写入走「临时文件 + rename」原子替换,
// 避免半截 YAML 让计划在下次启动时读不出来。
package hostschedule

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
	"gopkg.in/yaml.v3"
)

// fileHeader 落盘文件头(声明「谁维护」,避免用户手改后被下次保存覆盖而不自知)。
const fileHeader = "# gah 定时计划(NOND-W4):由界面/`/schedule` 命令维护;手改前请关闭 gah。\n"

// idPattern 计划 id 允许的字符集(目录名即 id → 必须防路径穿越)。
var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// schedulesDir 计划目录(每次现算,便于测试与运行期 GAH_HOME 变化)。
func schedulesDir() string { return filepath.Join(sdk.Home(), "schedules") }

// validID 校验计划 ID。
func validID(id string) bool { return idPattern.MatchString(id) }

// newPlanID 生成计划 ID(随机 4 字节十六进制,形如 sched-1a2b3c4d)。
// 不用名称派生:名称多为中文/带空格,派生不出稳定且合法的目录名。
func newPlanID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("host-schedule: 生成计划 ID 失败: %w", err)
	}
	return "sched-" + hex.EncodeToString(b), nil
}

// savePlan 原子写入一条计划(目录/文件权限收紧;失败显式返回错误)。
func savePlan(p sdk.Schedule) error {
	if !validID(p.ID) {
		return fmt.Errorf("host-schedule: 计划 ID 非法: %q", p.ID)
	}
	dir := schedulesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("host-schedule: 创建计划目录失败(%s): %w", dir, err)
	}
	b, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("host-schedule: 序列化计划失败: %w", err)
	}
	tmp := filepath.Join(dir, "."+p.ID+".yaml.tmp")
	if err := os.WriteFile(tmp, append([]byte(fileHeader), b...), 0o600); err != nil {
		return fmt.Errorf("host-schedule: 写计划失败(%s): %w", tmp, err)
	}
	dst := filepath.Join(dir, p.ID+".yaml")
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("host-schedule: 替换计划文件失败(%s): %w", dst, err)
	}
	return nil
}

// loadPlans 读取全部计划,按创建时间(同刻按 ID)稳定排序。
// 单文件损坏只跳过该条并把原因作为 warning 返回(其余计划照常可用),不整体失败。
func loadPlans() (plans []sdk.Schedule, warnings []string, err error) {
	dir := schedulesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil // 首启无目录 = 无计划
		}
		return nil, nil, fmt.Errorf("host-schedule: 读取计划目录失败(%s): %w", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".yaml") || strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			warnings = append(warnings, fmt.Sprintf("%s 读取失败: %v", name, rerr))
			continue
		}
		var p sdk.Schedule
		if uerr := yaml.Unmarshal(raw, &p); uerr != nil {
			warnings = append(warnings, fmt.Sprintf("%s 解析失败(已跳过): %v", name, uerr))
			continue
		}
		if p.ID == "" {
			p.ID = strings.TrimSuffix(name, ".yaml")
		}
		if !validID(p.ID) {
			warnings = append(warnings, fmt.Sprintf("%s 计划 ID 非法(已跳过): %q", name, p.ID))
			continue
		}
		plans = append(plans, p)
	}
	sort.SliceStable(plans, func(i, j int) bool {
		if !plans[i].CreatedAt.Equal(plans[j].CreatedAt) {
			return plans[i].CreatedAt.Before(plans[j].CreatedAt)
		}
		return plans[i].ID < plans[j].ID
	})
	return plans, warnings, nil
}

// removePlanFile 删除计划文件(不存在 = 幂等成功)。
func removePlanFile(id string) error {
	if !validID(id) {
		return fmt.Errorf("host-schedule: 计划 ID 非法: %q", id)
	}
	path := filepath.Join(schedulesDir(), id+".yaml")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("host-schedule: 删除计划失败(%s): %w", path, err)
	}
	return nil
}
