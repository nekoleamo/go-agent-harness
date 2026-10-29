package main

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gz 把一段字节压成 gzip(测试里模拟内嵌产物)。
func gz(t *testing.T, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestStageEmbedded 内嵌 sidecar 解一次、之后复用;内容必须与内嵌字节一致。
//
// 三条不变量:① 解出来的就是内嵌的那份;② 第二次不再重复解压(staged=false);
// ③ 可执行位存在(否则 spawn 会「找不到文件」而不是「没权限」,更难查)。
// 反向验证:把「已存在即复用」去掉,第 ② 条断言必红。
func TestStageEmbedded(t *testing.T) {
	old := sidecarGz
	t.Cleanup(func() { sidecarGz = old })

	payload := []byte("#!/bin/sh\necho embedded-sidecar\n")
	sidecarGz = gz(t, payload)

	home := t.TempDir()
	p1, err := stageEmbedded(home, "0.1.7")
	if err != nil {
		t.Fatalf("解内嵌 sidecar 失败: %v", err)
	}
	if !p1.staged || !p1.external {
		t.Fatalf("首次应 staged=true external=true: %+v", p1)
	}
	if got, _ := os.ReadFile(p1.bin); !bytes.Equal(got, payload) {
		t.Fatalf("解出来的内容不对: %q", got)
	}
	if st, err := os.Stat(p1.bin); err != nil || st.Mode()&0o100 == 0 {
		t.Fatalf("副本应可执行: %v mode=%v", err, st.Mode())
	}
	if !strings.Contains(p1.bin, "gah-0.1.7-") {
		t.Fatalf("副本名应带版本与指纹: %s", p1.bin)
	}
	// 数据根 = sidecar 所在目录的 gah-data(便携模式下 = 程序目录,见 resolveRuntime)。
	if p1.dataRoot != filepath.Join(home, "gah-data") {
		t.Fatalf("数据根应跟着副本目录走: %q", p1.dataRoot)
	}

	p2, err := stageEmbedded(home, "0.1.7")
	if err != nil {
		t.Fatal(err)
	}
	if p2.staged {
		t.Fatal("同一份内嵌字节应复用,不该重复解压")
	}
	if p2.bin != p1.bin {
		t.Fatalf("复用应指向同一副本: %q vs %q", p2.bin, p1.bin)
	}

	// 换了内嵌字节(新版本壳)⇒ 新名字 + 重新解压,旧副本被清理。
	sidecarGz = gz(t, []byte("#!/bin/sh\necho v2\n"))
	p3, err := stageEmbedded(home, "0.1.7")
	if err != nil {
		t.Fatal(err)
	}
	if !p3.staged || p3.bin == p1.bin {
		t.Fatalf("内嵌字节变了应换名字: %+v", p3)
	}
	if _, err := os.Stat(p1.bin); !isErrNotExist(err) {
		t.Fatal("旧副本应被清理")
	}
}

// TestResolveRuntimePrefersEmbedded 内嵌存在时优先用它;GAH_SIDECAR 仍是最高优先(开发演练)。
func TestResolveRuntimePrefersEmbedded(t *testing.T) {
	old := sidecarGz
	t.Cleanup(func() { sidecarGz = old })
	sidecarGz = gz(t, []byte("embedded"))

	home := t.TempDir()
	plan, err := resolveRuntime(home, "0.1.7")
	if err != nil {
		t.Fatalf("应走内嵌路径: %v", err)
	}
	if !plan.external || !strings.HasPrefix(plan.bin, filepath.Join(home, "bin")) {
		t.Fatalf("内嵌产物应落用户数据目录: %+v", plan)
	}

	// GAH_SIDECAR 显式指定时以它为准(就地跑,数据根在它同级)。
	dev := filepath.Join(t.TempDir(), "gah-dev")
	writeFile(t, dev, "dev-binary")
	t.Setenv("GAH_SIDECAR", dev)
	plan2, err := resolveRuntime(home, "0.1.7")
	if err != nil {
		t.Fatal(err)
	}
	if plan2.bin != dev || plan2.external {
		t.Fatalf("GAH_SIDECAR 应就地跑: %+v", plan2)
	}
	if plan2.dataRoot != filepath.Join(filepath.Dir(dev), "gah-data") {
		t.Fatalf("就地跑的数据根应是 sidecar 同级 gah-data: %q", plan2.dataRoot)
	}
}
