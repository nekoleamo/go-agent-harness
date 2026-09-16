// 设置面板里「当前模型」的落点计算。
//
// 单独成模块的原因:当前模型曾经有两个真源 —— 列表高亮取 state.model(运行时真正在用的),
// 而凭空补出来的「(当前)」选项取 provider 配置里的 Model(只是默认值)。两者不一致时高亮
// 永远匹配不上那个 value,表现为「设置里已经选了模型,但当前模型不显示」(2026-09-16 真机)。
// 现在统一为单一真源 state.model,并抽成纯函数 + 单测:这个位置错了在页面上很难自证。
//
// 另一个曾经的错:监听源里没有 state.model,于是选完模型后 state 更新了也不重算 ——
// 修在组件里(见 SettingsPanel.vue 的 watch),这里只管计算。

export interface ModelOption {
  label: string
  /** "Provider|模型ID"(面板列表的既有约定) */
  value: string
}

export function modelOptionValue(providerName: string, modelID: string): string {
  return providerName + '|' + modelID
}

// resolveModelValue 当前模型对应的选项 value;没有合适落点时返回空串。
// 先按「活跃 provider + 模型 ID」精确匹配,再退一步按模型 ID 唯一匹配(活跃 provider 与模型
// 归属不一致时,精确匹配必然落空)。
function resolveModelValue(options: ModelOption[], stateModel: string, activeProviderName: string): string {
  const cur = stateModel.trim()
  if (!cur) return ''
  const byActive = modelOptionValue(activeProviderName.trim(), cur)
  if (options.some((o) => o.value === byActive)) return byActive
  const hits = options.filter((o) => o.value.endsWith('|' + cur))
  return hits.length === 1 ? hits[0].value : ''
}

// withCurrentModel 保证当前模型在选项列表里有落点:找不到就补一条「(当前)」。
// 否则列表里一行都不会高亮 —— 用户看到的就是「已选择但当前模型未显示」。
export function withCurrentModel(options: ModelOption[], stateModel: string, activeProviderName: string): ModelOption[] {
  if (resolveModelValue(options, stateModel, activeProviderName) !== '') return options
  const cur = stateModel.trim()
  if (!cur) return options
  const name = activeProviderName.trim()
  const value = modelOptionValue(name, cur)
  return [{ label: (name ? name + ' · ' : '') + cur + '(当前)', value }, ...options]
}

// currentModelValue 当前模型在选项里的 value;返回空串表示「不高亮任何一项」。
export function currentModelValue(options: ModelOption[], stateModel: string, activeProviderName: string): string {
  return resolveModelValue(options, stateModel, activeProviderName)
}
