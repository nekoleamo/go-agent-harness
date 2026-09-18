<script setup lang="ts">
// 结构化提问弹层(P3 语义交互):question 帧出现时展示问题与选项,
// 单选点击即提交;多选/自由文本经提交按钮回传 REST /api/question。
// S-P0-2:支持「稍后作答」收起(不阻塞操作)——收起后由 App 侧角标挂出,点角标回到本弹层。
import { ref, watch } from 'vue'
import type { QuestionRequest } from '../types'

const props = defineProps<{
  request: QuestionRequest | null
  minimized?: boolean
  onAnswer: (values: string[], text: string) => void
  onMinimize?: () => void
}>()

const picked = ref<string[]>([])
const text = ref('')

// 新提问到达 → 重置本地选择态
watch(
  () => props.request?.id,
  () => {
    picked.value = []
    text.value = ''
  },
)

function toggle(v: string): void {
  if (props.request?.multiple) {
    picked.value = picked.value.includes(v) ? picked.value.filter((x) => x !== v) : [...picked.value, v]
  } else {
    picked.value = [v]
    submit()
  }
}
function onEnter(e: KeyboardEvent): void {
  if (e.isComposing || e.keyCode === 229) return // 输入法组字中不提交(见 InputBar 同款说明)
  submit()
}
// skip 跳过作答:回传空答案(后端按取消/超时语义处理并广播 questiondone),
// 不强迫用户作答——此前弹层无关闭路径,只能等回合侧超时。
function skip(): void {
  props.onAnswer([], '')
}
function submit(): void {
  const values = props.request?.multiple ? picked.value : picked.value.slice(0, 1)
  if (values.length === 0 && !text.value.trim()) return // 无选择且无文本:不提交(等用户输入)
  props.onAnswer(values, text.value.trim())
}
</script>

<template>
  <div v-if="request && !minimized" class="mask">
    <div class="dialog">
      <div class="title">需要你的选择</div>
      <pre class="prompt">{{ request.prompt }}</pre>
      <div v-if="request.options?.length" class="opts">
        <label v-for="o in request.options" :key="o.value" class="opt" :class="{ on: picked.includes(o.value) }">
          <input
            v-if="request.multiple"
            type="checkbox"
            :checked="picked.includes(o.value)"
            @change="toggle(o.value)"
          />
          <button v-else class="pick" type="button" @click="toggle(o.value)">{{ o.desc || o.value }}</button>
          <span v-if="request.multiple" class="lab">{{ o.desc || o.value }}</span>
        </label>
      </div>
      <input
        v-if="request.free_text || !request.options?.length"
        v-model="text"
        class="inp free"
        :placeholder="request.free_text ? '也可直接输入作答…' : '请输入你的回答…'"
        @keydown.enter="onEnter"
      />
      <div class="actions">
        <button v-if="onMinimize" class="later" data-tip="收起弹层,稍后再答(不影响继续对话)" @click="onMinimize()">
          稍后作答
        </button>
        <span class="spacer" />
        <button class="skip" @click="skip">跳过</button>
        <button class="go" :disabled="picked.length === 0 && !text.trim()" @click="submit">提交</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mask {
  position: fixed;
  inset: 0;
  background: var(--overlay);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 20;
}
.dialog {
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  padding: 18px 20px;
  max-width: 560px;
  width: 90%;
  box-shadow: var(--shadow-dialog);
}
.title {
  color: var(--fg);
  font-weight: 600;
  margin-bottom: 10px;
}
.prompt {
  margin: 0 0 14px;
  color: var(--fg);
  font: inherit;
  white-space: pre-wrap;
  word-break: break-word;
}
.opts {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 12px;
}
.opt {
  display: flex;
  align-items: center;
  gap: 8px;
}
.pick {
  width: 100%;
  text-align: left;
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  padding: 8px 12px;
  color: var(--fg);
  cursor: pointer;
}
.pick:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.lab {
  color: var(--fg);
}
.inp {
  width: 100%;
  box-sizing: border-box;
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  padding: 8px 12px;
  color: var(--fg);
  margin-bottom: 12px;
}
.actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
.spacer {
  flex: 1;
}
.later {
  background: transparent;
  color: var(--accent);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  padding: 8px 16px;
  cursor: pointer;
}
.later:hover {
  border-color: var(--accent);
}
.skip {
  background: transparent;
  color: var(--fg-dim);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  padding: 8px 16px;
  cursor: pointer;
}
.skip:hover {
  color: var(--fg);
  border-color: var(--fg-dim);
}
.go {
  background: var(--accent);
  color: var(--fg-on-accent);
  border: none;
  border-radius: var(--r-input);
  padding: 8px 16px;
  cursor: pointer;
}
.go:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
