<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";

const { busy, discardInstr, instrBytes, instrDirty, instrDraft, instrEdit, instrErr, instrExists, instrMax, instrMsg, instrOver, instrPath, instrReady, instrWarn, saveInstructions } = inject(SETTINGS_CTX) as SettingsCtx;
</script>

<template>
  <section v-if="instrReady" data-sec="instr" class="sec">
                <h3 class="h">
                  指令
                  <span v-if="instrDirty" class="dirty">未保存</span>
                  <button
                    class="link"
                    data-tip="编辑全局 AGENTS.md"
                    @click="instrEdit = !instrEdit"
                  >
                    {{ instrEdit ? "收起" : "编辑" }}
                  </button>
                </h3>
                <p class="dim">
                  全局 <span class="mono">AGENTS.md</span>（<span class="mono">{{
                    instrPath
                  }}</span
                  >）对<strong>所有</strong>角色生效：没有角色时注入，有角色时也注入（除非该角色声明了「不注入全局
                  AGENTS.md」）。
                </p>
                <div v-if="instrErr" class="serr">{{ instrErr }}</div>
                <div v-if="instrWarn" class="swarn">{{ instrWarn }}</div>
                <p v-if="instrMsg" class="dim ok">{{ instrMsg }}</p>
                <p class="dim">
                  {{
                    instrExists
                      ? "当前 " + instrBytes + " 字节"
                      : "还没有这份文件（保存即创建）"
                  }}／上限 {{ instrMax }} 字节
                  <span v-if="instrOver"
                    >·
                    现有文件已超上限，保存会被拒；注入时按上限截断并标注「已截断」</span
                  >
                </p>
                <div v-if="instrEdit" class="add-form">
                  <label class="fld">
                    <span class="fld-lab">全局指令（原文，保存即覆盖）</span>
                    <textarea
                      v-model="instrDraft"
                      class="inp mono"
                      rows="12"
                    ></textarea>
                  </label>
                  <div class="form-acts">
                    <button
                      class="ghost solid"
                      :disabled="busy"
                      @click="saveInstructions"
                    >
                      保存全局指令
                    </button>
                    <button
                      v-if="instrDirty"
                      class="ghost danger-text"
                      @click="discardInstr"
                    >
                      放弃修改
                    </button>
                    <button class="ghost" @click="instrEdit = false">收起</button>
                  </div>
                </div>
              </section>
</template>

