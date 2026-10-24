<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";

const { backupMsg, backups, busy, doBackupNow, doBackupRestore, fmtSize } = inject(SETTINGS_CTX) as SettingsCtx;
</script>

<template>
  <section data-sec="backup" class="sec">
                <h3 class="h">数据备份</h3>
                <div class="row acts">
                  <button class="ghost" :disabled="busy" @click="doBackupNow()">
                    立即备份
                  </button>
                  <button
                    v-if="backups.length"
                    class="ghost danger-text"
                    :disabled="busy"
                    :data-tip="'恢复 ' + backups[0].Name"
                    @click="doBackupRestore()"
                  >
                    恢复最新备份
                  </button>
                </div>
                <p v-if="backups.length" class="dim">
                  最近备份:{{ backups[0].Name }} · {{ fmtSize(backups[0].Size) }}
                  <span v-if="backups.length > 1"
                    >(共 {{ backups.length }} 份)</span
                  >
                </p>
                <p v-else class="dim">暂无备份(/backup 或立即备份创建第一份)</p>
                <p v-if="backupMsg" class="dim ok">{{ backupMsg }}</p>
              </section>
</template>

