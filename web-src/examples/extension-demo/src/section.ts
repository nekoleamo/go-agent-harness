// v2 扩展点示例:设置面板区段(settings-section)。
// 宿主在设置抽屉底部注入 <section class="sec"> 包裹本组件;插件自包含样式(内联)。
// 渲染层契约:禁止 v-html(文案文本渲染,天然转义)。
import { h } from 'vue'

export default {
  name: 'ExtSectionDemo',
  render() {
    return h('div', { style: { fontSize: '12px', color: '#565d65', lineHeight: '1.7' } }, [
      h('p', { style: { margin: '0 0 4px', color: '#171a1f', fontWeight: 600 } }, 'B5 示例:插件区段'),
      h('p', { style: { margin: '0' } }, '由 gah -install-ui extension-demo 安装后在设置面板自动注入。'),
    ])
  },
}
