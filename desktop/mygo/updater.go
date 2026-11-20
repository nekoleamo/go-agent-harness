// updater:桌面壳自更新(用 Wails v3 的 updater 机制 + gah 自己的清单格式)。
//
// 为什么不用现成的 provider(github/keygen/appcast):
//   - gah 的升级源是**两个**自持清单(Gitee 镜像优先、GitHub 兜底),而且带「失败记忆」
//     (某个源「表能取到、包下不下来」时下次先试另一个)—— 旧 Rust 壳的 update_source.rs
//     就是干这个的,换壳不能把这段判断丢掉;
//   - 清单格式自己定 ⇒ 一个 `go run` 的小工具就能签名发版,不需要 wails3 CLI(那玩意儿
//     装一次要 1.2GB,而省磁盘正是这次迁移的起因之一)。
//
// 安全模型(与旧壳一致,只是签名对象按 wails 的口径改):
//   - 清单里每个产物带 sha256 + ed25519(对 **sha256 摘要** 签名);
//   - 公钥**内置在壳二进制里**(构建时注入),清单自己无法替换信任根;
//   - Config.PublicKey 一旦设置,框架强制验签(fail closed):剥掉签名、退化成只校验摘要
//     的发布包会被拒绝。
//
// 产物形态:wails updater 只吃 单文件 / .zip(唯一顶层条目)/ .tar.gz;.dmg/.msi 不支持。
// 所以 mac 发 `.app.zip`、win 发「单 exe 的 zip」,首次安装仍走 dmg/NSIS。
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// updatePubKeyHex 内置的验签公钥(hex)。构建时用
// `-ldflags "-X main.updatePubKeyHex=..."` 注入;为空表示未配置(退化为仅摘要校验)。
var updatePubKeyHex = ""

// 默认清单地址:Gitee 镜像优先(国内直连快),GitHub 兜底。
// 两份表内容**不同** —— 各自把自己那份产物的下载地址写进去,所以「选中哪份表」就等于
// 「从哪个源下载」,一次选择同时解决取表与下载两跳(旧壳同此设计)。
const (
	defaultManifestGitee  = "https://gitee.com/null_593_5354/go-agent-harness/raw/master/desktop-latest.json"
	defaultManifestGitHub = "https://github.com/nekoleamo/go-agent-harness/releases/latest/download/desktop-latest.json"
)

// updateManifest 清单文件(自己定,简单到一眼能看完)。
type updateManifest struct {
	Version   string                    `json:"version"`
	Notes     string                    `json:"notes,omitempty"`
	PubDate   string                    `json:"pub_date,omitempty"`
	Artifacts map[string]updateArtifact `json:"artifacts"`
}

// updateArtifact 一个平台的产物。
type updateArtifact struct {
	URL    string `json:"url"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256"` // 十六进制
	Sig    string `json:"sig"`    // base64(ed25519 对 sha256 摘要的签名)
	File   string `json:"filename,omitempty"`
}

// versionNewer 判断 remote 是否比 current 新(容忍 "v" 前缀、预发布后缀与 build 元数据)。
// 不引 semver 依赖:gah 的版本就是 tag,逐段数字比较足够,且行为一眼可验。
//
// 预发布口径(2026-10-30 补):**核心号相同时,正式版比预发布版新** ——
// 0.8.0 > 0.8.0-preview。缺了这条,预发布用户升到正式版时会被判「已是最新」,
// 于是「0.8.0-preview 发出去、0.8.0 发不出来」,而界面只会说「当前已是最新」。
func versionNewer(remote, current string) bool {
	a, aPre := parseVer(remote)
	b, bPre := parseVer(current)
	// 通道口径(2026-11-06 用户决定):**稳定版只追稳定版**。
	//
	// 为什么单列一条而不是靠版本号比较:预览版的核心号往往更大(0.9.0-preview > 0.8.9),
	// 光比号码会把它推给稳定版用户。而「正式版」这个发布语义是有承诺的 —— 稳定版用户不该
	// 被自动推到预览通道;他们装了预览之后,下一个正式版的版本号还往往更小,对账会更绕。
	//
	// 预览版用户不设限:同核心号的正式版比预览版新(见文件末尾那条规则),所以预览用户会被
	// 正常引导回正式版。"dev"(本地开发构建,解析不出核心号)同样不设限 —— 它本来就不是发布版。
	if len(b) > 0 && !bPre && aPre {
		return false
	}
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	// 核心号相同:只有「当前是预发布、远端是正式版」才算更新。
	return bPre && !aPre
}

