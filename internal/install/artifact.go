// artifact.go:`gah -install-artifact <url> --id <id> --name <tool-x>` ——
// 装一个**别人已经构建好的**插件二进制,本机**不需要 Go / node / 任何工具链**。
//
// 为什么要有它(批七):`--prebuilt` 已经能在**作者配合**的情况下免工具链安装(manifest
// 里声明 `prebuilt:` 段 + 加那个开关)。但长尾第三方插件基本不会写那一段 ——
// 那要求作者额外发七份产物并维护一张表。对这些插件,今天免 Go 的唯一路径是
// 「手工下载 → 扔进 plugins/ → `gah -trust-plugin`」三步手工活。
//
// 本文件把那三步收成一条命令,并把该验的都验上:
//   - 只收 http/https(与 prebuilt 同口径:`file://` 会把「下载产物」变成「读本机任意文件」);
//   - 体积上限 + **架构魔数**(照抄发行脚本 `gen-extplugins.sh` 的 assert_arch);
//   - `--id` / `--name` 走**路径注入**校验(它们要拼进落位路径);
//   - 名字用**加载器自己的判据**校验(`isExternalHostBin`),而不是另立一套;
//   - 装完照常登记哈希白名单 + 来源账 + 审计行 ⇒ 事后可追。
//
// ⚠️ **诚实的代价与 `--prebuilt` 同款**:下载地址由**用户/作者**给出,而 gah **不验签名**
// ⇒ **装的那一刻没有独立校验**。缓解只有一条:装完的哈希进 `SHA256SUMS`,
// **装完再换会被下一次加载挡住**。这段话同时进确认文案与 README。
//
// **不猜任何东西**:本文件不接受"从文件名里猜 id/平台"这种形态(批三已否决过,
// 见归档说明)。id 与名字都必须显式给 —— 猜错的形态是「装上了,工具名不是你要的那个」。
package install

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// KindArtifact 来源种类:产物是**下载来的**,既没有仓库也没有 ref(批七)。
//
// 为什么单列一种而不是塞进 KindLocal:KindLocal 的含义是「本地目录安装」,
// 而这一件的来源是一个 URL。混用会让事后翻账的人误以为它是本地目录 ——
// 而这两件事的安全含义完全不同(本地目录是你自己的代码,URL 是别人的)。
const KindArtifact = "artifact"

// idPattern / namePattern 落位路径里两个参数允许的形状。
//
// 为什么要有正则而不是"过滤危险字符":这里是**路径注入**面 —— `--id ../..` 或
// `--name ../../evil` 拼进 `filepath.Join` 就能写到 `plugins/` 外面去。
// 白名单式校验(只允许列出的字符 + 首字符非点)比黑名单可靠:黑名单永远漏一个。
var (
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// ArtifactFacts 产物安装的确认事实。
type ArtifactFacts struct {
	URL  string // 下载地址
	ID   string // 插件 id(= 落位目录名)
	Name string // 二进制基名
	Dir  string // 落位目录
}

// ConfirmPromptArtifact 产物安装的确认文案。
//
// 这段文案必须自己讲清代价:产物是**下载来的**,来源由给出 URL 的人决定,gah **不验签名**
// ⇒ 装的那一刻没有独立校验。吞掉这句话,用户就是在不知情下接受了一个可执行二进制。
func ConfirmPromptArtifact(f ArtifactFacts) string {
	return fmt.Sprintf("从下载产物安装插件 %s?\n来源:%s\n产物:%s\n落位:%s\n\n"+
		"**不会执行任何构建命令**(所以本机不需要 Go / node / make —— 这正是这个入口的用途)。\n"+
		"但要知道:下载地址是**给的人自己声明的**,gah 不验签名 ⇒ 装的那一刻没有独立校验。\n"+
		"装完的哈希会进白名单,**装完再换会被下一次加载挡住**;仅此而已。\n"+
		"若你想要签名校验的保障,当前没有 —— gah 对第三方插件不验签名,后果自担。",
		f.ID, f.URL, f.Name, f.Dir)
}

// ValidateArtifactArgs 校验两个路径参数(装之前与确认之前都可调)。
//
// 拆出来是因为**确认文案也要用到它们**:先校验再把安全的事实说给用户听,而不是
// 让用户确认一段还没验证过的路径。
func ValidateArtifactArgs(id, name string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("install-artifact: 插件 id %q 不合法。"+
			"只允许字母数字与 . _ -,必须字母/数字开头,最长 64 字符(id 会直接成为落位目录名)", id)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("install-artifact: 插件 id %q 不合法(不能含 ..)", id)
	}
	if !namePattern.MatchString(name) {
		return fmt.Errorf("install-artifact: 产物名 %q 不合法。只允许字母数字与 . _ -,最长 64 字符", name)
	}
	if !isExternalHostBin(name) {
		// 用**加载器自己的判据**,而不是另立一套:两处规则一漂,就会出现
		// "装的时候过了、加载的时候被静默跳过"。
		return fmt.Errorf("install-artifact: 产物名必须以 tool- 或 cmd- 开头(host-bridge 扫描约定),得到 %q", name)
	}
	return nil
}

