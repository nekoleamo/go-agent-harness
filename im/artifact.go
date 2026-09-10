// 产物登记账本(MED-1 / E-B;D2 口径):把「模型能发什么文件」收窄为一组**显式登记**条目。
//
// 硬性约束(全部在运行时层):
//  1. 只登记**当前工作区内**的常规文件(realpath 前缀校验,防软链逃逸/越界外发);
//  2. 大小 ≤ 上限(默认 20 MB,`data.media_max_mb` 可配)与类型白名单(按扩展名判 image/video/voice/file);
//  3. 单次可用 + 10 分钟 TTL + 容量上限 8(登记不等于发送,过期自动清理);
//  4. 投递目标仍走已授权口径(Targets());未实现 MediaSender 的通道显式报「不支持出站文件」。
package im

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

const (
	// artifactTTL 登记有效期(未发送即过期)。
	artifactTTL = 10 * time.Minute
	// artifactCap 同时有效的登记条目上限(超出淘汰最旧)。
	artifactCap = 8
	// mediaDefaultMaxMB 出站媒体大小上限默认值(D2:20 MB)。
	mediaDefaultMaxMB = 20
)

// artifactKindByExt 扩展名 → 媒体类型(白名单:未知扩展名一律按普通文件处理,不拒绝)。
func artifactKindByExt(name string) MediaKind {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(name), ".")) {
	case "png", "jpg", "jpeg", "gif", "webp", "bmp":
		return MediaImage
	case "mp4", "mov", "m4v":
		return MediaVideo
	case "silk", "amr", "mp3", "ogg", "wav":
		return MediaVoice
	}
	return MediaFile
}

// artifact 一条登记记录。
type artifact struct {
	ID        string
	Path      string // realpath
	Name      string
	Kind      MediaKind
	Mime      string
	Size      int64
	ModTime   time.Time
	ExpiresAt time.Time
	Scope     string // 目标限定(Route.Key();空 = 任意已授权目标)
}

// artifactBook 登记账本(并发安全;容量 + TTL 双裁剪)。
type artifactBook struct {
	mu  sync.Mutex
	m   map[string]artifact
	seq []string // 登记顺序(淘汰最旧)
}

func newArtifactBook() *artifactBook {
	return &artifactBook{m: map[string]artifact{}}
}

// put 登记(同 id 覆盖;裁剪 TTL 与容量)。
func (b *artifactBook) put(a artifact) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(time.Now())
	if _, dup := b.m[a.ID]; !dup {
		b.seq = append(b.seq, a.ID)
	}
	b.m[a.ID] = a
	for len(b.seq) > artifactCap {
		old := b.seq[0]
		b.seq = b.seq[1:]
		delete(b.m, old)
	}
}

// take 取用(单次可用:命中即删除);未命中返回 ok=false。
func (b *artifactBook) take(id string, now time.Time) (artifact, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(now)
	a, ok := b.m[id]
	if !ok {
		return artifact{}, false
	}
	delete(b.m, id)
	for i, s := range b.seq {
		if s == id {
			b.seq = append(b.seq[:i], b.seq[i+1:]...)
			break
		}
	}
	return a, true
}

// restore 发送失败回滚(take 之后失败再把条目放回,允许重试)。
func (b *artifactBook) restore(a artifact) { b.put(a) }

// count 当前有效条目数(状态展示)。
func (b *artifactBook) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(time.Now())
	return len(b.m)
}

// pruneLocked 清理过期条目(调用方持锁)。
func (b *artifactBook) pruneLocked(now time.Time) {
	kept := b.seq[:0]
	for _, id := range b.seq {
		a, ok := b.m[id]
		if ok && a.ExpiresAt.After(now) {
			kept = append(kept, id)
			continue
		}
		delete(b.m, id)
	}
	b.seq = kept
}

