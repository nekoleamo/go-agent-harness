<script setup lang="ts">
// 状态栏(槽位 statusbar):只放**别处没有的只读环境事实** —— 运行状态 / 沙箱生效档 / 连接 /
// 上下文用量 / 版本。2026-09-17 去重:模型、思考档、审批档各自已有唯一交互位(输入框工具条、
// 设置面板),底栏再镜像一遍既是重复显示又把一行挤变形;命名会话也不在这里重复(看侧栏高亮),
// 版本号改成「关于 gah」入口。对齐 TUI 状态栏语义;空值显示占位(不假精确:窗口未知仅显示量)。
import { computed } from 'vue'
import { hasSessionOverride } from '../scope'
import { connLabel } from '../conn'
import type { StateView } from '../types'
import { othersRunning } from '../session-parallel'

const props = defineProps<{
  state: StateView
  conn?: 'open' | 'reconnecting' | 'offline'
  // curSteps 本窗口会话的当前步数(只对本窗口可见;别的会话只能知道"在跑")
  curSteps?: number
}>()
// 版本号点击 → 「关于 gah」。用插槽替换底栏的一方不发这个事件也不影响(监听是可选的)。
const emit = defineEmits<{ (e: 'open-about'): void }>()
// connText 连接状态短标签:口径来自 conn.ts(宿主与插件覆盖槽位共用同一定义)
const connText = computed(() => connLabel(props.conn ?? 'open'))
// othersRunning 别的会话正在跑的数量(不含本会话 —— 本会话在跑时上面已显示)。
const othersN = computed(() => othersRunning(props.state.running_sessions, props.state.running))

function k(n: number): string {
  if (n < 1024) return String(n)
  return (n / 1024).toFixed(1) + 'K'
}

// ctx 上下文占用:分子用**最近一次请求的实测 prompt**(LastPromptTokens),不用累计 PromptTokens
// —— 累计是多轮之和,多轮后会变成「5000K/128K」这种越跑越大的假数字。缓存仍是累计命中率。
const ctx = computed(() => {
  const s = props.state.stats
  const used = s.LastPromptTokens
  if (s.Window > 0) {
    const pct = Math.min(100, Math.round((used / s.Window) * 100))
    const cachePct = s.PromptTokens > 0 ? Math.round((s.CachedTokens / s.PromptTokens) * 100) : 0
    return `${k(used)}/${k(s.Window)} ${pct}% · 缓存 ${cachePct}%`
  }
  return used > 0 ? `${k(used)} (窗口未知)` : '–'
})

const SANDBOX_ZH: Record<string, string> = { 'read-only': '只读', 'full-access': '完全', 'workspace-write': '工作区' }
// sessionScoped:该会话是否**压过了全局默认**(模型/思考/沙箱/审批任一项自己设过)。
// 判据见 scope.ts:只看 session_prefs,不读 *_from(那些字段答的是「是不是角色给的」)。
const sessionScoped = computed(() => hasSessionOverride(props.state))

// 审批档位中文名:只服务下面的 sandboxLabel(拼"随审批联动"的出处);审批档本身不在底栏显示
// —— 它已有唯一交互位(设置面板「审批」分段按钮)。
const APPROVAL_ZH: Record<string, string> = { open: '开放', smart: '智能', strict: '严格' }
// sandboxLabel 显示**实际生效**档(approval 联动时后端给 sandbox_effective):
// 只读 state.sandbox(声明档)会在 approval=open 时把"完全"显示成"工作区",与实际拦截行为不符。
// 保留原因:输入框工具条显示的是**声明档**(生效档只在它的图说里),底栏是唯一常驻显示生效档的位置,
// 删掉会让「随审批联动」在界面上不可见 —— 去重不能以丢掉安全相关状态为代价。
const sandboxLabel = computed(() => {
  const eff = props.state.sandbox_effective || props.state.sandbox
  const base = SANDBOX_ZH[eff] ?? '工作区'
  if (!props.state.sandbox_derived || !props.state.sandbox_effective) return base
  // 偏离来源由后端下发(第九十二批):角色收紧时写"随审批联动"是假归因,
  // 而这两件事对用户意味着完全不同的处置(换个角色 vs 改审批档)。
  if (props.state.sandbox_from === 'role') return `${base}(角色收紧)`
  const src = APPROVAL_ZH[props.state.approval ?? '']
  return src ? `${base}(随审批${src})` : `${base}(随审批联动)`
})