// parseVer 取版本号的核心数字段,并标出是否为预发布。
//
//	"v0.7.4-3-gabc" → [0,7,4], true(-rc/-preview/describe 后缀都算预发布)
//	"0.8.0+build"   → [0,8,0], false(+ 是 build 元数据,不影响新旧)
func parseVer(v string) (core []int, pre bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, pre = v[:i], true
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out, pre
}

// newDesktopProvider 造 provider:清单与下载各用一个客户端(理由见 desktopProvider 字段注释)。
func newDesktopProvider() *desktopProvider {
	return &desktopProvider{
		client: &http.Client{Timeout: 30 * time.Second},
		dl:     &http.Client{},
	}
}

// updateSources 本次使用的清单地址(env 可覆盖,便于本地/CI 演练)。
func updateSources() []string {
	if v := os.Getenv("GAH_DESKTOP_UPDATE_SOURCES"); v != "" {
		var out []string
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{defaultManifestGitee, defaultManifestGitHub}
}

// desktopProvider gah 自己的更新源 provider(清单 + 下载 + 换源,壳内自有实现)。
type desktopProvider struct {
	// client 取清单(几 KB 的 JSON):总超时 30s,失败要尽快落到下一个源。
	client *http.Client
	// dl 下载产物(30–45MB):**不设总超时** —— http.Client.Timeout 覆盖整个响应体读取,
	// 慢网下 30s 会把一次正常下载判成失败(客户端看起来在「下载失败」,而其实只是慢)。
	// 兜底是调用方给的 ctx(安装流程 30 分钟)。
	dl *http.Client

	mu        sync.Mutex
	badSource string // 失败记忆:上次「表能取到、包下不下来」的源,下次垫到最后

	// digestOnly 壳里没内置验签公钥(开发构建)。此时**不能**把清单里的 sig 填进
	// Verification:wails 的口径是「有签名但没配公钥 ⇒ 直接拒」(verify.go 的
	// runVerification),那会让开发构建永远装不上更新,而日志还写着「仅校验摘要」。
	digestOnly bool
}

// Name 稳定的短名(事件与日志里出现)。
func (p *desktopProvider) Name() string { return "gah-desktop" }

// ordered 按优先级排序清单地址(失败记忆的源垫后)。
func (p *desktopProvider) ordered() []string {
	srcs := updateSources()
	p.mu.Lock()
	bad := p.badSource
	p.mu.Unlock()
	if bad == "" {
		return srcs
	}
	out := make([]string, 0, len(srcs))
	for _, s := range srcs {
		if s != bad {
			out = append(out, s)
		}
	}
	for _, s := range srcs {
		if s == bad {
			out = append(out, s)
		}
	}
	return out
}

// rememberBad 记下失败的源(下次垫后)。
func (p *desktopProvider) rememberBad(src string) {
	p.mu.Lock()
	p.badSource = src
	p.mu.Unlock()
}

// Check 取清单并挑出本平台产物;已是最新返回 (nil, nil)。
func (p *desktopProvider) Check(ctx context.Context, req checkRequest) (*updateRelease, error) {
	key := req.Platform + "-" + normalizeArch(req.Arch)
	var lastErr error
	for _, src := range p.ordered() {
		m, err := p.fetchManifest(ctx, src)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", src, err)
			continue // 端点不可达 → 自动落到下一个源
		}
		art, ok := m.Artifacts[key]
		if !ok {
			lastErr = fmt.Errorf("%s: 清单里没有 %s 的产物", src, key)
			continue
		}
		if !versionNewer(m.Version, req.CurrentVersion) {
			shellLogf("更新: 已是最新(remote=%s current=%s)", m.Version, req.CurrentVersion)
			return nil, nil
		}
		digest, err := hex.DecodeString(art.SHA256)
		if err != nil || len(digest) != sha256.Size {
			return nil, fmt.Errorf("清单里的 sha256 非法: %q", art.SHA256)
		}
		sig, err := base64.StdEncoding.DecodeString(art.Sig)
		if err != nil {
			return nil, fmt.Errorf("清单里的签名不是合法 base64: %w", err)
		}
		shellLogf("更新: 发现 %s(来源 %s)", m.Version, src)
		verification := &updateVerification{Digest: digest}
		if !p.digestOnly {
			verification.Signature = sig
		}
		return &updateRelease{
			Version:      m.Version,
			Notes:        m.Notes,
			PublishedAt:  parsePubDate(m.PubDate),
			Artifact:     updateArtifactInfo{Filename: art.File, Size: art.Size, Platform: req.Platform, Arch: req.Arch},
			Verification: verification,
			Metadata:     map[string]any{"url": art.URL, "source": src},
		}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的更新源")
	}
	return nil, lastErr
}

