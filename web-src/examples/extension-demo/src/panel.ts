// v2 扩展点示例:附加面板(extra-panel)。
// 宿主在侧栏「附加面板」区列入口 → 右侧抽屉渲染本组件(标题来自 manifest title)。
import { h } from 'vue'

export default {
  name: 'ExtPanelDemo',
  render() {
    return h('div', { style: { fontSize: '13px', color: '#171a1f', lineHeight: '1.8' } }, [
      h('p', { style: { margin: '0 0 6px' } }, 'B5 示例:附加面板(extra-panel)'),
      h('p', { style: { margin: '0', color: '#9aa0a6', fontSize: '12px' } }, '从侧栏「附加面板」入口打开本抽屉。'),
    ])
  },
}