// isExternalHostBin 产物名是否会被 host-bridge 当成插件二进制扫到。
//
// 判据与 host-bridge 的 isExternalPluginBin 逐字一致(tool-/cmd- 前缀)。**刻意不 import
// 那个包**:插件只 import sdk,而这里是宿主侧内核;把判据复制一份并**在注释里写明同源**
// 是这里唯一可行的做法(与 buildEnv 的口径同款)。
func isExternalHostBin(name string) bool {
	return strings.HasPrefix(name, "tool-") || strings.HasPrefix(name, "cmd-")
}

// InstallArtifact 下载一个已构建好的插件二进制并落位。**不构建、不需要工具链。**
func InstallArtifact(rawURL, id, name, home string) (*Result, error) {
	if err := ValidateArtifactArgs(id, name); err != nil {
		return nil, err
	}
	if err := checkPrebuiltURL(rawURL); err != nil {
		return nil, fmt.Errorf("install-artifact: %w", err)
	}
	pluginsDir := filepath.Join(home, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return nil, err
	}
	dir := filepath.Join(pluginsDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// 下载到同目录的临时名再改名:中断时盘上不会留下半个二进制(它会被 hash 验一遍,
	// 而一个装不上的半个文件比没有更让人困惑)。
	tmp, err := os.CreateTemp(dir, ".artifact-*.part")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath) // 成功路径上已被 rename 走,这里是兜底
	dst := filepath.Join(dir, name)
	if err := downloadAndVerify(rawURL, tmpPath); err != nil {
		return nil, err
	}
	if err := sdk.ReplaceFile(tmpPath, dst); err != nil {
		return nil, fmt.Errorf("install-artifact: 落位 %s: %w", dst, err)
	}
	// 登记白名单:登记的是**刚下下来的这一份**的哈希。我们只能担保自己装进去的这份,
	// 担保不了「作者那一份本来就好」(不验签名的直接后果,见包注释)。
	sum, err := plugintrust.HashFile(dst)
	if err != nil {
		return nil, fmt.Errorf("install-artifact: 算产物哈希失败: %w", err)
	}
	list, err := plugintrust.Load(pluginsDir)
	if err != nil {
		return nil, err
	}
	if err := list.RecordWithAudit(name, sum, "artifact-install:"+rawURL); err != nil {
		return nil, fmt.Errorf("install-artifact: 登记 %s 失败: %w", plugintrust.FileName, err)
	}
	// 来源账:按仓库记的那一套在这里**没有**仓库/ref,故记 kind=artifact + 产物 URL。
	// 「事后能翻出它是从哪个地址来的」与「它是不是会被自动更新检查」是两件事,
	// 后者靠 kind 让 CheckForUpdates 跳过(见 updatecheck.go)。
	ledger, err := LoadSources(home)
	if err != nil {
		return nil, err
	}
	entry := SourceEntry{
		Repo: rawURL, Kind: KindArtifact, PluginID: id, Origin: OriginUser,
		Prebuilt: rawURL, InstalledAt: nowRFC(),
	}
	ledger.Record(entry)
	if err := WriteSources(home, ledger); err != nil {
		return nil, fmt.Errorf("install-artifact: 写来源账失败(产物已装上,但来源没记上): %w", err)
	}
	// 装配:让 host-bridge 扫得到(与 install.Install 同一份 patch/profile 登记)。
	patch := filepath.Join(home, "config", PatchFile)
	if err := EnsurePatch(patch, Entry{
		ID: "host-bridge", Enabled: true,
		Data: map[string]any{"dir": pluginsDir, "watch": true},
	}); err != nil {
		return nil, err
	}
	if err := EnsureProfilePicks(home); err != nil {
		return nil, err
	}
	audit, _ := list.LastAuditOf(name)
	return &Result{
		ID: id, Protocol: "bridge", Binary: name, Dir: dir, Patch: patch,
		Source: rawURL, Audit: audit, Record: entry, Prebuilt: rawURL, Artifact: true,
		BuildCmd: "(未执行构建:直接安装已构建好的产物 " + rawURL + ")",
	}, nil
}