// Download 流式下载产物(带进度回调)。
//
// 换源重试(2026-11-02 补):Gitee 只保留最近两个 release 的附件,清单里指着的版本
// 一旦被清理,那个源就**注定**下不动(实测 v0.8.0-preview 的 .app.zip 已 404,而
// 清单还指着它)—— 失败记忆只是把这个源垫到下次检查的后面,用户当下这次升级仍然
// 走不通。所以主源在「一个字节都还没落盘」的阶段失败时,去另一个清单里找**同一个包**
// (同版本 + 同平台键 + 同 sha256)再下一次。
func (p *desktopProvider) Download(ctx context.Context, r *updateRelease, dst io.Writer, onProgress func(written, total int64)) error {
	url, _ := r.Metadata["url"].(string)
	if url == "" {
		return fmt.Errorf("release 缺少下载地址")
	}
	written, err := p.stream(ctx, url, dst, onProgress)
	if err == nil {
		return nil
	}
	// 已经落盘就不能换源:换了等于把两份字节拼进同一个文件,而 wails 的摘要是
	// 在下载流里边写边算的 ⇒ 摘要必然不符,错误会跑到下游、更难解释。
	if written > 0 {
		p.rememberBadSource(r)
		return err
	}
	alts := p.altURLs(ctx, r)
	if len(alts) > 0 {
		shellLogf("更新: 主源下载失败(%v),换源重试同一个包(sha256 一致才算)", err)
	}
	for _, alt := range alts {
		w, aerr := p.stream(ctx, alt, dst, onProgress)
		if aerr == nil {
			shellLogf("更新: 已从备用源下载成功: %s", alt)
			return nil
		}
		if w > 0 {
			return aerr
		}
	}
	// 「表能取到、包下不下来」正是失败记忆要治的那半段路。
	p.rememberBadSource(r)
	return err
}

// stream 下载一个地址。返回值 written 是**已写进 dst 的字节数**,调用方靠它判断
// 「是不是还能安全换源」(0 = dst 还没被动过)。
func (p *desktopProvider) stream(ctx context.Context, url string, dst io.Writer, onProgress func(written, total int64)) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := p.dl.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 64<<10)
	var written int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return written, werr
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, resp.ContentLength)
			}
		}
		if rerr == io.EOF {
			return written, nil
		}
		if rerr != nil {
			// 读一半断掉(代理超时/截断)同样属于「这个源下不来包」,记下来下次垫后。
			return written, rerr
		}
	}
}

// altURLs 其他清单里的**同一个包**。判定只认 sha256:换源换的必须是同一份字节,
// 只按版本号认等于把「下载失败」换成「验签失败」,用户更看不懂。
func (p *desktopProvider) altURLs(ctx context.Context, r *updateRelease) []string {
	if r.Verification == nil || len(r.Verification.Digest) == 0 || r.Artifact.Platform == "" {
		return nil
	}
	want := hex.EncodeToString(r.Verification.Digest)
	key := r.Artifact.Platform + "-" + normalizeArch(r.Artifact.Arch)
	self, _ := r.Metadata["source"].(string)
	seen := map[string]bool{}
	if u, _ := r.Metadata["url"].(string); u != "" {
		seen[u] = true
	}
	var out []string
	for _, src := range p.ordered() {
		if src == self {
			continue
		}
		m, err := p.fetchManifest(ctx, src)
		if err != nil || m.Version != r.Version {
			continue
		}
		art, ok := m.Artifacts[key]
		if !ok || art.URL == "" || seen[art.URL] || !strings.EqualFold(art.SHA256, want) {
			continue
		}
		seen[art.URL] = true
		out = append(out, art.URL)
	}
	return out
}

