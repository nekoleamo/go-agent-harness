// v2 扩展点示例:侧栏动作(sidebar-action)。
// 宿主在侧栏「插件动作」区渲染本组件;点击经 onClick 触发(演示 console 输出)。
import { h } from 'vue'

export default {
  name: 'ExtActionDemo',
  render() {
    return h(
      'button',
      {
        style: {
          width: '100%',
          background: 'none',
          border: '1px solid transparent',
          borderRadius: '8px',
          color: '#565d65',
          cursor: 'pointer',
          padding: '4px 8px',
          fontSize: '12px',
          textAlign: 'left',
        },
        onClick: () => console.log('[extension-demo] 侧栏动作被点击'),
      },
      '示例动作(侧栏注入)',
    )
  },
}
