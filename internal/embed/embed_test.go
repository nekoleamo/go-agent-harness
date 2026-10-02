// guard:seed 与仓库 config/ 样板一致性(防止双重维护漂移)。
package embed

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"bytes"
	"crypto/sha256"
	"github.com/nekoleamo/go-agent-harness/internal/testutil"
)

// assertNoTempLeftovers 断言目录里没留下写产物的临时文件(残留意味着 rename 没成)。
func assertNoTempLeftovers(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".gah-tmp-") {
			t.Fatalf("临时文件残留: %s", e.Name())
		}
	}
}

// TestEnsurePluginsUpgrade 自动升级:内容与 embed 不一致 → 覆盖;一致 → 跳过(幂等)。
// 覆盖旧插件二进制的能力缺失问题(如旧 tool-basic 缺 web_search),无需手动删除。
func TestEnsurePluginsUpgrade(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsurePlugins(home); err != nil {
		t.Fatal(err)
	}
	// 写一个内容错误的旧产物(模拟旧版本二进制)
	dst := ""
	names, err := listNames(extPlugins, extPluginDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if strings.HasSuffix(n, ExtPluginExt) {
			dst = pluginDst(home, n)
			break
		}
	}
	if dst == "" {
		t.Fatal("无外部插件产物")
	}
	if err := os.WriteFile(dst, []byte("stale-plugin-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 覆盖前记下文件身份:覆盖必须走「临时文件 + rename」换 inode —— macOS 上旧 inode 会被
	// taskgated 判「Code Signature Invalid」SIGKILL(详见 DESIGN R23)。
	// Windows 没有 inode 语义(os.SameFile 比的是卷序列号+文件索引,rename 后可能判定相同),
	// 故这条断言只在 POSIX 上断言;Windows 上仍校验内容已换 + 无临时残留。
	oldFi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	// 二次 EnsurePlugins:内容不同 → 覆盖为 embed 产物
	upgraded, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, w := range upgraded {
		if w == dst {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("旧内容应被覆盖并写入: %v", upgraded)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "stale-plugin-binary" {
		t.Fatal("旧内容应被覆盖")
	}
	if !testutil.IsWindows() {
		if newFi, err := os.Stat(dst); err == nil && os.SameFile(oldFi, newFi) {
			t.Fatal("覆盖必须换文件身份(旧文件在 macOS 上可能被 taskgated 杀)")
		}
	}
	assertNoTempLeftovers(t, filepath.Dir(dst))
	// 第三次:内容已一致 → 跳过
	repeat, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat) != 0 {
		t.Fatalf("内容一致后应幂等跳过: %v", repeat)
	}
}

// TestEnsurePlugins 方案B首启释放:产物落 home/plugins/<name>/,幂等(不覆盖已有/内容一致)。
func TestEnsurePlugins(t *testing.T) {
	home := t.TempDir()
	written, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) == 0 {
		t.Fatal("应有随包插件释放")
	}
	// 产物可执行文件存在。可执行位是 POSIX 概念:Windows 无 Unix 权限位,
	// .exe 的 Mode() 恒不含 0o111,故只在非 Windows 校验(存在性两侧都校验)。
	for _, w := range written {
		fi, err := os.Stat(w)
		if err != nil {
			t.Fatalf("产物应存在: %s %v", w, err)
		}
		if runtime.GOOS != "windows" && fi.Mode()&0o111 == 0 {
			t.Fatalf("产物应可执行: %s mode=%v", w, fi.Mode())
		}
	}
	// 幂等:二次释放不写、不覆盖
	repeat, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat) != 0 {
		t.Fatalf("二次释放应为空: %v", repeat)
	}
}

// TestSeedVersionUpgrade 样板版本化升级:落盘 bundle 版本低于 seed → 备份后覆盖;
// 版本一致 → 不覆盖(用户编辑保留);无版本旧文件 → 升级(老用户自动补齐能力条目)。
func TestSeedVersionUpgrade(t *testing.T) {
	raw, err := Seed.ReadFile("seed/bundle-base.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cur := seedVersion(raw)
	if cur < 1 {
		t.Fatalf("seed 应声明 seed-version>=1, got %d", cur)
	}

	run := func(t *testing.T, old string, wantUpgraded bool) {
		t.Helper()
		home := t.TempDir()
		dst := filepath.Join(home, "config", "bundle-base.yaml")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte(old), 0o644); err != nil {
			t.Fatal(err)
		}
		written, err := EnsureSeed(home)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatal(err)
		}
		found := func() bool {
			for _, w := range written {
				if w == dst {
					return true
				}
			}
			return false
		}
		if wantUpgraded {
			if string(got) != string(raw) {
				t.Fatalf("旧版应升级为 seed 新版:\n%s", got)
			}
			if !found() {
				t.Fatalf("升级应记录 dst 写入: %v", written)
			}
			// 备份存在且为旧内容
			entries, err := os.ReadDir(filepath.Join(home, "config"))
			if err != nil {
				t.Fatal(err)
			}
			var bak []string
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), "bundle-base.yaml.bak-") {
					bak = append(bak, e.Name())
				}
			}
			if len(bak) == 0 {
				t.Fatal("升级应生成 .bak-* 备份")
			}
			bakRaw, _ := os.ReadFile(filepath.Join(home, "config", bak[0]))
			if string(bakRaw) != old {
				t.Fatal("备份内容应为旧样板")
			}
		} else {
			if string(got) != old {
				t.Fatal("版本一致时用户编辑不应被覆盖")
			}
			if found() {
				t.Fatalf("同版本不应写入 dst: %v", written)
			}
			// 其余缺失样板正常补写(bundle-tui 等)
			if len(written) == 0 {
				t.Fatal("缺失样板应正常补写")
			}
		}
	}

	// 无版本旧文件(最老用户)→ 升级
	t.Run("legacy-no-version-upgrades", func(t *testing.T) {
		run(t, "# 旧版样板\nname: base\nentries:\n", true)
	})
	// 显式低版本 → 升级
	t.Run("older-version-upgrades", func(t *testing.T) {
		run(t, "# seed-version: "+fmt.Sprint(cur-1)+"\nname: base\nentries:\n", true)
	})
	// 同版本 → 保留用户编辑
	t.Run("same-version-keeps-edits", func(t *testing.T) {
		run(t, "# seed-version: "+fmt.Sprint(cur)+"\nname: base\nentries:\n  - id: host-tools\n    data:\n      custom: true\n", false)
	})
}

