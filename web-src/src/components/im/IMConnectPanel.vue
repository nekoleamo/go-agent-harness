<script setup lang="ts">
// IM 连接卡(E 组 E1/E2/E3):渠道按 kind 渲染「二维码卡」或「表单卡」。
// 状态来自 SSE `imconnect` 帧(imstore;零高频轮询),动作走 REST;密钥永不回显。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../../api'
import { imSpec, imStatus, phaseLabel, upsertIMStatus } from '../../imstore'
import type { IMConnectField } from '../../types'

const busy = ref(false)
const err = ref('')
const form = ref<Record<string, string>>({})
const showSecret = ref(false)

const spec = computed(() => imSpec.value)
const st = computed(() => imStatus.value)
const isQR = computed(() => spec.value?.kind === 'qr')
const isForm = computed(() => spec.value?.kind === 'form')
const statusText = computed(() => st.value?.error || st.value?.detail || phaseLabel(st.value?.phase))
const connected = computed(() => st.value?.phase === 'done')

// 初始化表单默认值(枚举取当前 env;密钥留空 = 沿用已配置)
function initForm(): void {
  const next: Record<string, string> = {}
  for (const f of spec.value?.fields || []) {
    if (f.options?.length) next[f.key] = f.options[0].value
    else next[f.key] = ''
  }
  if (st.value?.env) next['env'] = st.value.env
  form.value = next
}

async function refresh(): Promise<void> {
  try {
    const s = await api.imConnectState()
    upsertIMStatus(s)
  } catch (e) {
    /* 未装配/离线:保留现有状态 */
    void e
  }
}

async function start(): Promise<void> {
  if (busy.value) return
  busy.value = true
  err.value = ''
  try {
    upsertIMStatus(await api.imConnectStart())
  } catch (e) {
    err.value = (e as Error).message
  } finally {
    busy.value = false
  }
}

async function submit(): Promise<void> {
  if (busy.value) return
  busy.value = true
  err.value = ''
  try {
    // 密钥未重填 → 送哨兵,后端沿用已配置值(不回显明文)
    const values: Record<string, string> = { ...form.value }
    for (const f of spec.value?.fields || []) {
      if (f.secret && !values[f.key]) values[f.key] = '__keep__'
    }
    upsertIMStatus(await api.imConnectSubmit(values))
  } catch (e) {
    err.value = (e as Error).message
    await refresh()
  } finally {
    busy.value = false
  }
}

function fieldHint(f: IMConnectField): string {
  if (!f.configured) return f.help || ''
  return (f.mask ? '已配置 ' + f.mask : '已配置') + (f.help ? ' · ' + f.help : '')
}

let timer: ReturnType<typeof setInterval> | null = null
onMounted(async () => {
  await refresh()
  initForm()
  // 兜底低频轮询(主路径是 SSE imconnect 帧;5s 仅防事件丢失)
  timer = setInterval(() => void refresh(), 5000)
})
onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>