// rememberBadSource 记录「这个源下不来包」(下次检查先试另一个源)。
func (p *desktopProvider) rememberBadSource(r *updateRelease) {
	if r == nil {
		return
	}
	if src, ok := r.Metadata["source"].(string); ok && src != "" {
		p.rememberBad(src)
	}
}

// fetchManifest 拉一份清单。
func (p *desktopProvider) fetchManifest(ctx context.Context, src string) (*updateManifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var m updateManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("清单解析失败: %w", err)
	}
	if m.Version == "" || len(m.Artifacts) == 0 {
		return nil, fmt.Errorf("清单缺字段(version/artifacts)")
	}
	return &m, nil
}

// normalizeArch 把 Go 的 GOARCH 归一成清单里的拼法(aarch64/x86_64)。
func normalizeArch(arch string) string {
	switch arch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return arch
}

func parsePubDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// —— 以下为壳内自有更新器(不依赖框架):清单→下载校验→落位→重启 ——

// updateRelease 一次「发现新版本」的结论(自持类型,取代 wails updater 的 Release)。
type updateRelease struct {
	Version      string
	Notes        string
	PublishedAt  time.Time
	Artifact     updateArtifactInfo
	Verification *updateVerification
	Metadata     map[string]any // url / source(源地址与清单来源,换源重试用)
}

// updateArtifactInfo 产物元信息(清单里那一条)。
type updateArtifactInfo struct {
	Filename string
	Size     int64
	Platform string
	Arch     string
}

// updateVerification 校验材料:sha256 摘要 + 可选 ed25519 签名(对摘要签名)。
type updateVerification struct {
	Digest    []byte
	Signature []byte
}

// checkRequest 检查请求(当前版本 + 平台)。
type checkRequest struct {
	CurrentVersion string
	Platform       string
	Arch           string
}

// 更新器状态(控制面原样报给页面;前端只认 available 与几个终态)。
const (
	updateIdle        = "idle"
	updateAvailable   = "available"
	updateDownloading = "downloading"
	updateReady       = "ready"
	updateFailed      = "failed"
)

// updater 壳侧更新器。
//
// 职责边界(与旧壳同口径):清单解析/换源/下载/校验在壳里;真正换文件交给**安装器进程**,
// 因为 Windows 不允许覆盖运行中的 exe、macOS 的 .app 也不能在自己运行时被替换。
// 本文件只负责到「安装器已接手」为止(Restart 负责退出,让安装器接管)。
type updater struct {
	p   *desktopProvider
	key ed25519.PublicKey // nil = 未内置公钥(开发构建):仅校验摘要

	mu    sync.Mutex
	last  *updateRelease
	state string
	msg   string
}

// State 当前状态(控制面 /shell/update-state)。
func (u *updater) State() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.state == "" {
		return updateIdle
	}
	return u.state
}

// setState 记状态与一句话结论。
func (u *updater) setState(state, msg string) {
	u.mu.Lock()
	u.state, u.msg = state, msg
	u.mu.Unlock()
}

// newUpdater 装配更新器(App.Run 之前调用;失败由调用方决定是否禁用更新)。
func newUpdater() (*updater, error) {
	u := &updater{p: newDesktopProvider(), state: updateIdle}
	if v := os.Getenv("GAH_DESKTOP_UPDATE_PUBKEY"); v != "" {
		updatePubKeyHex = v
	}
	if updatePubKeyHex != "" {
		key, err := hex.DecodeString(updatePubKeyHex)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("内置更新公钥非法(需 32 字节 hex): %v", err)
		}
		u.key = key
	} else {
		// 没有公钥 = 只校验摘要。provider 必须同步按这个口径出清单(否则「有签名没公钥」
		// 会被判成验签失败,开发构建永远装不上更新)。
		u.p.digestOnly = true
		shellLogf("更新: 未内置验签公钥(开发构建),本次仅校验 sha256 摘要:能装更新,但不校验出版方签名")
	}
	shellLogf("更新器已就绪: current=%s 源=%v", strings.TrimPrefix(version, "v"), updateSources())
	return u, nil
}

