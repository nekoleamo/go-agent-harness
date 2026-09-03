# 开发规范(gah 仓库)

## 项目定位
- 微内核 harness:core/ 仅含 ctx/event/plugin/config/boot,零 Agent 能力、零 UI。
- 一切其余能力以插件形式存在(plugins/ 下,经 bundles 组装),随时插拔开关。
- 交付形态:单一静态二进制(`CGO_ENABLED=0`),运行时依赖 = 0(见 DESIGN.md §7)。

## 红线
- **插件只 import sdk/,不 import core/ 或 tui/**;core/ 不 import 业务包。
- **注册即副作用,卸载即撤销**:任何 Provide/Subscribe/工具注册必须随 Disposer 撤销,disposers 幂等。
- 缺失依赖显式失败,不静默降级。
- core/ 包必须带单测;新增分发模式/生命周期语义需测试覆盖。

## 语言与工具
- Go 1.27+;golangci-lint;testify 可选,多数用例标准库断言即可。
- 发布编译:`CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(git describe --tags)"`。
- CI:go vet + go test ./... -race。

## 事件与配置约定
- 插件事件名用点分(`agent/pre-step`、`tools/pre-execute`),扩展点优先 waterfall。
- 配置条目 id = `<type>-<name>`,与 plugins/ 目录同名;bundle 文件在 config/ 或 embed。
