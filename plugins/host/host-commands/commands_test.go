// host-commands 注册表单测:注册/获取/列举/同名冲突(非静默)/撤销(注册即副作用)。
package hostcommands

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func spec(name string) sdk.CommandSpec {
	return sdk.CommandSpec{Name: name, Usage: "/" + name, Desc: name + " 说明", Run: func([]string) (string, error) { return "", nil }}
}

func TestRegisterListGet(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register(spec("jobs")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(spec("model")); err != nil {
		t.Fatal(err)
	}
	got := r.List()
	if len(got) != 2 || got[0].Name != "jobs" || got[1].Name != "model" {
		t.Fatalf("List 应按注册顺序: %+v", got)
	}
	if s, ok := r.Get("jobs"); !ok || s.Desc != "jobs 说明" {
		t.Fatalf("Get 取回不符: %+v %v", s, ok)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("未注册命令不应命中")
	}
}

func TestRegisterNameConflict(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register(spec("jobs")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(spec("jobs")); err == nil || !strings.Contains(err.Error(), "已注册") {
		t.Fatalf("同名注册应拒绝并显式报错(非静默): %v", err)
	}
	if n := len(r.List()); n != 1 {
		t.Fatalf("冲突后注册表应不变: %d", n)
	}
}

func TestRegisterEmptyName(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Register(spec("")); err == nil {
		t.Fatal("空命令名应拒绝")
	}
}

func TestDisposeRemoval(t *testing.T) {
	r := NewRegistry()
	d, err := r.Register(spec("jobs"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Register(spec("model")); err != nil {
		t.Fatal(err)
	}
	d() // 插件卸载:命令撤销
	if _, ok := r.Get("jobs"); ok {
		t.Fatal("dispose 后命令应消失")
	}
	if s, ok := r.Get("model"); !ok || s.Name != "model" {
		t.Fatalf("其余命令应保留: %+v %v", s, ok)
	}
	// 撤销后可重注册同名(先到先得语义:空出的名字可再用)
	if _, err := r.Register(spec("jobs")); err != nil {
		t.Fatalf("dispose 后同名可重注册: %v", err)
	}
}

func TestDisposeIdempotent(t *testing.T) {
	r := NewRegistry()
	d, err := r.Register(spec("jobs"))
	if err != nil {
		t.Fatal(err)
	}
	d()
	d() // 幂等:重复撤销不 panic、不误删
	if s, ok := r.Get("jobs"); ok {
		t.Fatalf("撤销后应消失: %+v", s)
	}
}
