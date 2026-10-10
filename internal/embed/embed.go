// Package embed 提供配置样板的内嵌资源与首启释放(对齐设计 §7.2/7.3:单二进制自包含)。
// seed/ 与仓库 config/ 保持一致(guard 测试保证);发布形态下无本地 config 也能启动。
package embed

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/sdk"
	"sort"
)

//go:embed seed
var Seed embed.FS

// FileNames 返回 seed 中的样板文件名(首启释放清单)。
func FileNames() ([]string, error) {
	return listNames(Seed, "seed")
}

func listNames(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// EnsureSeed 把 seed 样板释放到 home 的 config/ 目录。
// 版本语义:缺失写;已存在且版本一致 → 跳过(用户编辑不被覆盖);
// **seed 版本更高(bundle-*.yaml 头部 seed-version)→ 备份后覆盖**——新增基础能力条目
// (host-* 等)老用户自动升级,无需人工删样板(配置树语义:用户自定义应走 patch 层)。
// 返回释放/升级的文件名列表。
func EnsureSeed(home string) ([]string, error) {
	cfgDir := filepath.Join(home, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return nil, err
	}
	names, err := FileNames()
	if err != nil {
		return nil, err
	}
	written := []string{}

	for _, n := range names {
		dst := filepath.Join(cfgDir, n)
		raw, err := Seed.ReadFile("seed/" + n)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			if err := os.WriteFile(dst, raw, 0o644); err != nil {
				return nil, err
			}
			written = append(written, dst)
			continue
		}
		if !strings.HasPrefix(n, "bundle-") {
			continue // 非 bundle 样板(profile/patch):用户配置偏好,已有不覆盖
		}
		if seedVersion(raw) <= diskVersion(dst) {
			continue // 版本一致或更高:不覆盖(用户编辑保留)
		}
		// 版本升级:备份旧内容后覆盖(新增能力条目对老用户生效)
		old, err := os.ReadFile(dst)
		if err != nil {
			return nil, fmt.Errorf("seed 升级读取旧样板失败 %s: %w", dst, err)
		}
		bak := dst + ".bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(bak, old, 0o644); err != nil {
			return nil, fmt.Errorf("seed 升级备份失败 %s: %w", dst, err)
		}
		// 覆盖走「临时文件 + 原子替换」而不是直接 WriteFile 覆写:
		//  ① Windows:目标带**只读属性**时直接覆写被拒(ACCESS_DENIED)⇒ 老用户升级直接失败
		//     ($GAH_HOME 整体来自只读副本 / zip 解压保留属性 / 单副本备份还原,这三者都不罕见);
		//  ② 任何平台:写到一半崩了会留下**半截样板**,而 boot 后续读它会得到坏配置。
		if err := writeSeedAtomic(dst, raw); err != nil {
			return nil, err
		}
		written = append(written, dst)
	}
	return written, nil
}

// writeSeedAtomic 同目录临时文件 → sdk.ReplaceFile 落盘(见调用处的两条理由)。
func writeSeedAtomic(dst string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".seed-tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := sdk.ReplaceFile(tmpPath, dst); err != nil {
		return err
	}
	tmpPath = "" // 已就位:defer 不再删(否则会把刚写好的文件删掉)
	return nil
}

// seedVersion 解析样板首部 seed-version 注释(bundle 系列;无标记 = 0)。
func seedVersion(raw []byte) int {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# seed-version:") {
			// 取首个 token(容忍行内说明:# seed-version: 1 # 备注…)
			f := strings.Fields(strings.TrimPrefix(line, "# seed-version:"))
			if len(f) > 0 {
				if v, err := strconv.Atoi(f[0]); err == nil {
					return v
				}
			}
		}
	}
	return 0
}

// diskVersion 读取落盘样板版本(无版本/不可读 = 0,视为旧版触发升级)。
func diskVersion(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return seedVersion(raw)
}

