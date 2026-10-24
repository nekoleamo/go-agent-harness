<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";

const { autoState, isDesktop } = inject(SETTINGS_CTX) as SettingsCtx;
</script>

<template>
  <section v-if="isDesktop" data-sec="about" class="sec">
                <h3 class="h">关于 gah</h3>
                <p class="dim" data-testid="about-autostart">
                  开机自启:{{
                    autoState === "on"
                      ? "已启用"
                      : autoState === "off"
                        ? "未启用"
                        : "未知"
                  }}
                </p>
                <p class="dim">
                  升级入口固定在面板底部(不随内容滚动):点「检查更新」即查 GitHub
                  Release 上的新版本,有更新会自动下载安装并重启。
                </p>
              </section>
</template>