// Check 取清单并挑出本平台产物;已是最新返回 (nil, nil)。
func (u *updater) Check(ctx context.Context) (*updateRelease, error) {
	req := checkRequest{
		CurrentVersion: strings.TrimPrefix(version, "v"),
		Platform:       runtime.GOOS,
		Arch:           runtime.GOARCH,
	}
	rel, err := u.p.Check(ctx, req)
	if err != nil {
		u.setState(updateFailed, explainUpdateError(err))
		return nil, err
	}
	if rel == nil {
		u.setState(updateIdle, "已是最新")
		return nil, nil
	}
	u.mu.Lock()
	u.last = rel
	u.mu.Unlock()
	u.setState(updateAvailable, "")
	return rel, nil
}

// DownloadAndInstall 下载 → 验签 → 解包 → 把换文件交给安装器进程。
//
// 到「安装器已接手」为止:安装器会等本进程退出后再动文件(Windows 不允许覆盖运行中的 exe,
// macOS 的 .app 也不能在自己运行时被整包替换)。Restart 负责退出,交接在那一刻完成。
func (u *updater) DownloadAndInstall(ctx context.Context) error {
	u.mu.Lock()
	rel := u.last
	u.mu.Unlock()
	if rel == nil {
		return fmt.Errorf("没有可安装的更新(先检查更新)")
	}
	target, terr := installTarget()
	if terr != nil {
		u.setState(updateFailed, terr.Error())
		return terr
	}
	u.setState(updateDownloading, "")
	dir, err := stageUpdatePayload(ctx, u.p, rel, u.key)
	if err != nil {
		u.setState(updateFailed, explainUpdateError(err))
		return err
	}
	archive := filepath.Join(dir, safeArtifactName(rel.Artifact.Filename))
	top, err := extractUpdate(archive, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		u.setState(updateFailed, err.Error())
		return err
	}
	plan := installPlan{
		WaitPID:    os.Getpid(),
		Target:     target,
		PayloadTop: top,
		Relaunch:   true,
	}
	if err := spawnInstaller(plan, dir); err != nil {
		_ = os.RemoveAll(dir)
		u.setState(updateFailed, err.Error())
		return err
	}
	u.setState(updateReady, "")
	shellLogf("更新: %s 已就绪(安装器已接手),退出后自动替换", rel.Version)
	return nil
}

// Restart 退出应用,把交接点让给已经在等着的安装器。
//
// 刻意**不**调 App.Relaunch:mac 上 .app 正被替换,自行 relaunch 会 exec 到一个
// 正在被 rename 的路径;重启由安装器在换完之后用 LaunchServices 做。
func (u *updater) Restart(context.Context) error {
	shellLogf("更新: 退出以完成替换(安装器接手)")
	mygo.App.Quit()
	return nil
}

// safeArtifactName 把清单里的文件名收敛成一个安全的本地名(只留 basename 与常见字符)。
func safeArtifactName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		}
		return -1
	}, base)
	if base == "" || base == "." || base == ".." {
		base = "artifact.zip"
	}
	return base
}

