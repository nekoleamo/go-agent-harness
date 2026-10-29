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
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
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

// platformKey 清单里的平台键(沿用旧壳的 updater 键命名,便于镜像脚本复用)。
func platformKey() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	return runtime.GOOS + "-" + arch
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

// desktopProvider gah 自己的更新源 provider(实现 updater.Provider)。
type desktopProvider struct {
	// client 取清单(几 KB 的 JSON):总超时 30s,失败要尽快落到下一个源。
	client *http.Client
	// dl 下载产物(30–45MB):**不设总超时** —— http.Client.Timeout 覆盖整个响应体读取,
	// 慢网下 30s 会把一次正常下载判成失败(客户端看起来在「下载失败」,而其实只是慢)。
	// 兜底是调用方给的 ctx(安装流程 30 分钟)。
	dl *http.Client

	mu        sync.Mutex
	badSource string // 失败记忆:上次「表能取到、包下不下来」的源,下次垫到最后
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
func (p *desktopProvider) Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error) {
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
		return &updater.Release{
			Version:     m.Version,
			Notes:       m.Notes,
			PublishedAt: parsePubDate(m.PubDate),
			Artifact:    updater.Artifact{Filename: art.File, Size: art.Size, Platform: req.Platform, Arch: req.Arch},
			Verification: &updater.Verification{
				DigestAlgo:    "sha256",
				Digest:        digest,
				SignatureAlgo: "ed25519",
				Signature:     sig,
			},
			Metadata: map[string]any{"url": art.URL, "source": src},
		}, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的更新源")
	}
	return nil, lastErr
}

// Download 流式下载产物(带进度回调)。
func (p *desktopProvider) Download(ctx context.Context, r *updater.Release, dst io.Writer, onProgress func(written, total int64)) error {
	url, _ := r.Metadata["url"].(string)
	if url == "" {
		return fmt.Errorf("release 缺少下载地址")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := p.dl.Do(req)
	if err != nil {
		// 「表能取到、包下不下来」正是失败记忆要治的那半段路。
		p.rememberBadSource(r)
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		p.rememberBadSource(r)
		return fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, 64<<10)
	var written int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			written += int64(n)
			onProgress(written, resp.ContentLength)
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			// 读一半断掉(代理超时/截断)同样属于「这个源下不来包」,记下来下次垫后。
			p.rememberBadSource(r)
			return rerr
		}
	}
}

// rememberBadSource 记录「这个源下不来包」(下次检查先试另一个源)。
func (p *desktopProvider) rememberBadSource(r *updater.Release) {
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

// initUpdater 装配更新器(在 app.Run() 之前调用)。
func (s *shell) initUpdater() error {
	p := newDesktopProvider()
	cfg := updater.Config{
		CurrentVersion: strings.TrimPrefix(version, "v"),
		Providers:      []updater.Provider{p},
	}
	// 测试/CI 钩子:构建期注入是常态,但自动化用例需要能临时换公钥。
	if v := os.Getenv("GAH_DESKTOP_UPDATE_PUBKEY"); v != "" {
		updatePubKeyHex = v
	}
	if updatePubKeyHex != "" {
		key, err := hex.DecodeString(updatePubKeyHex)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("内置更新公钥非法(需 32 字节 hex): %v", err)
		}
		cfg.PublicKey = key
	} else {
		// 没有公钥 = 只校验摘要。显式说明,不让它悄悄弱化。
		shellLogf("更新: 未内置验签公钥,本次仅校验 sha256 摘要(发行构建应注入 updatePubKeyHex)")
	}
	if err := s.app.Updater.Init(cfg); err != nil {
		return err
	}
	s.updater = p
	shellLogf("更新器已就绪: current=%s 源=%v", cfg.CurrentVersion, updateSources())
	return nil
}

// explainUpdateError 把更新失败的原始错误翻成人话(旧 Rust 壳 explainUpdateError 等价)。
//
// 为什么要翻:失败原文形如 `updater: gah-desktop: HTTP 404`、`ed25519 signature did not verify`,
// 用户看不懂也不知道该找谁。这里只做**归类**,不吞掉原文(原文追加在括号里,便于反馈)。
func explainUpdateError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "404"):
		return "更新源上没有这个版本的清单(可能还没发布完):" + msg
	case strings.Contains(lower, "signature"):
		return "更新包验签失败(发布包与内置公钥不匹配,可能被替换或签名错了):" + msg
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