// 会话标识只在**未命名**时显示(命名会话侧栏有高亮、输入框也有「会话」按钮,底栏再显一次纯重复,
// 且长会话名会把底栏一行挤变形);它唯一的真实价值是"这条消息进的是哪条"。
// roleLabel 当前角色徽标(第七十九批):角色会改系统提示与技能可见集合,是"环境事实",
// 属于本栏该放的东西。未启用角色时不占位(不显示"无角色")。
const roleLabel = computed(() => {
  const id = props.state.role
  if (!id) return ''
  const name = props.state.role_name
  return name && name !== id ? `${name}(${id})` : id
})

const anonSession = computed(() => {
  const s = props.state.session
  if (!s || s.name) return ''
  return s.id ? ' #' + s.id : ' (主)'
})
</script>

<template>
  <div class="bar">
    <span class="run" :class="{ busy: state.running }">
      <span v-if="state.running" class="dot" />
      {{ state.running ? '运行中' : '就绪' }}
      <template v-if="state.running && (curSteps || 0) > 0">· 第 {{ curSteps }} 步</template>
    </span>
    <!-- 别的窗口/别的会话在跑:只在**本会话没在跑**时提示。本会话在跑时不必说
         「还有 N 个」——底栏已经很挤,且那件事在侧栏有逐会话标记。 -->
    <span v-if="othersN > 0" class="it faint" data-testid="others-running">
      另有 {{ othersN }} 个会话在跑
    </span>
    <!-- 沙箱与角色都是**本会话**的(第一百三十四批)。前缀只在**该会话真的压过了全局**时出现:
         绝大多数会话跟随全局,那时这三个字是纯噪音,还要挤底栏的宽度(700px 窗口下实测溢出
         46px)。真独立了才值得说 —— 「信号才占位」与页签方块标记同一口径。 -->
    <span v-if="sessionScoped" class="it faint scope-key">本会话</span>
    <span class="it faint">沙箱 {{ sandboxLabel }}</span>
    <!-- 角色名是本栏唯一「用户自定长度」的字段:截断显示,全文在 tooltip 里 ——
         不截断时长名字会把文字挤成多行(底栏从 22px 涨到 58px,真实测得)并把右侧挤出去。 -->
    <span
      v-if="roleLabel"
      class="it role"
      :data-tip="'当前角色:' + roleLabel + '(下一轮生效;可在设置面板切换)'"
    >
      <span class="role-key">角色</span>{{ roleLabel }}
    </span>
    <span v-if="anonSession" class="it faint">未命名会话{{ anonSession }}</span>
    <span class="spacer" />
    <span class="conn" :class="props.conn">
      <span class="conn-dot" />
      <span class="conn-text">{{ connText }}</span>
    </span>
    <span class="ctx mono">{{ ctx }}</span>
    <button class="ver mono" data-tip="关于 gah(版本 / 检查更新)" @click="emit('open-about')">
      v{{ state.version || 'dev' }}
    </button>
  </div>
</template>