// stageUpdatePayload 下载到临时目录、校验摘要与签名、解包,返回暂存目录。
//
// 校验顺序刻意固定为「摘要在先签名在后」:摘要是**字节是否完整**,签名是**是不是我们发的**。
// 混在一起报错会让用户分不清"下载坏了"和"包被换了"。
func stageUpdatePayload(ctx context.Context, p *desktopProvider, rel *updateRelease, key ed25519.PublicKey) (string, error) {
	if rel.Verification == nil || len(rel.Verification.Digest) != sha256.Size {
		return "", fmt.Errorf("清单缺少合法的 sha256 摘要")
	}
	dir, err := os.MkdirTemp("", "gah-update-")
	if err != nil {
		return "", fmt.Errorf("建暂存目录失败: %w", err)
	}
	// 文件名带上清单里的原始扩展名:解包按扩展名判格式(发布链只发 zip),
	// 存成固定的 artifact.bin 会让后面的解包器认不出来(2026-11-20 e2e 实测)。
	tmp := filepath.Join(dir, safeArtifactName(rel.Artifact.Filename))
	f, err := os.Create(tmp)
	if err != nil {
		return "", fmt.Errorf("建暂存文件失败: %w", err)
	}
	h := sha256.New()
	err = p.Download(ctx, rel, io.MultiWriter(f, h), nil)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	sum := h.Sum(nil)
	if !bytes.Equal(sum, rel.Verification.Digest) {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("digest mismatch: 下载到的更新包与清单摘要不一致")
	}
	if key != nil {
		if len(rel.Verification.Signature) == 0 {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("清单没有签名,而壳里内置了验签公钥(fail closed)")
		}
		if !ed25519.Verify(key, sum, rel.Verification.Signature) {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("签名校验失败: 更新包不是用发布私钥签的(可能被替换)")
		}
	}
	return dir, nil
}

// installAction 「下载并安装」这一步的准入判定结果。
type installAction string

const (
	installProceed        installAction = "proceed"
	installPortableOnly   installAction = "portable"
	installVersionChanged installAction = "versionChanged"
)

// installGate 决定这一步能不能走 —— 托盘菜单与 /shell/update-install **共用同一份**判定。
//
// 顺序:便携拒绝**先于**版本对账 —— 便携版连"要不要装"都不该问,再谈版本没有意义。
func installGate(expect, seen string, portable, writable, autoUpdateOff bool) (installAction, string) {
	if portable && (autoUpdateOff || !writable) {
		return installPortableOnly, "便携版不自动更新:请下载新包覆盖本目录"
	}
	if versionDrift(expect, seen) {
		return installVersionChanged, "更新源已变成 " + seen + "(你确认的是 " + expect + "),请重新确认后再安装"
	}
	return installProceed, ""
}

// noteSeenVersion 记下本次「检查更新」看到的远端版本(check 与 install 对账用)。
func (s *shell) noteSeenVersion(v string) {
	if v == "" {
		return
	}
	s.updateSeenMu.Lock()
	s.updateSeen = v
	s.updateSeenMu.Unlock()
}

// seenVersion 读最近一次看到的远端版本。
func (s *shell) seenVersion() string {
	s.updateSeenMu.Lock()
	defer s.updateSeenMu.Unlock()
	return s.updateSeen
}

// versionDrift 判「用户确认要装的版本」与「现在看到的版本」已不是同一个。
// 空串一律不漂移:调用方没传 expect 时不能凭这个拦人。
func versionDrift(expect, seen string) bool {
	return expect != "" && seen != "" && expect != seen
}

// explainUpdateError 把更新失败的原始错误翻成人话(旧 Rust 壳 explainUpdateError 等价)。
//
// 为什么要翻:失败原文形如 `下载返回 HTTP 404`、`签名校验失败`,用户看不懂也不知道该找谁。
// 这里只做**归类**,不吞掉原文(原文追加在后面,便于反馈)。
func explainUpdateError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(msg, "下载返回 HTTP"):
		if strings.Contains(msg, "404") {
			return "更新包在镜像源上已被清理(Gitee 只保留最近两个 release 的附件),另一个源也拿不到同一个包:请到 Release 页手动下载:" + msg
		}
		return "更新包下载失败:" + msg
	case strings.Contains(lower, "digest mismatch"):
		return "下载到的更新包与清单摘要不一致(包被替换或下载被截断):" + msg
	case strings.Contains(lower, "签名"):
		return "更新包验签失败(发布包与内置公钥不匹配,可能被替换或签名错了):" + msg
	case strings.Contains(lower, "404"):
		return "更新源上没有这个版本的清单(可能还没发布完):" + msg
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline exceeded"):
		return "连不上更新源(超时):" + msg
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "no route"):
		return "连不上更新源(网络不可达):" + msg
	case strings.Contains(lower, "no such host") || strings.Contains(lower, "dns"):
		return "连不上更新源(域名解析失败):" + msg
	case strings.Contains(lower, "没有") && strings.Contains(msg, "产物"):
		return "更新源里没有当前平台的产物:" + msg
	default:
		return msg
	}
}
