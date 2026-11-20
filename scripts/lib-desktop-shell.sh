#!/usr/bin/env bash
# 桌面壳选择器(两套壳并存期的**单一事实源**)。
#
# 用法(其余脚本 source 它):
#   source scripts/lib-desktop-shell.sh
#   MOD="$(gah_shell_mod)"
#
# 开关:GAH_DESKTOP_SHELL=mygo(默认)| wails
#   - 2026-11-20 起默认 **mygo**(0.10.0 的桌面壳就是它;用户已真机验收);
#     wails 保留可构建作回退:出问题把这里的默认值改回 wails 重打即可,不需改代码;
#   - mygo 壳的构建方式不同:它自带 go.mod 且要求 go 1.27.3,不在仓库 workspace 里,
#     所以必须 GOWORK=off(见 docs/DESKTOP_FEASIBILITY.md §14.3);
#   - wails 的退役(删模块/脚本改名)留给后续批次;在那之前两边都要能编。
#
# 为什么需要这层:产物布局(binaries/、build/、embed/gah.bin.gz、.app 结构、可执行文件名)
# 两套壳完全一致 —— 让调用方(gen/publish/package/release/workflow)只认这些函数,
# 而不是各自硬编码 desktop/wails 路径(硬编码是"换壳时漏改一处就静默发错产物"的成因)。

# gah_shell_id 当前选择的壳 id。
gah_shell_id() { echo "${GAH_DESKTOP_SHELL:-mygo}"; }

# gah_shell_mod 壳模块目录(相对仓库根)。
gah_shell_mod() {
  case "$(gah_shell_id)" in
    wails) echo "desktop/wails" ;;
    mygo) echo "desktop/mygo" ;;
    *)
      echo "未知 GAH_DESKTOP_SHELL=$(gah_shell_id)(只支持 wails|mygo)" >&2
      return 1
      ;;
  esac
}

# gah_shell_tags <goos> 额外 build tags(空格分隔;空串表示不带)。
#   wails/darwin → purego(Wails v3.1 的 CGO-free macOS 后端,§12)
#   mygo         → 无(它本来就是 purego 实现)
gah_shell_tags() {
  case "$(gah_shell_id):$1" in
    wails:darwin) echo "purego" ;;
    *) echo "" ;;
  esac
}

# gah_shell_exe <goos> build/ 下壳可执行文件名。
gah_shell_exe() {
  if [ "$1" = "windows" ]; then echo "gah-desktop.exe"; else echo "gah-desktop"; fi
}

# gah_shell_build <goos> <goarch> <ldflags> <tags> <out-path>
# 在壳模块目录里构建,产物落到模块的 build/ 下(路径布局两套壳一致)。
gah_shell_build() {
  local goos="$1" goarch="$2" ld="$3" tags="$4" out="$5" mod
  mod="$(gah_shell_mod)" || return 1
  mkdir -p "$mod/build"
  local targs=()
  [ -n "$tags" ] && targs=(-tags "$tags")
  local gowork=()
  # mygo 不在 workspace(它要 go 1.27.3),必须 GOWORK=off 才编得动。
  [ "$(gah_shell_id)" = "mygo" ] && gowork=(GOWORK=off)
  ( cd "$mod" && env "${gowork[@]}" CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath "${targs[@]}" -ldflags "$ld" -o "build/$(basename "$out")" . )
}

# gah_shell_genwinres <goos> <version>
# 给 Windows 产物写 PE 资源(图标/版本信息/manifest)。**为什么必须在构建前跑**:
# 图标与"属性→详细信息"都来自 exe 的资源段,裸 go build 不含它们 —— 用户看到的是 Go 默认图标
# 与空版本(2026-11-20 真机反馈)。只有 mygo 壳带这个生成器(wails 壳走它的打包工具链)。
gah_shell_genwinres() {
  local goos="$1" ver="$2" mod
  [ "$goos" = "windows" ] || return 0
  [ "$(gah_shell_id)" = "mygo" ] || return 0
  mod="$(gah_shell_mod)" || return 1
  [ -f "$mod/cmd/genwinres/main.go" ] || return 0
  ( cd "$mod" && GOWORK=off go run ./cmd/genwinres -version "$ver" ) || {
    echo "gah_shell_genwinres: 生成 Windows 资源失败($mod)" >&2
    return 1
  }
}
