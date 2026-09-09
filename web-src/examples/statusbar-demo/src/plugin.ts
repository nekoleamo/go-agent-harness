// 槽位覆盖示例组件(statusbar):渲染模型徽标(契约 v1 props: state)。
// 渲染层契约:禁止 v-html(文案文本渲染,天然转义)。
import { h } from 'vue'

// 槽位 Props 契约(与宿主 web-src/src/registry.ts 对齐;v1)
interface StatusbarProps {
  state: { model?: string; running?: boolean }
}

export default {
  name: 'StatusbarDemo',
  props: {
    state: { type: Object, default: () => ({}) },
  },
  render(this: { state: StatusbarProps['state'] }) {
    const s = this.state ?? {}
    return h(
      'span',
      {
        style: {
          background: s.running ? '#d4a25c' : '#2a2e36',
          color: '#0c1015',
          padding: '1px 8px',
          borderRadius: '10px',
          fontWeight: 600,
          fontSize: '11px',
        },
      },
      'UI-PLUGIN:' + (s.model ?? '(未设置)'),
    )
  },
}