// EnsurePlugins 释放随包外部插件二进制到 home/plugins/<name>/<name[.exe]>(方案 B 首启释放)。
// P0 体积门(M7):embed 存**压缩产物**(2026-10-02 起为 zstd,.zst),释放时解压落盘;
// 自动升级:内容(sha256)与 embed 一致 → 跳过(幂等,同版/用户自装产物保留);
// 内容不同 → 覆盖(插件产物必须与主程序版本匹配,旧版能力缺失有害,如缺 web_search)。
// 不保留备份:plugins 扫描会加载任何 tool-* 前缀文件(host-bridge),同目录备份会被误加载;
// 二进制随包可再生,无保留价值。
// ReadExtPlugin 读本平台某个外部插件的**解压后**二进制。
// name 为插件基名(tool-basic),平台扩展名由本函数补:调用方不必关心平台差异。
// P4 平台匹配:build-tag 保证只取当前构建平台的产物(黑盒测试/工具链读取用)。
//
// 2026-10-02:由 `OpenExtPlugin(io.ReadCloser)` 改名并改成返回字节。旧形态返回的是
// **压缩流**,名字却像「打开二进制」,于是每个调用方都得自己再解一层 —— 这正是
// tests/ 里那段多余 gzip.NewReader 的来历。压缩格式换代(gzip→zstd)时,那一层在
// 调用方还得跟着改一次;改成「读出解压后的字节」之后,格式是实现细节,调用方不再感知。
func ReadExtPlugin(name string) ([]byte, error) {
	if err := checkPlatformEmbedded(); err != nil {
		return nil, err
	}
	f, err := extPlugins.Open(extPluginDir + "/" + ExtPluginBinary(name) + ExtPluginExt)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec, err := zstd.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("internal/embed: %s 解码器初始化失败:%w", name, err)
	}
	defer dec.Close()
	return io.ReadAll(dec.IOReadCloser())
}

// ExtPluginExt 随包外部插件产物的压缩扩展名(单一事实源:生成侧 scripts/gen-extplugins.sh)。
const ExtPluginExt = ".zst"

// checkPlatformEmbedded 平台内置产物可用性(发行矩阵外 → 显式错误,不静默返回空/不存在)。
func checkPlatformEmbedded() error {
	if extPluginDir == "" {
		return fmt.Errorf("internal/embed: 本平台不在内置外部插件发行矩阵内(darwin/linux × amd64/arm64 + windows/amd64);"+
			"当前 %s/%s 请自行把插件二进制放入数据根 plugins/(矩阵见 scripts/gen-extplugins.sh)", runtime.GOOS, runtime.GOARCH)
	}
	return nil
}

