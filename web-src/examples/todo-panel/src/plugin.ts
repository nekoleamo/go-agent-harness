// todo 面板展示联动插件(M8-T2):覆盖 statusbar 槽位——保留默认状态栏语义,
// 叠加右下角浮动 todo 面板:经 /api/todo 读任务状态(宿主代理 todo 工具查询,零额外 seam),
// 5s 轮询 + 手动刷新;会话工具结果变化后自动反映。
// 渲染层契约:禁止 v-html(文案插值渲染)。
import { h, onMounted, onUnmounted, ref, computed } from 'vue'

interface TodoItem {
  id: string
  subject: string
  status: string
  activeForm?: string
}

interface StatusbarProps {
  state: { model?: string; running?: boolean; thinking?: string; sandbox?: string }
}

const STATUS_LABEL: Record<string, string> = {
  pending: '待办',
  in_progress: '进行中',
  completed: '已完成',
  deleted: '已删',
}

const STATUS_COLOR: Record<string, string> = {
  pending: '#7d838f',
  in_progress: '#d4a25c',
  completed: '#7dd87d',
  deleted: '#ff7b72',
}

export default {
  name: 'TodoPanel',
  props: {
    state: { type: Object, default: () => ({}) },
  },
  setup() {
    const todos = ref<TodoItem[]>([])
    const err = ref('')
    const open = ref(true)
    let timer: ReturnType<typeof setInterval> | null = null

    async function refresh(): Promise<void> {
      try {
        const resp = await fetch('/api/todo')
        if (!resp.ok) {
          err.value = `HTTP ${resp.status}`
          return
        }
        const data = (await resp.json()) as TodoItem[]
        // 业务错误以单元素 {error} 回传(list 正常为空数组)
        if (Array.isArray(data)) {
          todos.value = data
          err.value = ''
        } else {
          err.value = (data as { error?: string }).error ?? '未知错误'
        }
      } catch (e) {
        err.value = String(e)
      }
    }

    const counts = computed(() => {
      const c = { pending: 0, in_progress: 0, completed: 0 }
      for (const t of todos.value) {
        if (t.status in c) c[t.status as keyof typeof c]++
      }
      return c
    })

    onMounted(() => {
      void refresh()
      timer = setInterval(() => void refresh(), 5000)
    })
    onUnmounted(() => {
      if (timer) clearInterval(timer)
    })

    return { todos, err, open, refresh, counts }
  },
  render() {
    const self = this as unknown as {
      state: StatusbarProps['state']
      todos: TodoItem[]
      err: string
      open: boolean
      refresh: () => void
      counts: { pending: number; in_progress: number; completed: number }
    }
    const s = self.state ?? {}
    // 默认状态栏语义(对齐宿主 StatusBar:gah | 状态 | 模型 | 思维 | 计数徽标)
    const bar = h(
      'div',
      { style: { display: 'flex', alignItems: 'center', gap: '10px', fontSize: '12px', color: '#7d838f' } },
      [
        h('span', { style: { color: '#6eb3ff', fontWeight: 600 } }, 'gah'),
        h('span', { style: { color: s.running ? '#d4a25c' : '#7d838f' } }, s.running ? '思考/执行' : '待输入'),
        h('span', {}, '模型 ' + (s.model || '未设置')),
        h('span', {}, '[' + s.thinking + ']'),
        h('span', { style: { color: '#7dd87d' } }, `✓${self.counts.completed}`),
        h('span', { style: { color: '#d4a25c' } }, `▶${self.counts.in_progress}`),
        h('span', { onClick: () => { self.open = !self.open } }, self.open ? '▾ 收起' : '▸ 展开'),
      ],
    )
    const panel =
      self.open
        ? h(
            'aside',
            {
              style: {
                position: 'fixed',
                right: '14px',
                bottom: '44px',
                width: '300px',
                maxHeight: '320px',
                overflowY: 'auto',
                background: '#1b1e24',
                border: '1px solid #2a2e36',
                borderRadius: '8px',
                padding: '10px',
                zIndex: 15,
                fontSize: '12px',
                boxShadow: '0 8px 24px rgba(0,0,0,.45)',
              },
            },
            [
              h('div', { style: { display: 'flex', justifyContent: 'space-between', marginBottom: '6px' } }, [
                h('b', { style: { color: '#c9cdd6' } }, '任务面板(todo)'),
                h('button', { onClick: () => void self.refresh(), style: btnStyle }, '↻ 刷新'),
              ]),
              self.err
                ? h('div', { style: { color: '#ff7b72' } }, self.err)
                : self.todos.length === 0
                  ? h('div', { style: { color: '#7d838f' } }, '暂无任务(模型可经 todo 工具建单)')
                  : self.todos.map((t) =>
                      h(
                        'div',
                        {
                          key: t.id,
                          style: {
                            display: 'flex',
                            gap: '8px',
                            padding: '4px 0',
                            borderBottom: '1px solid #2a2e36',
                            color: '#c9cdd6',
                          },
                        },
                        [
                          h('span', { style: { color: STATUS_COLOR[t.status] ?? '#7d838f', fontWeight: 600 } }, STATUS_LABEL[t.status] ?? t.status),
                          h('span', { style: { flex: 1 } }, t.subject),
                          t.status === 'in_progress' && t.activeForm
                            ? h('span', { style: { color: '#6eb3ff' } }, t.activeForm)
                            : null,
                        ],
                      ),
                    ),
            ],
          )
        : null
    return h('div', null, [bar, panel])
  },
}

const btnStyle: Record<string, string> = {
  background: 'none',
  border: '1px solid #2a2e36',
  borderRadius: '4px',
  color: '#6eb3ff',
  cursor: 'pointer',
  fontSize: '11px',
  padding: '1px 8px',
}
