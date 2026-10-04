// prebuilt.go:`--prebuilt` —— 把**陌生构建脚本**移出你的机器(批三,2026-10-03)。
//
// 它解决的是这个模型下最有效的一条安全措施(不是易用性):装一个插件原本要在**你的机器上**
// 跑作者声明的任意 shell 命令。上一批已把构建环境与默认命令收口(GOFLAGS readonly、不主动
// tidy、凭据清洗),但那仍然是任意 shell —— 构建脚本还能读你磁盘上的文件。
// `--prebuilt` 把这一格**整个消掉**:只下载作者发布的产物,本机可以没有 go / node / sh。
//
// 形态是 manifest 里的一个**可选段**(作者不写就不允许 `--prebuilt`,不给「以为走了预编译
// 其实走了源码构建」留缝):
//
//	prebuilt:
//	  darwin/arm64: https://github.com/a/b/releases/download/v1.2.0/tool-demo-darwin-arm64
//	  linux/amd64:  https://…
//
// **与批一联动**:`prebuilt:` 段是 manifest 的一部分,而 manifest 是 clone 下来的 ⇒
// **tag 漂移守卫在下载之前就已经生效**。一个被顶替的 tag 给出的预编译 URL 同样会被拦。
//
// ⚠️ **诚实的代价**:下载源由**作者声明**,所以「装的那一刻」没有独立校验 —— 这是不做签名的
// 直接后果,不是可以绕过的实现瑕疵。缓解只有一条:装完的哈希进 `SHA256SUMS`,**装完再换会被
// 下一次加载挡住**。这段话同时进 README 与确认文案。
//
// 第一版**只装当前平台**(不选架构):预编译包的第一使用者要的是「我不用装 Go」,
// 而不是「我在一台机器上管理七份产物」。
package install

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// maxPrebuiltBytes 下载体积上限。
//
// 为什么要有上限:URL 由作者声明,一个 8 GB 的"预编译产物"会把磁盘写满 —— 而磁盘写满的
// 后果(装不上别的插件、配置写不进去、日志写不进)远比"这次安装失败"严重。
const maxPrebuiltBytes = 512 << 20 // 512 MiB

// prebuiltDownloadTimeout 单次下载的总时限。
//
// 为什么不是「无超时」:一个不响应的 CDN 会让 `gah -install` 永远挂着,用户只能强杀 ——
// 而强杀之后进程状态不明。设一个上限,超了报一条能看懂的错。
const prebuiltDownloadTimeout = 10 * time.Minute

// prebuiltFor 当前平台的预编译条目(url);没有 ⇒ ok=false。
func (m Manifest) PrebuiltFor(goos, goarch string) (string, bool) {
	u := strings.TrimSpace(m.Prebuilt[goos+"/"+goarch])
	return u, u != ""
}

// prebuiltPlatforms 声明过的平台列表(排序;给「你这台没有」的报错用)。
func (m Manifest) PrebuiltPlatforms() []string {
	out := make([]string, 0, len(m.Prebuilt))
	for k, v := range m.Prebuilt {
		if strings.TrimSpace(v) != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// prebuiltDownloadURL 取当前平台的 URL;没有则**显式报错**并列出声明过的平台。
//
// 为什么必须显式报错而不是回退到源码构建:用户点了 `--prebuilt` 就是为了**不跑作者的
// 构建脚本**。悄悄回退等于把这个开关变成一个谎言,而且是在用户最不该被蒙的时刻。
func (m Manifest) prebuiltDownloadURL() (string, error) {
	key := runtime.GOOS + "/" + runtime.GOARCH
	if u, ok := m.PrebuiltFor(runtime.GOOS, runtime.GOARCH); ok {
		if err := checkPrebuiltURL(u); err != nil {
			return "", err
		}
		return u, nil
	}
	declared := m.PrebuiltPlatforms()
	if len(declared) == 0 {
		return "", fmt.Errorf("install: 这个插件的 plugin.yaml 没有 prebuilt 段。" +
			"「--prebuilt」只走作者发布的预编译产物,不会退回源码构建(那要跑仓库里的任意 shell)。" +
			"去掉 --prebuilt 就按源码构建")
	}
	return "", fmt.Errorf("install: 这个插件没有声明当前平台的预编译产物(%s),声明过的是 %s。"+
		"「--prebuilt」不会退回源码构建 —— 那要跑仓库里的任意 shell,不正是你加这个开关要避开的事。"+
		"去掉 --prebuilt 就按源码构建",
		key, strings.Join(declared, "、"))
}

// checkPrebuiltURL 只收 http/https。
//
// 为什么不让 file:// 与其它 scheme:scheme 由作者决定,而 `file:///etc/shadow` 这类地址
// 会把「下载一个产物」变成「读你本机的任意文件并装成可执行插件」。收窄到 http/https 之后,
// 至少来源一定是网络上一个可被用户点开核对的地址。
func checkPrebuiltURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("install: prebuilt URL 解析失败 %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("install: prebuilt 只接受 http/https URL(收到 %q)—— "+
			"scheme 由作者声明,而 file:// 之类会把「下载产物」变成「读你本机的任意文件」", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("install: prebuilt URL 缺主机名: %q", raw)
	}
	return nil
}

// downloadAndVerify 下载 + 校验产物到 dst。**完全不调用构建**。
//
// 批七起 `--install-artifact` 也走这一份:预编译路与「装一个别人构建好的产物」
// 在下载/校验这件事上**完全相同**,分成两份实现只会漂(而漂了的后果是某条路径
// 少验了架构魔数)。
//
// 校验三条(各自独立报错,因为它们代表三件不同的事):
//   - 体积:非空 + 不超上限;
//   - 架构魔数:文件名写 amd64 而内容是 arm64,是最容易犯也最难查的一类错 ——
//     症状是「装上了,一调就 exec format error」,用户会以为是插件坏了;
//   - 可执行位(unix):缺了它装完同样 exec 不起来,而这个可以在装之前就说清。
func downloadAndVerify(rawURL, dst string) error {
	if err := checkPrebuiltURL(rawURL); err != nil {
		return err
	}
	client := &http.Client{
		Timeout: prebuiltDownloadTimeout,
		// 跳转后仍然只允许 http/https:首跳合法、末跳跳到 file:// 是一样的事。
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("跳转超过 10 次")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("跳转到了非 http/https 的 %q", req.URL.Scheme)
			}
			return nil
		},
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return fmt.Errorf("install: 下载 %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("install: 下载 %s:HTTP %d", rawURL, resp.StatusCode)
	}
	if err := writeLimited(dst, resp.Body, maxPrebuiltBytes); err != nil {
		return fmt.Errorf("install: 下载 %s: %w", rawURL, err)
	}
	if err := os.Chmod(dst, 0o755); err != nil {
		return err
	}
	return verifyArch(dst)
}

