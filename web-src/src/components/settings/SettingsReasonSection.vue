<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";
import ScopeNote from "../ScopeNote.vue";

const { AP, AP_ZH, SB, SB_ZH, THINK, THINK_LABEL, applyCtl, busy, onSyncToggle, props } = inject(SETTINGS_CTX) as SettingsCtx;
</script>

<template>
  <section data-sec="reason" class="sec">
                <h3 class="h">推理</h3>
                <!-- 同上:这一段的思考/沙箱/审批三个控件改的都是**全局默认**。 -->
                <ScopeNote what="思考 / 沙箱 / 审批" />
                <div class="row">
                  <span class="lab-inline">思考</span>
                  <div class="seg">
                    <button
                      v-for="t in THINK"
                      :key="t"
                      class="seg-it"
                      :class="{
                        on:
                          (props.state.thinking_session ??
                            props.state.thinking) === t,
                      }"
                      :data-tip="'思考 ' + THINK_LABEL[t]"
                      @click="applyCtl({ thinking: t })"
                    >
                      {{ THINK_LABEL[t] }}
                    </button>
                  </div>
                </div>
                <p
                  v-if="props.state.thinking_from === 'role'"
                  class="dim"
                  data-testid="thinking-role-note"
                >
                  当前角色的思考档覆盖了会话设置：实际按「{{
                    THINK_LABEL[props.state.thinking] || props.state.thinking
                  }}」跑（上面选的是会话档，停用角色后生效）。
                </p>
                <div class="row">
                  <span class="lab-inline">沙箱</span>
                  <div class="seg">
                    <button
                      v-for="s in SB"
                      :key="s.v"
                      class="seg-it"
                      :class="{ on: props.state.sandbox === s.v }"
                      :data-tip="'沙箱 ' + s.label"
                      @click="applyCtl({ sandbox: s.v })"
                    >
                      {{ s.label }}
                    </button>
                  </div>
                </div>
                <p
                  v-if="props.state.sandbox_from === 'role'"
                  class="dim"
                  data-testid="sandbox-role-note"
                >
                  当前角色把沙箱收紧为「{{
                    SB_ZH[props.state.sandbox_effective ?? ""] ??
                    props.state.sandbox_effective
                  }}」：写入被拦在这里，上面选的是**本会话**档（停用角色后生效）。
                </p>
                <div class="row">
                  <span class="lab-inline">审批</span>
                  <div class="seg">
                    <button
                      v-for="s in AP"
                      :key="s.v"
                      class="seg-it"
                      :class="{ on: props.state.approval === s.v }"
                      :data-tip="'审批 ' + s.label"
                      @click="applyCtl({ approval: s.v })"
                    >
                      {{ s.label }}
                    </button>
                  </div>
                </div>
                <!-- 上面选的是**本会话档**(这一段三个控件都经 api.control 默认带 session,第一百三十四批核实);
                 角色收紧时实际按更严的那个裁决(第九十二批)。
                 不说清就会变成"点了严格却不生效"的静默矛盾。 -->
                <p
                  v-if="props.state.approval_from === 'role'"
                  class="dim"
                  data-testid="approval-role-note"
                >
                  当前角色把审批收紧为「{{
                    AP_ZH[props.state.approval_effective ?? ""] ??
                    props.state.approval_effective
                  }}」：实际按它裁决（上面选的是**本会话**档，停用角色后生效）。
                </p>
                <!-- 联动开关(R10 ②-2):默认开启时审批档会覆盖沙箱档(开放 → 完全访问、
                 严格 → 只读),于是"改了沙箱档却不生效";关掉后沙箱档独立生效。
                 后端不支持时该字段缺失 → 整行不显示(不摆一个点了没反应的开关)。 -->
                <div v-if="props.state.sandbox_sync !== undefined" class="row">
                  <span class="lab-inline">档位联动</span>
                  <label
                    class="chk"
                    :data-tip="'审批档联动沙箱:开启时开放档 → 完全访问、严格档 → 只读'"
                  >
                    <input
                      type="checkbox"
                      :checked="props.state.sandbox_sync"
                      :disabled="busy"
                      @change="onSyncToggle"
                    />
                    <span>审批档覆盖沙箱档</span>
                  </label>
                  <span class="dim grow">{{
                    props.state.sandbox_sync ? "开启" : "关闭(沙箱档独立生效)"
                  }}</span>
                </div>
              </section>
</template>