// ExtPluginBinary 外部插件在**当前平台**的产物文件名(压缩之前)。
//
// Windows 必须带 .exe:宿主用 exec.Command(绝对路径) 启动外部插件,而 os/exec 在 Windows
// 上走 PATHEXT 补全,findExecutable 对**无扩展名**的文件不会 stat 字面路径
// (Go 1.27 os/exec/lp_windows.go:hasExt 为假时只逐个试 exts)→ 无 .exe 的 PE 一律
// ErrNotFound,外部插件在 Windows 上根本无法启动(而 isExternalPluginBin 只按前缀匹配,
// 发现阶段看不出来)。生成侧对应 scripts/gen-extplugins.sh。
func ExtPluginBinary(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// pluginDst 产物在数据根中的落点:plugins/<目录名>/<文件名>。
// 目录名去平台扩展名(plugins/tool-basic/tool-basic.exe):目录口径跨平台统一,
// 文件名保留 .exe 以适配 Windows 的 PATHEXT 解析(见 ExtPluginBinary)。
func pluginDst(home, packedName string) string {
	bin := pluginBaseName(packedName)
	return filepath.Join(home, "plugins", strings.TrimSuffix(bin, ".exe"), bin)
}

// pluginBaseName 压缩产物名 → 插件基名(`tool-basic.exe.zst` → `tool-basic.exe`)。
func pluginBaseName(packedName string) string {
	return strings.TrimSuffix(packedName, ExtPluginExt)
}

// readFileBytes 读文件内容(不存在/读失败 ok=false,与幂等跳过区分)。
func readFileBytes(path string) ([]byte, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return raw, true
}

// 外部插件 embed 声明按平台拆在 extplugins_<os>_<arch>.go(build-tag 限定,
// 每平台文件定义同名 extPlugins/extPluginDir;主包每目标只嵌本平台产物,
// 体积门不变,发行产物平台匹配——P4 交叉编译矩阵回归)。
func EnsurePlugins(home string) ([]string, error) {
	if err := checkPlatformEmbedded(); err != nil {
		return nil, err
	}
	want, err := packedDigests()
	if err != nil {
		return nil, err
	}
	// 先清理「曾经随包、但当前版本不再随包」的旧插件(见 pruneObsolete 的注释)。
	// 必须在拿到 want 之后、释放新产物之前:否则先装新的、再移旧的,中间存在
	// 同名两份的时刻虽短,但没必要制造它。
	written := pruneObsolete(home, want)
	names, err := listNames(extPlugins, extPluginDir)
	if err != nil {
		return written, err
	}
	for _, n := range names {
		if !strings.HasSuffix(n, ExtPluginExt) {
			continue // 只处理本仓生成的压缩产物(格式换代后旧 .gz 残留会被忽略,不会被当插件加载)
		}
		bin := pluginBaseName(n)
		dst := pluginDst(home, n)
		// 稳态快路径:清单里有这个产物、磁盘上也有,且内容哈希一致 ⇒ **不解压**(旧实现
		// 每次启动都要把 4 件共 ~28 MiB 全解压一遍再比,实测 ~150ms;这一跳省掉)。
		wantSum, ok := want[bin]
		if !ok {
			return nil, fmt.Errorf("internal/embed: %s 不在 SHA256SUMS 里(产物与清单不同批;重跑 scripts/gen-extplugins.sh)", bin)
		}
		if cur, ok := readFileBytes(dst); ok && sha256.Sum256(cur) == wantSum {
			continue
		}
		f, err := extPlugins.Open(extPluginDir + "/" + n)
		if err != nil {
			return nil, err
		}
		dec, derr := zstd.NewReader(f)
		if derr != nil {
			f.Close()
			return nil, fmt.Errorf("internal/embed: %s 解码器初始化失败:%w", n, derr)
		}
		// 流式解码 + 一边写一边哈希:旧实现是 io.ReadAll 整份读进内存(4 件合计
		// ~28 MiB 解压后峰值内存),临时文件也只在这条「需要写」的路径上出现。
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			dec.Close()
			f.Close()
			return nil, err
		}
		// 写临时文件再 rename 覆盖,而不是直接 WriteFile 截断重写:
		//   ① 覆盖变成原子的(插件扫描不会撞上半截产物);
		//   ② macOS 上必须换 inode —— 实测同一份字节留在旧 inode 里会被 taskgated
		//      判「Code Signature Invalid」直接 SIGKILL(codesign -vvv 却说 valid),
		//      而 rename 出来的新 inode 同一份字节就能跑。详见 DESIGN R23。
		//   临时名以点开头且不带 tool- 前缀,即使残留也不会被插件扫描误当产物加载。
		tmp := filepath.Join(filepath.Dir(dst), ".gah-tmp-"+filepath.Base(dst))
		sum, err := writeHashed(tmp, dec.IOReadCloser())
		dec.Close()
		f.Close()
		if err != nil {
			os.Remove(tmp)
			return nil, err
		}
		// 解出来的东西必须与清单一致 —— 清单错 = 装上去的插件不是这份构建的产物。
		if sum != wantSum {
			os.Remove(tmp)
			return nil, fmt.Errorf("internal/embed: %s 解出内容与 SHA256SUMS 不一致(嵌入产物与清单不同批)", bin)
		}
		if err := sdk.ReplaceFile(tmp, dst); err != nil {
			os.Remove(tmp)
			return nil, err
		}
		written = append(written, dst)
	}
	// 把自己登记进**用户侧**插件白名单(2026-10-03)。
	//
	// 为什么必须有这一步:白名单(`plugins/SHA256SUMS`)一存在即强制,而它默认
	// **不存在**(opt-in)。若不主动登记,用户装完官方插件、第一次跑起 `gah -install-plugin`
	// 或 `-trust-plugin` 之后,清单就出现了 —— 里面却没有官方四件 ⇒ 下次启动它们全被拒。
	// 官方产物是**本仓自签**的(嵌入的 SHA256SUMS 已在上面逐件核对过),登记它不降低边界:
	// 它防的是「盘上那份被换掉」,而盘上那份每次启动都要与嵌入清单比对一次。
	if err := recordSelf(home, want); err != nil {
		// 登记失败**不阻断启动**:插件此刻已经落盘且内容与嵌入清单一致,
		// 真正的风险(有人改了盘上文件)在每次启动的嵌入比对里已经被挡住。
		// 硬失败只会让一个「想加个白名单」的用户装不上官方插件。
		return written, fmt.Errorf("internal/embed: 登记官方插件到 plugins/%s 失败(插件已释放,可稍后执行 gah -trust-plugin <名>):%w", plugintrust.FileName, err)
	}
	return written, nil
}