// writeLimited 落盘并封顶体积。
//
// 三条报错分开说,因为它们是三件不同的事:空的(发布链接指到了空文件/被墙返回了 200 但无内容)、
// 超限的(一个 8 GB 的"产物"会把磁盘写满,而磁盘写满的后果远重于这次安装失败)、
// 读失败的(网络中断)。
func writeLimited(dst string, r io.Reader, limit int64) error {
	// 0700 起:这个文件马上就要被执行,不该有一瞬间是别人可写的。
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		return err
	}
	defer f.Close()
	// 多读 1 字节用来判「超限」而不是「恰好等于上限」。
	n, err := io.CopyN(f, r, limit+1)
	if err != nil && err != io.EOF {
		return err
	}
	if n == 0 {
		return fmt.Errorf("产物是空的")
	}
	if n > limit {
		return fmt.Errorf("产物超过体积上限 %d 字节", limit)
	}
	return f.Close()
}

// verifyArch 校验产物的架构魔数(参照 scripts/gen-extplugins.sh 的 assert_arch)。
//
// 为什么**必须**验:预编译包最容易犯的错是"文件名/URL 写 amd64、里面是 arm64"。
// 不验的后果是「装上了,一调就 exec format error」—— 用户会以为是插件坏了,
// 而真实原因在两个系统之外,排查成本极高。
func verifyArch(path string) error {
	got, err := archOf(path)
	if err != nil {
		return fmt.Errorf("install: 校验产物架构: %w", err)
	}
	want := runtime.GOOS + "/" + runtime.GOARCH
	if got != want {
		return fmt.Errorf("install: 产物架构不符:需要 %s,这个包是 %s(%s)。"+
			"作者多半把下载链接配错了 —— 换掉它,或去掉 --prebuilt 按源码构建",
			want, got, filepath.Base(path))
	}
	return nil
}

// archOf 读文件头判断 "<os>/<arch>"。
//
// 魔数与偏移照抄 gen-extplugins.sh(ELF e_machine@18 / Mach-O LE64 cputype@4 /
// PE e_lfanew@0x3C → "PE\0\0" → machine@+4),两份实现漂了就会出现"脚本说对、宿主说错"。
func archOf(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hdr := make([]byte, 0x100)
	n, err := io.ReadFull(f, hdr)
	if err != nil && n == 0 {
		return "", fmt.Errorf("文件太短,读不出魔数: %w", err)
	}
	hdr = hdr[:n]
	switch {
	case len(hdr) >= 4 && hdr[0] == 0x7f && hdr[1] == 'E' && hdr[2] == 'L' && hdr[3] == 'F':
		if len(hdr) < 20 {
			return "", fmt.Errorf("ELF 头不完整")
		}
		mach := binary.LittleEndian.Uint16(hdr[18:20])
		switch mach {
		case 0x3e:
			return "linux/amd64", nil
		case 0xb7:
			return "linux/arm64", nil
		}
		return fmt.Sprintf("linux/0x%x", mach), nil
	case len(hdr) >= 8 && hdr[0] == 0xcf && hdr[1] == 0xfa && hdr[2] == 0xed && hdr[3] == 0xfe:
		// Mach-O little-endian 64(cputype 在 +4,4 字节小端)
		if len(hdr) < 8 {
			return "", fmt.Errorf("Mach-O 头不完整")
		}
		cpu := binary.LittleEndian.Uint32(hdr[4:8])
		switch cpu {
		case 0x01000007:
			return "darwin/amd64", nil
		case 0x0100000c:
			return "darwin/arm64", nil
		}
		return fmt.Sprintf("darwin/0x%x", cpu), nil
	case len(hdr) >= 2 && hdr[0] == 'M' && hdr[1] == 'Z':
		if len(hdr) < 0x40 {
			return "", fmt.Errorf("PE 头不完整")
		}
		off := binary.LittleEndian.Uint32(hdr[0x3c:0x40])
		start := int(off) + 4 // PE\0\0 之后才是 machine
		if start+2 > len(hdr) {
			return "", fmt.Errorf("PE 头指向越界(e_lfanew=%d)", off)
		}
		mach := binary.LittleEndian.Uint16(hdr[start : start+2])
		switch mach {
		case 0x8664:
			return "windows/amd64", nil
		case 0xaa64:
			return "windows/arm64", nil
		case 0x01c4:
			return "windows/arm", nil
		}
		return fmt.Sprintf("windows/0x%x", mach), nil
	}
	return "unknown/unknown", nil
}
