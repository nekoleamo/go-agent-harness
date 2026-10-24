<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";
import ScopeNote from "../ScopeNote.vue";

const { busy, curModelLabel, filteredModels, hiddenModelCount, modelFilter, modelOptions, modelVal, models, noPriceInfoProviders, onlyUsableModels, pickModel, props } = inject(SETTINGS_CTX) as SettingsCtx;
</script>

<template>
  <section data-sec="model" class="sec">
                <h3 class="h">模型</h3>
                <!-- 作用域声明(第一百三十四批):本段的控件改的是**全局默认**,
                     下方那一行显示的是**本会话当前在用**的值。两者不是一回事,
                     原来只有一个没标作用域的「当前生效」⇒ 用户以为改的是全局、实际只改了当前页签。 -->
                <ScopeNote what="模型" />
                <div class="row">
                  <span class="lab-inline">当前模型</span>
                  <input
                    id="set-model"
                    v-model="modelFilter"
                    class="inp grow"
                    :placeholder="'筛选模型名/Provider'"
                    :disabled="busy"
                  />
                </div>
                <!-- 当前生效单独一行:上面那个输入框是筛选框,用户会把它当成「当前模型」的显示位,
                 真机上因此得出「已选了模型但当前模型显示为空」的结论。
                 这一行是「当前状态」不是说明文字 → 用强调色 + 加重,别与灰色提示同级
                 (2026-10-03 用户反馈:这一行应为强调色,更显眼)。 -->
                <p class="cur-line" data-testid="cur-model">
                  <span class="cur-key">本会话在用</span>
                  <span class="cur-val">{{ curModelLabel || "(未设置)" }}</span>
                  <span
                    v-if="props.state.model_from === 'role'"
                    class="sstate ss-ok"
                    >角色指定</span
                  >
                  <span
                    v-if="
                      props.state.model_from === 'role' &&
                      props.state.model_session
                    "
                    class="dim"
                  >
                    （会话档
                    {{ props.state.model_session }} 被角色覆盖，停用角色后生效）
                  </span>
                </p>
                <p
                  v-if="props.state.model_from === 'role'"
                  class="dim"
                  data-testid="model-role-note"
                >
                  当前角色已指定模型：这里切换只改会话档，实际仍按角色跑。
                </p>
                <p v-if="noPriceInfoProviders.length" class="dim">
                  {{ noPriceInfoProviders.join("、") }}
                  未提供价格信息，判不出哪些模型免费（免费标记只依据端点自述的价格，
                  gah 不内置任何"某家免费"的说法——那些政策变得比软件快）。
                </p>
                <div class="m-list">
                  <label class="m-filter dim">
                    <input type="checkbox" v-model="onlyUsableModels" />
                    只看能当 agent 用的
                    <span v-if="hiddenModelCount > 0" class="dim">
                      （已隐去 {{ hiddenModelCount }} 条：端点声明不支持工具调用，或上下文
                      太小；关掉开关就能看到，它们会在标签后标出原因）
                    </span>
                  </label>
                  <div
                    v-for="o in filteredModels"
                    :key="o.value"
                    class="m-item"
                    :class="{ on: modelVal === o.value }"
                    data-tip="点击切换模型"
                    @click="pickModel(o)"
                  >
                    <span class="m-lab">{{ o.label }}</span>
                    <span v-if="o.warn" class="m-warn" :data-tip="o.warn">⚠</span>
                  </div>
                  <p v-if="!filteredModels.length" class="dim">
                    {{
                      onlyUsableModels
                        ? "没有能当 agent 用的模型(可关掉上方开关查看全部,或换一个 Provider)"
                        : "无匹配模型(清空筛选或经 /model 手动设置)"
                    }}
                  </p>
                </div>
                <p v-if="!models.length && !modelOptions.length" class="dim">
                  模型列表不可用(可经 Provider 配置或 /model 手动设置)
                </p>
              </section>
</template>

<style scoped src="../settings/settings-panel.css"></style>