<style scoped>
.bar {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 22px;
  /* 底栏是**单行**事实栏:窄窗口下该被截断的是角色名(能吃省略号),不是把
     「沙箱/连接/用量」折成两行 —— 折行会让底栏从 22px 涨到 38/58px(实测 700/640px)。 */
  white-space: nowrap;
  /* min-width:0:底栏是状态栏槽位的 flex 项,默认的 min-width:auto(= 内容宽)会让它
     拒绝收缩,于是窄窗口下把右侧按钮顶出屏幕。允许收缩后,亏空由 .role 的省略号吸收。 */
  min-width: 0;
}
.bar > * {
  /* 固定文案不参与挤压(挤压份额全部留给 .role;见下方 .role 的 flex-shrink:1) */
  flex-shrink: 0;
}
.run {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--fg-dim);
  font-size: 12px;
}
.run.busy {
  color: var(--accent);
  font-weight: 500;
}
.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--accent);
  animation: pulse 1.1s ease-in-out infinite;
}
@keyframes pulse {
  0%,
  100% {
    opacity: 0.35;
  }
  50% {
    opacity: 1;
  }
}
.it {
  color: var(--fg-dim);
}
.it.faint {
  color: var(--fg-faint);
  font-size: 12px;
}
.role {
  /* 角色会改系统提示与技能可见集合 —— 是“当前人格”这个环境事实,不是一条状态说明。
     2026-10-03 用户反馈「主界面当前角色很难注意到」:此前是 .faint 灰小字,与沙箱/会话
     等提示同级。改为单强调色 + 胶囊底 + 边框(不引第二色相:单强调色纪律)。
     display 必须留在 inline-block:flex 容器里 text-overflow:ellipsis **不生效**(溢出文本
     只被裁掉、不出省略号),长角色名会把底栏撑高 —— 布局护栏 test:layout 会红。
     同理不能用 gap(那是 flex/grid 属性),间距走 .role-key 的 margin。 */
  display: inline-block;
  padding: 0 8px;
  border: 1px solid var(--accent-line);
  border-radius: 999px;
  background: var(--accent-soft);
  color: var(--accent);
  font-size: 12px;
  font-weight: 600;
  /* flex-basis 显式给 20ch:不给的话 flex 基准取"未截断的整串名字"(实测 484px),
     它会吃掉全部压缩份额并触顶在 max-width,剩下的亏空转嫁给别的项(它们只能折行)。 */
  flex-basis: 20ch;
  max-width: 20ch;
  /* 空间不够时由徽标先让,一直可以让到 0:可省略号的内容是唯一值得牺牲的,
     连接状态/上下文用量/版本号(以及右侧按钮)都比"角色名显示全"更不可牺牲。 */
  min-width: 0;
  flex-shrink: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}
.role-key {
  color: var(--fg-dim);
  font-weight: 500;
  margin-right: 5px;
}
/* 作用域前缀词(「本会话」):比值本身更弱,但比没有强 —— 它的作用是**把作用域说出口**,
   不是抢注意力。与 .role-key 同构但更淡。 */
.scope-key {
  font-size: 11px;
  opacity: 0.75;
}
/* 窄窗(底栏已被挤满时):胶囊的 padding+border 是**不可压缩的固定开销**(flex-shrink 只压内容,
   压不掉内边距),而本栏里角色徽标正是「唯一允许先让位」的那一项 —— 不让位的结果是
   700px 视口下整条底栏溢出 16px(布局护栏 test:layout 实测红)。
   故窄窗退成**无胶囊的强调色粗体**:显眼度的大头(强调色 + 加重)全保留,只丢掉装饰,
   零布局开销。判断线取 760px:低于它底栏本就没有余量。 */
@media (max-width: 760px) {
  .bar {
    gap: 8px;
  }
  .role {
    padding: 0;
    border: 0;
    background: none;
    border-radius: 0;
  }
  .role-key {
    display: none;
  }
}
/* 窄窗(底栏已被挤满时):胶囊的 padding+border 是**不可压缩的固定开销**(flex-shrink 只压
   内容,压不掉内边距),而本栏里角色徽标正是「唯一允许先让位」的那一项 —— 不让位的结果是
   700px 视口下整条底栏溢出 16px(布局护栏会红)。
   故窄窗退成**无胶囊的强调色粗体**:显眼度的大头(强调色 + 加重)全保留,只丢掉装饰,
   零布局开销。判断线取 760px:低于它底栏本就没有余量。 */
.ctx {
  color: var(--fg-dim);
  font-size: 12px;
}
.spacer {
  flex: 1;
}
.conn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  color: var(--fg-dim);
  font-size: 12px;
}
.conn-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--ok);
}
.conn.reconnecting .conn-dot {
  background: var(--tool);
  animation: pulse 1.1s ease-in-out infinite;
}
.conn.offline .conn-dot {
  background: var(--err);
}
.conn-text {
  color: var(--fg-dim);
}
.conn.reconnecting .conn-text {
  color: var(--tool);
  font-weight: 500;
}
.conn.offline .conn-text {
  color: var(--err);
  font-weight: 600;
}
.mono {
  font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
}
/* 版本号 = 「关于 gah」入口:清掉按钮默认样式,只留底栏的淡显文本外观 */
.ver {
  padding: 0;
  border: 0;
  background: none;
  color: var(--fg-faint);
  font-size: 12px;
  cursor: pointer;
  transition: color var(--dur-fast) var(--ease-out);
}
.ver:hover {
  color: var(--fg-dim);
  text-decoration: underline;
}
</style>