<template>
  <div class="imc">
    <div class="imc-head">
      <span class="imc-ch">{{ spec?.channel || 'IM' }}</span>
      <span class="dot" :class="{ on: connected }" />
      <span class="imc-phase">{{ phaseLabel(st?.phase) }}</span>
      <span v-if="st?.account" class="imc-acct mono">{{ st.account }}</span>
    </div>
    <div v-if="spec?.hint" class="imc-hint">{{ spec.hint }}</div>
    <div v-if="statusText" class="imc-status" :class="{ err: !!st?.error }">{{ statusText }}</div>
    <div v-if="err" class="imc-status err">{{ err }}</div>

    <!-- 扫码渠道:二维码卡 + 相位(过期自动重取由后端完成,新码经事件推送) -->
    <div v-if="isQR" class="imc-body">
      <div class="imc-qr">
        <img v-if="st?.qr_png" :src="st.qr_png" alt="登录二维码" />
        <div v-else class="imc-qr-empty">{{ st?.phase === 'done' ? '已连接' : '点击「扫码登录」获取二维码' }}</div>
      </div>
      <div class="imc-actions">
        <button class="btn primary" :disabled="busy || st?.phase === 'waiting_scan'" @click="start">
          {{ spec?.action || '扫码登录' }}
        </button>
        <button class="btn" :disabled="busy" @click="refresh">刷新状态</button>
        <a v-if="spec?.docs_url" class="link" :href="spec.docs_url" target="_blank" rel="noopener noreferrer">使用说明</a>
      </div>
      <div class="imc-note">协议不下发二维码有效期:过期自动重取新码,无需重按。</div>
    </div>

    <!-- 表单渠道:AppID/AppSecret/环境 + 即时校验 + 平台外链 -->
    <div v-else-if="isForm" class="imc-body">
      <label v-for="f in spec?.fields || []" :key="f.key" class="field">
        <span class="fl">{{ f.label }}<span v-if="f.required" class="req">*</span></span>
        <select v-if="f.options?.length" v-model="form[f.key]" class="inp">
          <option v-for="o in f.options" :key="o.value" :value="o.value">{{ o.desc || o.value }}</option>
        </select>
        <span v-else class="inp-wrap">
          <input
            v-model="form[f.key]"
            class="inp"
            :type="f.secret && !showSecret ? 'password' : 'text'"
            :placeholder="f.placeholder || ''"
            autocomplete="off"
          />
          <button v-if="f.secret" class="eye" data-tip="显示/隐藏" @click="showSecret = !showSecret">
            {{ showSecret ? '隐藏' : '显示' }}
          </button>
        </span>
        <span class="fh">{{ fieldHint(f) }}</span>
      </label>
      <div class="imc-actions">
        <button class="btn primary" :disabled="busy" @click="submit">{{ spec?.action || '保存并校验' }}</button>
        <button class="btn" :disabled="busy" @click="refresh">刷新状态</button>
        <a v-if="spec?.login_url" class="link" :href="spec.login_url" target="_blank" rel="noopener noreferrer">前往开放平台创建机器人</a>
      </div>
      <div class="imc-note">密钥仅用于换取 access_token:不回显、不落日志,保存后只显示尾号。</div>
    </div>

    <div v-else class="imc-note">该渠道不支持面板连接配置(请用通道命令)。</div>
  </div>
</template>

<style scoped>
.imc {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.imc-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.imc-ch {
  font-weight: 650;
  font-size: 14px;
}
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--line-strong);
}
.dot.on {
  background: var(--ok);
}
.imc-phase {
  font-size: 12px;
  color: var(--fg-dim);
}
.imc-acct {
  margin-left: auto;
  font-size: 11px;
  color: var(--fg-faint);
}
.imc-hint {
  font-size: 12px;
  color: var(--fg-dim);
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  padding: 8px 10px;
  line-height: 1.6;
}
.imc-status {
  font-size: 12.5px;
  color: var(--fg-dim);
}
.imc-status.err {
  color: var(--err);
  white-space: pre-wrap;
}
.imc-qr {
  display: flex;
  align-items: center;
  justify-content: center;
  background: #fff;
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  padding: 12px;
  min-height: 180px;
}
.imc-qr img {
  width: 180px;
  height: 180px;
  image-rendering: pixelated;
}
.imc-qr-empty {
  color: var(--fg-faint);
  font-size: 12.5px;
}
.imc-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.btn {
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  color: var(--fg-dim);
  cursor: pointer;
  font-size: 12.5px;
  padding: 6px 12px;
}
.btn:hover:not(:disabled) {
  border-color: var(--line-strong);
  color: var(--fg);
}
.btn.primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--fg-on-accent);
}
.btn.primary:hover:not(:disabled) {
  background: var(--accent-hover);
  color: var(--fg-on-accent);
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.link {
  color: var(--accent);
  font-size: 12px;
  text-decoration: none;
}
.link:hover {
  text-decoration: underline;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.fl {
  font-size: 12px;
  color: var(--fg-dim);
}
.req {
  color: var(--err);
  margin-left: 2px;
}
.inp-wrap {
  position: relative;
  display: flex;
}
.inp {
  flex: 1;
  background: var(--bg2);
  border: 1px solid var(--line);
  border-radius: var(--r-input);
  color: var(--fg);
  font-size: 12.5px;
  padding: 6px 10px;
}
.inp:focus {
  outline: none;
  border-color: var(--accent);
}
.eye {
  background: none;
  border: none;
  color: var(--accent);
  cursor: pointer;
  font-size: 11px;
  padding: 0 6px;
}
.fh {
  font-size: 11px;
  color: var(--fg-faint);
}
.imc-note {
  font-size: 11.5px;
  color: var(--fg-faint);
  line-height: 1.6;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
</style>