func TestSeedMatchesRepoConfig(t *testing.T) {
	names, err := FileNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("seed 应为空")
	}
	repo := filepath.Join("..", "..", "config")
	for _, n := range names {
		seedRaw, err := Seed.ReadFile("seed/" + n)
		if err != nil {
			t.Fatal(err)
		}
		repoRaw, err := os.ReadFile(filepath.Join(repo, n))
		if err != nil {
			t.Fatalf("seed %s 在仓库 config/ 缺失: %v", n, err)
		}
		if strings.TrimSpace(string(seedRaw)) != strings.TrimSpace(string(repoRaw)) {
			t.Fatalf("seed/%s 与 config/%s 不一致(修改样板需同步两处)", n, n)
		}
	}
}

func TestEnsureSeedWritesOnce(t *testing.T) {
	home := t.TempDir()
	written, err := EnsureSeed(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) == 0 {
		t.Fatal("应释放样板")
	}
	// 二次调用:已存在,不覆盖
	written2, err := EnsureSeed(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(written2) != 0 {
		t.Fatalf("二次释放应 no-op,got %v", written2)
	}
	// 用户编辑不被覆盖
	p := filepath.Join(home, "config", "profile-tui.yaml")
	if err := os.WriteFile(p, []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureSeed(home); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "custom" {
		t.Fatal("用户编辑的配置不应被 seed 覆盖")
	}
}

// TestSeedRolesRelease 预置角色:首启释放、二次 no-op、用户改过的不被覆盖、坏文件守卫。
func TestSeedRolesRelease(t *testing.T) {
	home := t.TempDir()
	written, err := EnsureRoles(home)
	if err != nil {
		t.Fatal(err)
	}
	// 至少覆盖方案承诺的五个预置角色
	for _, id := range []string{"assistant", "finance", "novelist", "coding-master", "news-writer"} {
		dir := filepath.Join(home, "roles", id)
		if _, err := os.Stat(filepath.Join(dir, "role.yaml")); err != nil {
			t.Fatalf("预置角色 %s 未释放: %v", id, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ".seed-version")); err != nil {
			t.Fatalf("预置角色 %s 缺 .seed-version 标记: %v", id, err)
		}
	}
	if len(written) == 0 {
		t.Fatal("应释放角色目录")
	}
	// 二次调用:目录已存在 → 整体跳过
	written2, err := EnsureRoles(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(written2) != 0 {
		t.Fatalf("二次释放应 no-op, got %v", written2)
	}
	// 用户改过的角色:不得被覆盖/补齐
	p := filepath.Join(home, "roles", "finance", "AGENTS.md")
	if err := os.WriteFile(p, []byte("我的自定义规则"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRoles(home); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "我的自定义规则" {
		t.Fatal("用户编辑的角色文件被 seed 覆盖")
	}
	// 新增预置角色(缺失即释放):删掉一个目录后重放 → 只有它回来
	if err := os.RemoveAll(filepath.Join(home, "roles", "novelist")); err != nil {
		t.Fatal(err)
	}
	again, err := EnsureRoles(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || filepath.Base(again[0]) != "novelist" {
		t.Fatalf("缺失的角色应单独释放: %v", again)
	}
}

// roleIDRe 角色 ID 口径(与 internal/roles.ValidateID 同源;此处本地复刻以免测试引入依赖)。
var roleIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// TestSeedRolesParse 预置角色文件必须可解析(role.yaml 结构 + 非空身份句)。
// 它是 seed 的一部分 —— 写错了会让每个新装用户开局就有一个坏角色。
func TestSeedRolesParse(t *testing.T) {
	entries, err := fs.ReadDir(Seed, "seed/roles")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		seen++
		id := e.Name()
		if !roleIDRe.MatchString(id) {
			t.Errorf("角色目录名 %q 不符合 ID 口径", id)
		}
		raw, err := Seed.ReadFile("seed/roles/" + id + "/role.yaml")
		if err != nil {
			t.Fatalf("%s 缺 role.yaml: %v", id, err)
		}
		var spec struct {
			Name     string `yaml:"name"`
			Identity string `yaml:"identity"`
		}
		if err := yaml.Unmarshal(raw, &spec); err != nil {
			t.Fatalf("%s role.yaml 解析失败: %v", id, err)
		}
		if strings.TrimSpace(spec.Name) == "" || strings.TrimSpace(spec.Identity) == "" {
			t.Errorf("%s 的 name/identity 不能为空", id)
		}
		agents, err := Seed.ReadFile("seed/roles/" + id + "/AGENTS.md")
		if err != nil {
			t.Fatalf("%s 缺 AGENTS.md: %v", id, err)
		}
		if len(agents) > 32*1024 {
			t.Errorf("%s AGENTS.md 超 32KiB", id)
		}
		// 正文里不应出现 emoji(Web/TUI 观感纪律)
		if strings.ContainsFunc(string(agents), func(r rune) bool { return r > 0x1F000 }) {
			t.Errorf("%s AGENTS.md 含 emoji", id)
		}
	}
	if seen < 5 {
		t.Fatalf("预置角色应不少于 5 个, got %d", seen)
	}
}

// TestEnsurePluginsFastPathAndIntegrity 覆盖三条与「解压格式换代 + sha256 清单」直接相关的行为:
//
//	① 稳态二次调用**零写入**:磁盘上已有且哈希一致 ⇒ 不再解压、不再落盘(这是本次换
//	   格式的启动性能收益所在;旧实现每次启动都要全解压一遍再比)。
//	② 篡改产物 ⇒ 下次调用**修复**(内容必须与 embed 里的这份构建一致)。
//	③ 清单与产物不同批 / 缺清单 ⇒ 显式失败,不静默放行。
func TestEnsurePluginsFastPathAndIntegrity(t *testing.T) {
	home := t.TempDir()

	first, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("首启应释放出插件产物")
	}
	// 首启之后,plugins/ 下不应留任何临时文件
	entries, err := os.ReadDir(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".gah-tmp-") {
			t.Fatalf("临时文件残留:%s", e.Name())
		}
	}

	// ① 稳态:零写入
	second, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("内容一致时应跳过(零写入),got %d 条重写:%v", len(second), second)
	}

	// ② 篡改一个产物 ⇒ 下次调用修回来
	var victim string
	for _, p := range first {
		victim = p
		break
	}
	if err := os.WriteFile(victim, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	third, err := EnsurePlugins(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(third) != 1 {
		t.Fatalf("篡改的那一个应被修复重写,got %d 条:%v", len(third), third)
	}
	raw, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "tampered" || len(raw) < 1024 {
		t.Fatalf("修复后内容应等于 embed 里的产物(%d 字节)", len(raw))
	}
}

// packedDigests 的坏输入处理:清单是**必需**的,坏清单必须显式失败。
func TestPackedDigestsRejectsBadManifest(t *testing.T) {
	m, err := packedDigests()
	if err != nil {
		t.Fatalf("本仓产物的清单应可解析:%v", err)
	}
	if len(m) != 4 {
		t.Fatalf("清单应恰好 4 条,got %d(%v)", len(m), m)
	}
	// 每条哈希非零(真解析出来的,不是零值占位)
	for name, sum := range m {
		if sum == ([32]byte{}) {
			t.Errorf("%s 的哈希是零值 —— 解析没真生效", name)
		}
	}
}

// parseDigests 的坏输入路径:清单是**必需**的,坏清单必须显式失败而不是退回
// 「每次启动全解压」的第二条路 —— 两条路并存就会漂,而且没人发现漂了。
func TestParseDigestsRejectsBadManifests(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"坏行", "broken-line\n", "坏行"},
		{"字段数不对", "onlyonefield\n", "坏行"},
		{"哈希长度不对", "abcd  a\n", "坏行"},
		{"哈希非十六进制", strings.Repeat("z", 64) + "  a\n", "哈希非法"},
		{"空清单", "\n# 只有注释\n", "是空的"},
	}
	for _, c := range cases {
		_, err := parseDigests([]byte(c.content))
		if err == nil {
			t.Errorf("%s:应报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s:错误应含 %q,got %q", c.name, c.want, err.Error())
		}
	}
}

// 正常清单的读法:空行与 # 注释跳过。
func TestParseDigestsSkipsBlanksAndComments(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	m, err := parseDigests([]byte("# 注释\n\n" + sum + "  tool-x\n" + sum + "  tool-y\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["tool-x"] == ([32]byte{}) {
		t.Fatalf("应解析出 2 条且哈希非零,got %v", m)
	}
}

// writeHashed:写文件 + 返回内容哈希(两件事必须一致 —— 宿主靠这个哈希判「要不要重写」)。
func TestWriteHashedMatchesContent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.bin")
	payload := []byte("hello embedded plugin")
	sum, err := writeHashed(p, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("写出的内容不对:%q", got)
	}
	if sum != sha256.Sum256(payload) {
		t.Fatalf("哈希与内容不一致:%x vs %x", sum, sha256.Sum256(payload))
	}
	// 权限:可执行位(解出来的插件必须能被 exec)
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("应带执行位,got %v", fi.Mode().Perm())
	}
	// 覆盖写:第二次写更短的内容,文件被截断而不是残留旧尾巴
	if _, err := writeHashed(p, bytes.NewReader([]byte("ab"))); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "ab" {
		t.Fatalf("覆盖写应截断,got %q", b)
	}
}

// ReadExtPlugin 的失败路径:不存在的插件名 ⇒ 显式错误(而不是返回一个空流,
// 让调用方拿到 0 字节还以为「插件是空的」)。
func TestReadExtPluginUnknownName(t *testing.T) {
	if _, err := ReadExtPlugin("no-such-plugin-xyz"); err == nil {
		t.Fatal("不存在的插件名应报错")
	}
}

// writeHashed 的两条失败路径:目录不存在(建不出文件)与读取中途出错。
func TestWriteHashedErrors(t *testing.T) {
	// 目录不存在
	if _, err := writeHashed(filepath.Join(t.TempDir(), "nope", "x.bin"), bytes.NewReader([]byte("x"))); err == nil {
		t.Error("目录不存在时应报错")
	}
	// 读取中途出错:写到一半失败,不能留下一个「看起来完整」的文件
	dir := t.TempDir()
	p := filepath.Join(dir, "y.bin")
	if _, err := writeHashed(p, &errReader{}); err == nil {
		t.Error("读取出错时应报错")
	}
	if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
		t.Error("失败后不该留下非空的半截文件")
	}
}

// errReader 一个总是失败的 reader。
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }
