<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";

const { memBudget, memBusy, memCand, memContent, memDraft, memEnabled, memErr, memHasCandidates, memLimit, memMsg, memPath, memProject, memReady, memSource, memSources, memToday, memTodayMax, memUsed, memUser, memoryAccept, memoryAcceptAll, memoryAdd, memoryPropose, memoryReject, memoryRejectAll, memoryRemove, memoryRemoveSource, memoryToggle } = inject(SETTINGS_CTX) as SettingsCtx;
</script>

<template>
  <section v-if="memReady" data-sec="memory" class="sec">
                <h3 class="h">
                  记忆
                  <span v-if="!memEnabled" class="dim" data-testid="mem-off"
                    >注入已关</span
                  >
                </h3>
                <p class="dim">
                  跨会话记忆是<strong>事实性记录</strong>：每轮按预算注入系统提示，排在角色指令之后
                  —— 记忆
                  <strong>不能覆盖</strong
                  >指令与工作规则，冲突时以指令为准。记忆不会自动提取（要记住什么，由你
                  或 <span class="mono">/memory add</span> 明确写下来）。
                </p>
                <p class="dim">
                  存在纯 markdown 文件 <span class="mono">{{ memPath }}</span
                  >，写错了可以直接改这个文件（记忆层最大的风险是写错，给人一个文本文件比给一套管理工具更有用）。
                </p>
                <div v-if="memErr" class="serr">{{ memErr }}</div>
                <p v-if="memMsg" class="dim ok">{{ memMsg }}</p>
                <p class="dim">
                  用户级 {{ memUser.length }} 条／本项目
                  {{ memProject.length }} 条／注入预算 {{ memBudget }}
                  字节（超预算时按时间倒序丢最旧）
                </p>
                <template v-if="memHasCandidates">
                  <p class="dim" data-testid="mem-cand-head">
                    候选池 {{ memUsed }}/{{ memLimit }} 条（今日已提
                    {{ memToday }}/{{ memTodayMax }}）——
                    <strong>候选不进上下文</strong>，只有「转正」之后才生效。文件
                    <span class="mono">{{
                      memPath.replace(/user\.md$/, "candidates.md")
                    }}</span>
                    同样可以直接改。
                  </p>
                  <div v-if="memCand.length" class="mem-list">
                    <div
                      v-for="(l, i) in memCand"
                      :key="'c' + i"
                      class="mem-item"
                    >
                      <span class="mem-txt" :data-tip="memContent(l)">{{
                        memContent(l)
                      }}</span>
                      <button
                        class="ghost solid"
                        :disabled="memBusy"
                        data-tip="转正为记忆(之后每轮带进上下文)"
                        @click="memoryAccept(i + 1)"
                      >
                        转正
                      </button>
                      <button
                        class="ghost danger-text"
                        :disabled="memBusy"
                        data-tip="丢弃(不进记忆)"
                        @click="memoryReject(i + 1)"
                      >
                        丢弃
                      </button>
                    </div>
                  </div>
                  <div v-if="memCand.length" class="form-acts">
                    <button
                      class="ghost solid"
                      :disabled="memBusy"
                      data-testid="mem-accept-all"
                      @click="memoryAcceptAll"
                    >
                      全部转正
                    </button>
                    <button
                      class="ghost danger-text"
                      :disabled="memBusy"
                      @click="memoryRejectAll"
                    >
                      全部丢弃
                    </button>
                  </div>
                  <p v-else class="dim">候选池是空的。</p>
                </template>
                <div class="add-form">
                  <label class="fld">
                    <span class="fld-lab">记一条（写清“是什么、为什么”）</span>
                    <input
                      v-model="memDraft"
                      class="inp"
                      :disabled="memBusy"
                      placeholder="比如：报告里的图表用蓝灰配色，不要渐变"
                      data-testid="mem-input"
                      @keyup.enter="memoryAdd"
                    />
                  </label>
                  <div class="form-acts">
                    <button
                      class="ghost solid"
                      :disabled="memBusy || !memDraft.trim()"
                      data-testid="mem-add"
                      @click="memoryAdd"
                    >
                      记住
                    </button>
                    <button
                      v-if="memHasCandidates"
                      class="ghost"
                      :disabled="memBusy || !memDraft.trim()"
                      data-testid="mem-propose"
                      data-tip="先进候选池,确认之后才进上下文"
                      @click="memoryPropose"
                    >
                      提候选
                    </button>
                    <button
                      v-if="memEnabled"
                      class="ghost danger-text"
                      data-tip="只关注入，记忆文件保留"
                      @click="memoryToggle(false)"
                    >
                      关掉注入
                    </button>
                    <button
                      v-else
                      class="ghost"
                      data-tip="重新把记忆注入系统提示"
                      @click="memoryToggle(true)"
                    >
                      开启注入
                    </button>
                  </div>
                </div>
                <template v-if="memUser.length">
                  <div class="mem-list">
                    <div v-for="(l, i) in memUser" :key="i" class="mem-item">
                      <span class="mem-txt" :data-tip="memContent(l)">{{
                        memContent(l)
                      }}</span>
                      <span v-if="memSource(l)" class="dim mono">{{
                        memSource(l)
                      }}</span>
                      <button
                        class="ghost danger-text"
                        :disabled="memBusy"
                        data-tip="删除这条记忆"
                        @click="memoryRemove(i + 1, l)"
                      >
                        删除
                      </button>
                    </div>
                  </div>
                  <div v-if="memSources.length" class="mem-src">
                    <p class="dim">
                      按来源整段删（某个会话写进来的记忆一次性清干净）
                    </p>
                    <div class="form-acts">
                      <button
                        v-for="s in memSources"
                        :key="s"
                        class="ghost danger-text"
                        :disabled="memBusy"
                        data-tip="删掉该来源的全部记忆"
                        @click="memoryRemoveSource(s)"
                      >
                        会话 {{ s }}
                      </button>
                    </div>
                  </div>
                </template>
                <p v-else class="dim">
                  还没有记忆。记一条试试，比如“部署前先跑一次 /verify”。
                </p>
                <div v-if="memProject.length" class="mem-list">
                  <div
                    v-for="(l, i) in memProject"
                    :key="'p' + i"
                    class="mem-item"
                  >
                    <span class="mem-txt dim" :data-tip="memContent(l)">{{
                      memContent(l)
                    }}</span>
                  </div>
                </div>
              </section>
</template>

<style scoped src="../settings/settings-panel.css"></style>
