<script setup lang="ts">
// 结构化提问弹层(P3 语义交互):question 帧出现时展示问题与选项,
// 单选点击即提交;多选/自由文本经提交按钮回传 REST /api/question。
import { ref, watch } from 'vue'
import type { QuestionRequest } from '../types'

const props = defineProps<{
  request: QuestionRequest | null
  onAnswer: (values: string[], text: string) => void
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
function submit(): void {
  const values = props.request?.multiple ? picked.value : picked.value.slice(0, 1)
  if (values.length === 0 && !text.value.trim()) return // 无选择且无文本:不提交(等用户输入)
  props.onAnswer(values, text.value.trim())
}
</script>

<template>
  <div v-if="request" class="mask">
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
        @keydown.enter="submit"
      />
      <div class="actions">
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
  background: var(--bg-soft);
  border: 1px solid var(--line);
  border-radius: var(--r-btn);
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
  background: var(--bg-soft);
  border: 1px solid var(--line);
  border-radius: var(--r-btn);
  padding: 8px 12px;
  color: var(--fg);
  margin-bottom: 12px;
}
.actions {
  display: flex;
  justify-content: flex-end;
}
.go {
  background: var(--accent);
  color: var(--on-accent);
  border: none;
  border-radius: var(--r-btn);
  padding: 8px 16px;
  cursor: pointer;
}
.go:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
</style>
