<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";

const { MCP_MODE_DIRECT, MCP_MODE_SEARCH, busy, discardMcp, fmtCommand, isRemote, mcpAdd, mcpDirty, mcpDrafts, mcpEnvRows, mcpErr, mcpMsg, mcpNotices, mcpRemove, mcpRowState, mcpRowStateClass, mcpView, modeHint, modeLabel, saveMcp, serverStateLabel, sourceLabel } = inject(SETTINGS_CTX) as SettingsCtx;
</script>

<template>
  <section data-sec="mcp" class="sec">
                <h3 class="h">
                  MCP server
                  <button
                    class="link"
                    data-tip="新增一个 MCP server"
                    @click="mcpAdd"
                  >
                    ＋ 添加
                  </button>
                  <span v-if="mcpDirty" class="dirty">未保存</span>
                </h3>
                <p class="dim">
                  MCP
                  让模型用上外部工具(记忆、代码图谱、数据库等)。每行一条启动命令,与你终端里输入的一致。
                  <strong>全量注册</strong>适合工具少的
                  server;<strong>按需检索</strong>适合工具多的
                  server(工具不进每轮上下文,模型先 mcp_search 查、再 mcp_call
                  调)。
                </p>
                <div v-if="mcpErr" class="serr">{{ mcpErr }}</div>
                <p v-if="mcpMsg" class="dim ok">{{ mcpMsg }}</p>
                <div v-for="n in mcpNotices" :key="n" class="serr">{{ n }}</div>

                <div class="plist">
                  <div
                    v-for="(r, i) in mcpDrafts"
                    :key="'mcp' + i"
                    class="prow scrow"
                    :class="{ off: !r.enabled }"
                  >
                    <div class="pmain">
                      <label class="fld">
                        <span class="fld-lab"
                          >名称(决定工具前缀
                          mcp_&lt;名称&gt;_*,留空则不加前缀)</span
                        >
                        <input
                          v-model="r.name"
                          class="inp mono"
                          placeholder="deja"
                        />
                      </label>
                      <label v-if="!isRemote(r)" class="fld">
                        <span class="fld-lab">启动命令</span>
                        <input
                          v-model="r.command"
                          class="inp mono"
                          placeholder="npx -y @modelcontextprotocol/server-memory"
                        />
                      </label>
                      <!-- 远程 server(transport: http):没有本地进程,只有 url。
                           只读展示而不是空命令框 —— 空框会让人以为配置丢了,进而「补」上一条
                           命令把它变成 stdio(后端 MergePreserve 只补不覆盖,那种改动会生效)。 -->
                      <div v-else class="fld">
                        <span class="fld-lab">远程地址(只读,改请编辑 config/mcp.yaml)</span>
                        <span class="psub mono brk">{{ r.url }}</span>
                      </div>
                      <div class="mrow">
                        <label class="chk">
                          <input v-model="r.enabled" type="checkbox" />
                          <span>启用</span>
                        </label>
                        <select v-model="r.mode" class="sel">
                          <option :value="MCP_MODE_DIRECT">全量注册</option>
                          <option :value="MCP_MODE_SEARCH">按需检索</option>
                        </select>
                      </div>
                      <span class="psub">{{ modeHint(r.mode) }}</span>
                    </div>
                    <div class="sacts">
                      <span class="sstate" :class="mcpRowStateClass(r)">{{
                        mcpRowState(r)
                      }}</span>
                      <button
                        class="ghost danger-text"
                        data-tip="从配置里移除"
                        @click="mcpRemove(i)"
                      >
                        删除
                      </button>
                    </div>
                  </div>

                  <div
                    v-for="s in mcpEnvRows"
                    :key="'env' + s.name"
                    class="prow scrow"
                  >
                    <div class="pmain">
                      <span class="sname">
                        {{ s.name || "(未命名)" }}
                        <span class="sstate ss-none">{{
                          sourceLabel(s.source)
                        }}</span>
                      </span>
                      <span class="psub mono">{{ fmtCommand(s) }}</span>
                      <span class="psub"
                        >{{ modeLabel(s.mode) }} · {{ serverStateLabel(s) }}</span
                      >
                    </div>
                    <div class="sacts">
                      <span class="dim">来自环境变量(env.sh),改后需重启 gah</span>
                    </div>
                  </div>

                  <p v-if="!mcpDrafts.length && !mcpEnvRows.length" class="dim">
                    还没有 MCP server:点上方「＋
                    添加」写一条启动命令,保存后模型立刻能用上它的工具。
                  </p>
                </div>

                <div class="row acts">
                  <button
                    class="ghost solid"
                    :disabled="busy || !mcpDirty"
                    @click="saveMcp"
                  >
                    保存并重载
                  </button>
                  <button
                    class="ghost"
                    :disabled="busy || !mcpDirty"
                    @click="discardMcp"
                  >
                    放弃修改
                  </button>
                </div>
                <p v-if="mcpView" class="dim">
                  配置文件:{{ mcpView.path }}
                  <span v-if="!mcpView.reload_available"
                    >(当前构建不能热重载,改完需重启 gah)</span
                  >
                </p>
              </section>
</template>

