<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";

const { addProvider, applied, applyPreset, busy, deleteProvider, doProviderUse, onboardHint, pf, pfConflicts, presets, probe, providerNames, providers, reprobe, showAdd, toggleAdd } = inject(SETTINGS_CTX) as SettingsCtx;
</script>

<template>
  <section data-sec="provider" class="sec">
                <h3 class="h">
                  Provider
                  <button
                    class="link"
                    data-tip="新增/编辑 LLM 端点"
                    @click="toggleAdd"
                  >
                    {{ showAdd ? "收起" : "＋ 新增" }}
                  </button>
                </h3>

                <!-- 一个 Key 就能开始:没有 provider 时空状态本身就是入口。
                     点完预设会自动展开下方表单(加 !showAdd 是为了不出现两排一模一样的 chip)。 -->
                <div v-if="!providers.length && !showAdd" class="onboard">
                  <p class="ob-lead">
                    粘贴一个 API Key 就能开始,base_url 由预设填好。
                  </p>
                  <div class="ob-row">
                    <button
                      v-for="p in presets"
                      :key="p.name"
                      class="chip"
                      :data-tip="p.note"
                      @click="applyPreset(p)"
                    >
                      {{ p.label }}
                    </button>
                  </div>
                  <p class="dim">{{ onboardHint }}</p>
                </div>

                <div v-if="showAdd" class="add-form">
                  <!-- 预设入口:**不能只存在于空状态**。
                       原实现里预设 chips 整块包在 `!providers.length` 里 ⇒ 配好第一个之后
                       预设全部消失,而「＋ 新增」打开的表单只能手打 base_url ——
                       「已添加一个 provider 之后就无法再通过预设添加」就是这么来的
                       (2026-10-06 用户反馈)。现在无论有没有 provider，点「＋ 新增」都能从预设起步。 -->
                  <div class="chip-row">
                    <button
                      v-for="p in presets"
                      :key="p.name"
                      class="chip"
                      :class="{ added: providerNames.has(p.name) }"
                      :data-tip="
                        providerNames.has(p.name)
                          ? p.note + '(已添加:保存会更新它)'
                          : p.note
                      "
                      @click="applyPreset(p)"
                    >
                      {{ p.label }}
                    </button>
                  </div>
                  <p v-if="pfConflicts" class="dim">
                    「{{ pfConflicts }}」已存在：保存会**更新**它（换成这次的 api_key），不是新增一条；
                    想多接一家请选别的预设，或自己改个名称。
                  </p>
                  <label class="fld">
                    <span class="fld-lab">名称</span>
                    <input
                      v-model="pf.name"
                      class="inp"
                      placeholder="如 deepseek"
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">base_url</span>
                    <input
                      v-model="pf.base_url"
                      class="inp mono"
                      placeholder="https://api.deepseek.com/v1"
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">api_key</span>
                    <input
                      ref="keyInput"
                      v-model="pf.api_key"
                      class="inp mono"
                      type="password"
                      autocomplete="off"
                      :placeholder="
                        applied?.key_optional ? '本地端点留空即可' : 'sk-…'
                      "
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">默认模型(可选)</span>
                    <input
                      v-model="pf.model"
                      class="inp mono"
                      placeholder="留空则在保存后从模型下拉里选"
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">自定义请求头(可选,一行一个「键: 值」)</span>
                    <textarea
                      v-model="pf.headers"
                      class="inp mono"
                      rows="2"
                      placeholder="x-opencode-session: ${session}"
                    ></textarea>
                    <span class="dim"
                      >网关按请求头路由或限流时才需要。支持
                      <code>${session}</code> / <code>${version}</code> /
                      <code>${cwd}</code> 三个占位符(保存时由服务端展开);值留空表示删掉
                      这个键。注意:头值会**明文**存在 provider.yaml 里,长期凭据请放
                      api_key。</span
                    >
                  </label>
                  <div class="form-acts">
                    <button
                      class="ghost solid"
                      :disabled="busy"
                      @click="addProvider"
                    >
                      保存 Provider
                    </button>
                  </div>
                </div>

                <!-- 连通性自检:保存后自动跑;失败给人话 + 原始报错 + 重试 -->
                <div v-if="probe" class="probe" :class="probe.ok ? 'ok' : 'bad'">
                  <span>{{ probe.text }}</span>
                  <span v-if="probe.raw" class="probe-raw mono">{{
                    probe.raw
                  }}</span>
                  <button
                    v-if="!probe.ok"
                    class="link"
                    :disabled="busy"
                    @click="reprobe"
                  >
                    重新自检
                  </button>
                </div>

                <div class="plist">
                  <div
                    v-for="p in providers"
                    :key="p.Name"
                    class="prow"
                    :class="{ active: p.Active }"
                  >
                    <div class="pmain">
                      <span class="pname">{{ p.Name }}</span>
                      <span v-if="p.Active" class="pbadge">活跃</span>
                      <span class="psub mono">{{
                        p.BaseURL.replace(/^https?:\/\//, "")
                      }}</span>
                      <span class="psub mono">{{ p.Model }}</span>
                    </div>
                    <div class="pops">
                      <button
                        v-if="!p.Active"
                        class="ghost"
                        data-tip="切换为活跃"
                        @click="doProviderUse(p.Name)"
                      >
                        启用
                      </button>
                      <button
                        class="ghost danger-text"
                        data-tip="删除该 Provider(不可恢复)"
                        @click="deleteProvider(p)"
                      >
                        删除
                      </button>
                    </div>
                  </div>
                  <p v-if="!providers.length" class="dim">
                    还没有配置 Provider:点上方「＋ 新增」或直接选一个预设
                  </p>
                </div>
              </section>
</template>

<style scoped src="../settings/settings-panel.css"></style>
