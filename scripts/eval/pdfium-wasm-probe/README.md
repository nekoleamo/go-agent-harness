# pdfium.wasm × wazero 可行性探针(SELF-1 前置实测)

评测件,**不参与主模块构建**(本目录自带 `go.mod`,主模块 `go build ./...` 不会包含它)。

## 结论摘要(2026-10-11 实测)

**功能可行**:`@embedpdf/pdfium` 的 `pdfium.wasm` 在**纯 Go 运行时 wazero** 上完成
`初始化 → 载入 PDF → 渲染位图`,与 poppler `pdftoppm` 逐像素对照**只差抗锯齿/字形边缘**,
无结构性差异(3 份真实 PDF:矢量文字+认证标志页、iWork 图形页、10 页合成文档)。

**代价与残留风险**:

| 项 | 实测值 |
|---|---|
| wasm 体积 | @embedpdf/pdfium 2.15.0:4.42 MiB raw / **2.03 MiB gz**;@hyzyla/pdfium 2.1.13:3.80 / **1.92** |
| wazero 运行时二进制增量 | **+3.46 MiB**(`-trimpath -ldflags "-s -w"`,hello 1.12 → probe 4.59 MiB) |
| 合计对体积门影响 | 40.72 + ≈5.5 = **≈46.2 MiB** → **超出当前 ≤46 MiB 门**;gz 29.2 MiB(门 ≤30) |
| 编译 | 冷 828ms / 命中 compilation cache 26ms(可落 `$GAH_HOME/cache`) |
| 初始化 / 载入 | 1ms / <1ms |
| 渲染 | 10 页 @144dpi(900×1120)= 23ms |
| 线性内存 | 基线 17.75 MiB → 10 页 41.38 MiB |
| 进程峰值 RSS | ≈256–294 MiB(含编译) |
| 宿主 stub | env 30 + wasi_snapshot_preview1 7 个导入(Emscripten 产物,WASI 为 32 位偏移变体,不能复用标准 WASI 宿主) |

**残留风险(未解)**:Emscripten `invoke_*`(JS 侧函数指针 trampoline)在 wazero 中**无法忠实实现**
(公开 API 无 table/function-reference 调用能力),只能 stub 成空操作;实测一份真实 PDF 触发 6 次
`invoke_*`,渲染结果经像素对照无可见差异,但**不可证明安全**。彻底解决需自行以
`-sSTANDALONE_WASM` / `-sSUPPORT_LONGJMP=wasm` 构建 pdfium(emsdk + depot_tools,重型)。

**判定**:SELF-1 仍**暂存**——仅当「零外部依赖部署」成为硬需求时实施,且需先执行降体路径
(外部插件协议去 gRPC 化 ≈ -14 MiB / extplugins 附包化 ≈ -20 MiB)或重定体积门。

## 用法

```bash
# 1) 取候选 wasm(npm registry;体积/导入面可离线核对)
curl -sS -o embedpdf.tgz https://registry.npmjs.org/@embedpdf/pdfium/-/pdfium-2.15.0.tgz
tar xzf embedpdf.tgz                                  # → package/dist/pdfium.wasm
curl -sS -o hyzyla.tgz https://registry.npmjs.org/@hyzyla/pdfium/-/pdfium-2.1.13.tgz

# 2) 依赖 + 构建(本目录是嵌套模块:仓库根有 go.work,须 GOWORK=off;
#    默认 proxy.golang.org 在部分网络不可达时可改 GOPROXY=https://goproxy.io)
GOWORK=off GOPROXY=https://goproxy.io GOSUMDB=off go mod download
GOWORK=off go build -o probe .

# 3) 探针
./probe -wasm package/dist/pdfium.wasm -init PDFiumExt_Init -pdf big.pdf -scale 2 -pages 10
./probe -wasm package/dist/pdfium.wasm -init PDFium_Init     -pdf big.pdf   # @hyzyla 用 PDFium_Init

# 4) 与 poppler 对照(需本机 pdftoppm)
pdftoppm -png -r 144 -f 1 -l 1 big.pdf ref    # 页码 ≥10 时输出 ref-01.png
./probe -wasm package/dist/pdfium.wasm -init PDFiumExt_Init -pdf big.pdf -scale 2 -pages 1 \
    -png got.png -diff ref-1.png -diffimg diff.png
```

参数:`-scale`(1=72dpi,2≈144dpi)、`-pages`(渲染页数)、`-png`(首页写出 PNG)、
`-diff`/`-diffimg`(与参考 PNG 逐像素对照:差异像素占比 / 最大通道差 / 墨迹量 / 差异包围盒 / 标红叠加图)。

## 探针实现要点

- 宿主 stub 的**签名直接取自模块自身的导入定义**(`CompiledModule.ImportedFunctions()`),
  因此 Emscripten 的 32 位偏移 WASI 变体也能装配(复用 wazero 标准 WASI 宿主会签名不匹配)。
- 有语义的 stub 只有 4 个:`_emscripten_memcpy_js`(真做内存拷贝)、`emscripten_resize_heap`
  (对导出 memory 调 `Grow`)、`emscripten_date_now`(当前毫秒)、`fd_write`(把 pdfium 诊断转 stderr);
  其余按名分类计数后零值返回(`invoke_*` / `__syscall*` / 时间 / abort 类)。
- `env` 与 `wasi_snapshot_preview1` 两个命名空间都由本探针提供,便于统计各 stub 的**实际调用次数**
  (判断哪些可以安全空实现)。
