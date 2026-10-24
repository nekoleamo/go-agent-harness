<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";

const { applyHistory, busy, compactNow, histN } = inject(SETTINGS_CTX) as SettingsCtx;
</script>

<template>
  <section data-sec="history" class="sec">
                <h3 class="h">会话历史</h3>
                <div class="row">
                  <span class="lab-inline">历史注入</span>
                  <select v-model="histN" class="sel grow" @change="applyHistory">
                    <option :value="0">全部上下文</option>
                    <option :value="20">最近 20 条</option>
                    <option :value="10">最近 10 条</option>
                    <option :value="-1">不注入历史</option>
                  </select>
                </div>
                <div class="row acts">
                  <button
                    class="ghost danger-text"
                    :disabled="busy"
                    @click="compactNow"
                  >
                    压缩当前会话
                  </button>
                </div>
                <p class="dim">
                  压缩将最旧内容折叠为摘要(节省上下文),仅影响后续回合。
                </p>
              </section>
</template>

<style scoped src="../settings/settings-panel.css"></style>