// workspaceRoot 当前工作区根(经 ctx.sandbox;未装配 = 进程 cwd,最保守的选择)。
func (b *Bridge) workspaceRoot() string {
	if b.c != nil {
		var sb sdk.Sandbox
		if err := b.c.Inject("ctx.sandbox", &sb); err == nil && sb != nil {
			if root := strings.TrimSpace(sb.Root()); root != "" {
				return realPathOrAbs(root)
			}
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return realPathOrAbs(wd)
}

// realPathOrAbs 归一为 realpath(软链解析;失败退绝对路径)。
func realPathOrAbs(p string) string {
	if rr, err := filepath.EvalSymlinks(p); err == nil {
		return rr
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// mediaMaxBytes 出站媒体大小上限(Options 覆盖,默认 20 MB)。
func (b *Bridge) mediaMaxBytes() int64 {
	if b.mediaMaxMB > 0 {
		return int64(b.mediaMaxMB) << 20
	}
	return mediaDefaultMaxMB << 20
}

// RegisterArtifact 登记待发送产物(sdk.IMAttachmentService;幂等)。
func (b *Bridge) RegisterArtifact(_ context.Context, path string) (sdk.IMArtifact, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return sdk.IMArtifact{}, fmt.Errorf("im: 产物路径不能为空")
	}
	abs := realPathOrAbs(p)
	fi, err := os.Stat(abs)
	if err != nil {
		return sdk.IMArtifact{}, fmt.Errorf("im: 产物不可读: %w", err)
	}
	if fi.IsDir() || !fi.Mode().IsRegular() {
		return sdk.IMArtifact{}, fmt.Errorf("im: 仅支持常规文件(目录/设备文件不可发送)")
	}
	root := b.workspaceRoot()
	if root == "" {
		return sdk.IMArtifact{}, fmt.Errorf("im: 无法确定工作区根,拒绝登记")
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return sdk.IMArtifact{}, fmt.Errorf("im: 仅允许发送当前工作区内的文件(越界: %s)", filepath.Base(abs))
	}
	max := b.mediaMaxBytes()
	if fi.Size() > max {
		return sdk.IMArtifact{}, fmt.Errorf("im: 文件过大(%d 字节 > %d 字节上限;可调 data.media_max_mb)",
			fi.Size(), max)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", abs, fi.Size(), fi.ModTime().UnixNano())))
	art := artifact{
		ID:        hex.EncodeToString(sum[:8]),
		Path:      abs,
		Name:      filepath.Base(abs),
		Kind:      artifactKindByExt(abs),
		Mime:      mime.TypeByExtension(filepath.Ext(abs)),
		Size:      fi.Size(),
		ModTime:   fi.ModTime(),
		ExpiresAt: time.Now().Add(artifactTTL),
	}
	b.arts.put(art)
	return sdk.IMArtifact{
		ID: art.ID, Name: art.Name, Kind: string(art.Kind), Mime: art.Mime,
		Bytes: art.Size, ExpiresAt: art.ExpiresAt,
	}, nil
}

// SendArtifact 向已授权目标投递已登记产物。
func (b *Bridge) SendArtifact(ctx context.Context, target, artifactID string) error {
	id := strings.TrimSpace(target)
	if i := strings.Index(id, "\x00"); i >= 0 {
		id = id[i+1:]
	}
	if id == "" {
		return fmt.Errorf("im: 目标不能为空(用 im_status 查看可投目标)")
	}
	route, ok := b.routeForTarget(id)
	if !ok {
		return fmt.Errorf("im: 目标未授权或不存在: %s", id)
	}
	sender, ok := b.tr.(MediaSender)
	if !ok {
		return fmt.Errorf("im: 该渠道不支持出站文件(%s)", b.tr.Name())
	}
	art, ok := b.arts.take(strings.TrimSpace(artifactID), time.Now())
	if !ok {
		return fmt.Errorf("im: 产物未登记或已过期(先登记:RegisterArtifact;单次可用)")
	}
	// 文件可能在登记后被改动/删除 → 发送前复核(大小/mtime 一致),不一致则拒绝并提示重新登记
	fi, err := os.Stat(art.Path)
	if err != nil || fi.Size() != art.Size || !fi.ModTime().Equal(art.ModTime) {
		b.arts.restore(art)
		return fmt.Errorf("im: 产物已变更(重新登记后再发送)")
	}
	payload := MediaPayload{
		ArtifactID: art.ID, Kind: art.Kind, Path: art.Path,
		Name: art.Name, Mime: art.Mime, Size: art.Size,
	}
	if err := sender.SendMedia(ctx, route, payload); err != nil {
		b.arts.restore(art) // 失败可重试(条目还原)
		return fmt.Errorf("im: 出站媒体投递失败(%s): %w", art.Name, err)
	}
	return nil
}

// artifactCount 已登记待投产物数(状态展示)。
func (b *Bridge) artifactCount() int {
	if b.arts == nil {
		return 0
	}
	return b.arts.count()
}

// artifactIDs 诊断用:当前有效登记 id 排序快照。
func (b *Bridge) artifactIDs() []string {
	if b.arts == nil {
		return nil
	}
	b.arts.mu.Lock()
	defer b.arts.mu.Unlock()
	b.arts.pruneLocked(time.Now())
	out := make([]string, 0, len(b.arts.m))
	for id := range b.arts.m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