// OfficialPluginNames 随包官方外部插件的全部基名(排序)。
//
// 为什么需要它(而不是用 EnsurePlugins 的返回值):那个返回值是**本次新写入**的那些,
// 第二次启动时它为空 —— 用它去标注来源,官方插件就永远标不上「official」。
// 而分类展示(批四 P3)需要的是**全部**官方插件,与本次是否重写了它们无关。
func OfficialPluginNames() ([]string, error) {
	want, err := packedDigests()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// recordSelf 把官方产物登记进用户侧插件白名单(upsert,已存在的条目原样保留)。
func recordSelf(home string, want map[string][32]byte) error {
	dir := filepath.Join(home, "plugins")
	list, err := plugintrust.Load(dir)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(want))
	for n := range want {
		names = append(names, n)
	}
	sort.Strings(names)
	// 逐条登记(而非 Set 一次写完):Set 不写审计行,而这份清单是事后唯一能说清
	// 「这份官方二进制是什么时候、由谁登记」的依据。已登记过的条目仍会**每次刷新**
	// 审计时间 —— 这是有意的:官方产物每次启动都要与嵌入清单比对,盘上那份被换掉时
	// 清单会跟着记下新哈希,那条记录就是线索。
	for _, n := range names {
		sum, ok := want[n]
		if !ok {
			continue
		}
		if err := list.RecordWithAudit(n, sum, "embed"); err != nil {
			return err
		}
	}
	return nil
}

// packedDigests 读 embed 内的 SHA256SUMS(未压缩内容的哈希表:插件基名 → sha256)。
//
// 为什么清单是**必需的**而不是可选的:它同时是稳态快路径的判据、产物与构建绑定的
// 校验、以及发行校验门核对「装到磁盘上的那份是不是这份构建的」的依据。缺了它 ⇒
// 显式失败,而不是悄悄退回「每次启动全解压再比」的第二条路(两条路漂移起来没人发现)。
func packedDigests() (map[string][32]byte, error) {
	raw, err := extPlugins.ReadFile(extPluginDir + "/SHA256SUMS")
	if err != nil {
		return nil, fmt.Errorf("internal/embed: 缺 %s/SHA256SUMS(请跑 bash scripts/gen-extplugins.sh 重建产物):%w", extPluginDir, err)
	}
	return parseDigests(raw)
}

