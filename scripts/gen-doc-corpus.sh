#!/bin/bash
# 真实文档保真语料生成(E-D;与 plugins/host/host-docview/fidelity_test.go 配套)。
#
# 目的:文档抽取器(D2–D4)此前只对手工构造夹具验证过;本脚本用**本机第三方生产者**
# 产出真实世界文档,喂给 opt-in 保真 harness,得到可核对的质量报告而无需入库二进制样本。
#
# 生产者(离线;缺失即显式跳过,不静默):
#   - textutil(macOS):HTML → docx/odt/rtf/doc(Apple OOXML 写出器,独立实现)
#   - cupsfilter(macOS):txt → pdf(CUPS texttopdf 滤镜,独立实现)
#   - 系统真实 PDF:Homebrew 测试夹具 / CoreSimulator 设备 PDF(存在才拷)
#   - 高级特性 xlsx(图表/透视/批注/内嵌图/文本框):本机无 xlsx 生产者,由
#     scripts/gen-xlsx-advanced.py 以**纯标准库手工构造 OOXML** 补位(D6-3 判定件)
#
# 用法:
#   bash scripts/gen-doc-corpus.sh [目标目录] [额外真实文件或目录 ...]
#   目标目录默认 $GAH_HOME/cache/doc-corpus(便携纪律:运行数据走 GAH_HOME 派生路径)
# 之后:
#   GAH_DOC_CORPUS=<目标目录> go test ./plugins/host/host-docview/ -run TestFidelityCorpus -v
set -euo pipefail

OUT="${1:-${GAH_HOME:-$HOME/.cache}/cache/doc-corpus}"
shift || true

mkdir -p "$OUT"
echo "语料目录: $OUT"

made=0
skip() { echo "  跳过: $*"; }

# —— 1) textutil:HTML → docx / odt / rtf ——
HTML="$OUT/_src.html"
cat > "$HTML" <<'EOF'
<!doctype html><html><head><meta charset="utf-8"><title>语料样本 标题</title></head><body>
<h1>一级标题 中文</h1><h2>二级标题</h2>
<p>正文段落,含 <b>粗体</b>、<i>斜体</i>、<u>下划线</u> 与 <code>inline code</code>。</p>
<ul><li>项目一</li><li>项目二<ul><li>嵌套项</li></ul></li></ul>
<ol><li>有序一</li><li>有序二</li></ol>
<blockquote>引用段落</blockquote>
<table border="1"><tr><th>列A</th><th>列B</th></tr><tr><td>1</td><td>2</td></tr><tr><td>中文</td><td>english</td></tr></table>
<pre>fenced code
  line2</pre>
</body></html>
EOF
if command -v textutil >/dev/null 2>&1; then
  for fmt in docx odt; do
    if textutil -convert "$fmt" -output "$OUT/corpus-$fmt.$fmt" "$HTML" 2>/dev/null && [ -s "$OUT/corpus-$fmt.$fmt" ]; then
      echo "  生成: corpus-$fmt.$fmt ($(wc -c <"$OUT/corpus-$fmt.$fmt") 字节)"
      made=$((made+1))
    else
      echo "  失败: textutil → $fmt"
    fi
  done
else
  skip "textutil 不存在(非 macOS)"
fi

# —— 2) cupsfilter:文本 → PDF ——
if command -v cupsfilter >/dev/null 2>&1; then
  printf 'Corpus PDF sample\n\nhello world 中文测试\nline three\n' > "$OUT/_src.txt"
  if cupsfilter "$OUT/_src.txt" > "$OUT/corpus-cups.pdf" 2>/dev/null && head -c 5 "$OUT/corpus-cups.pdf" | grep -q '%PDF-'; then
    echo "  生成: corpus-cups.pdf ($(wc -c <"$OUT/corpus-cups.pdf") 字节)"
    made=$((made+1))
  else
    echo "  失败: cupsfilter 未产出 PDF"
  fi
else
  skip "cupsfilter 不存在"
fi

# —— 3) 系统真实 PDF(第三方产出的多页/带字体文件) ——
for src in \
  /opt/homebrew/Library/Homebrew/test/support/fixtures/test.pdf \
  "/Library/Developer/CoreSimulator/Profiles/DeviceTypes/iPhone Xʀ.simdevicetype/Contents/Resources/sensor_bar_class_01.pdf" \
  "/Library/Developer/CoreSimulator/Profiles/DeviceTypes/iPhone Air.simdevicetype/Contents/Resources/sensor_bar_class_05.pdf" ; do
  if [ -f "$src" ]; then
    name="real-$(basename "$(dirname "$src")" | tr ' ' '_')-$(basename "$src")"
    cp "$src" "$OUT/$name"
    echo "  拷入: $name ($(wc -c <"$OUT/$name") 字节)"
    made=$((made+1))
  fi
done

# —— 3b) 高级特性 xlsx(D6-3 判定件;本机无 xlsx 生产者 → 纯标准库手工构造 OOXML) ——
#   性质:合成容器(非第三方生产者输出),只用于驱动特性探针/抽取路径,见脚本头部说明。
if command -v python3 >/dev/null 2>&1; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if python3 "$SCRIPT_DIR/gen-xlsx-advanced.py" "$OUT"; then
    made=$((made+3))
  else
    echo "  失败: gen-xlsx-advanced.py"
  fi
else
  skip "python3 不存在(无法生成高级特性 xlsx)"
fi

# —— 4) 额外真实文档(用户/项目交付物;目录则递归) ——
for extra in "$@"; do
  if [ -d "$extra" ]; then
    while IFS= read -r f; do
      cp "$f" "$OUT/extra-$(basename "$f")"
      echo "  拷入: extra-$(basename "$f") ($(wc -c <"$OUT/extra-$(basename "$f")") 字节)"
      made=$((made+1))
    done < <(find "$extra" -maxdepth 4 -type f \( -iname '*.docx' -o -iname '*.xlsx' -o -iname '*.pptx' -o -iname '*.pdf' -o -iname '*.md' -o -iname '*.csv' -o -iname '*.ipynb' \) -size -8M 2>/dev/null | head -20)
  elif [ -f "$extra" ]; then
    cp "$extra" "$OUT/extra-$(basename "$extra")"
    echo "  拷入: extra-$(basename "$extra") ($(wc -c <"$OUT/extra-$(basename "$extra")") 字节)"
    made=$((made+1))
  else
    skip "不存在: $extra"
  fi
done

rm -f "$HTML" "$OUT/_src.txt"
echo "完成:$made 个语料文件。运行:"
echo "  GAH_DOC_CORPUS=\"$OUT\" go test ./plugins/host/host-docview/ -run TestFidelityCorpus -v"
if [ "$made" -eq 0 ]; then
  echo "警告:未生成任何语料(检查第三方工具是否可用)" >&2
  exit 1
fi