// parseDigests 解析清单正文(**纯函数**:embed.FS 里的内容不可替换,坏输入只能靠它测)。
//
// 清单是**必需**的而不是可选的:它同时是稳态快路径的判据、产物与构建绑定的校验、
// 以及发行校验门核对「装到磁盘上的那份是不是这份构建的」的依据。缺了它 ⇒ 显式失败,
// 而不是悄悄退回「每次启动全解压再比」的第二条路(两条路漂移起来没人发现)。
func parseDigests(raw []byte) (map[string][32]byte, error) {
	out := map[string][32]byte{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return nil, fmt.Errorf("internal/embed: SHA256SUMS 有坏行:%q", line)
		}
		// 用 hex.Decode 而非 fmt.Sscanf("%64x"):Sscanf 对定长十六进制的宽度语义
		// 并不保证「正好 32 字节」(实测直接报错),别在格式化上绕。
		h, err := hex.DecodeString(fields[0])
		if err != nil || len(h) != sha256.Size {
			return nil, fmt.Errorf("internal/embed: SHA256SUMS 哈希非法(%q)", fields[0])
		}
		var sum [32]byte
		copy(sum[:], h)
		out[fields[1]] = sum
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("internal/embed: SHA256SUMS 是空的(产物与清单不同批)")
	}
	return out, nil
}

// writeHashed 把 r 写到 path(0755),同时返回内容的 sha256(一边写一边算,不二次读盘)。
func writeHashed(path string, r io.Reader) ([32]byte, error) {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), r); err != nil {
		out.Close()
		return [32]byte{}, err
	}
	if err := out.Close(); err != nil {
		return [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

var _ = sdk.SDKVersion // 保持 sdk 感知(seed 与 SDK 同版本发布语义)

// pruneObsolete 把「**曾经**由随包官方发布、但当前版本不再随包」的插件移出插件目录。
//
// 为什么必须有这一步(2026-10-07 用户真机):升级只加不减时,旧版本插件会**永远留在**
// 数据根里,并与新版重复提供同名工具 —— 而先注册的旧插件先上场。用户的真实症状:
// v0.5.6 已支持 anysearch 搜索 provider,面板里却一直报「未知搜索 provider "anysearch"
// (可选: exa)」—— 提供 web_search 的是升级前留下的旧插件,它只认 exa。
// 用户看到的是「新功能没出现」,而系统自检(版本、哈希)全绿:没有任何一处能自证这件事。
//
// 三条边界:
//   - **只动 audit 标记为 `embed` 的**(随包官方发布)。用户自己装的(`install:` /
//     `trust:manual`)永远不动 —— 升级不该把用户装的东西悄悄处理掉;
//   - **移走而不是删除**:落到 `<name>.obsolete-<hash8>`,用户能自己看、能放回去;
//     删掉之后就只剩"插件不见了",而没人能判断它是不是本来就不该在;
//   - 任何一步失败只记一笔、绝不阻断启动:清理是卫生工作,不是启动条件。
//
// 返回被移走的插件 id(调用方可打日志/进 notice)。
func pruneObsolete(home string, want map[string][32]byte) []string {
	dir := filepath.Join(home, "plugins")
	list, err := plugintrust.Load(dir)
	if err != nil || list == nil {
		return nil // 清单读不出来 ⇒ 什么都不做(绝不凭猜测删插件)
	}
	var pruned []string
	for _, e := range list.Audit() {
		if e.Source != "embed" {
			continue // 用户装的:不碰
		}
		if _, still := want[e.Name]; still {
			continue // 当前版本仍随包:上面那条哈希路会更新它
		}
		src := filepath.Join(dir, e.Name)
		if _, err := os.Stat(src); err != nil {
			continue // 目录已不在(可能已被用户手动处理)
		}
		dst := fmt.Sprintf("%s.obsolete-%s", src, shortHash(e.Hash))
		if err := os.Rename(src, dst); err != nil {
			continue // 删不掉/移不动:留着,下次启动再试(Windows 上插件正被运行就会这样)
		}
		pruned = append(pruned, e.Name+" → "+filepath.Base(dst))
	}
	return pruned
}

func shortHash(hex64 string) string {
	if len(hex64) > 8 {
		return hex64[:8]
	}
	if hex64 == "" {
		return "unknown"
	}
	return hex64
}
