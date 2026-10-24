<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";

const { PLUGIN_CAP, bundledPlugins, digestLine, doCheckPluginUpdates, doDisablePlugin, doEnablePlugin, doInstall, doReload, doTrustPlugin, doUIInstall, doUIToggle, doUITrust, doUIUninstall, doUninstallPlugin, doUntrustPlugin, installBusy, installErr, installOk, installPrebuilt, installRows, installSpec, installWarn, isDesktop, manageMeta, pickInstallDir, pickUIDir, pluginExpanded, pluginFilter, pluginHidden, pluginHits, pluginShown, pluginStateLabel, pluginToggle, plugins, rescanUIPlugins, sourceLine, uiBusy, uiErr, uiOk, uiPluginDigests, uiPluginErrors, uiPluginList, uiPluginTrustEnforced, uiPluginTrustNote, uiScanBusy, uiSpec, updChecking, updChecks, updErr, userPlugins } = inject(SETTINGS_CTX) as SettingsCtx;
</script>

<template>
  <section data-sec="plugin" class="sec">
                <h3 class="h">
                  插件
                  <span class="h-sub"
                    >{{ pluginShown.length }}/{{ plugins.length }}</span
                  >
                </h3>
                <!-- 插件安装(2026-10-03):CLI 的安装内核接进面板。两条来源都支持 ——
                 第三方仓库与**本地自己写的目录**(不需要先 git init + push)。
                 每次安装/卸载/登记都走二次确认;审批档为「严格」时服务端直接拒。 -->
                <!-- 批九重排:三个子块各带标题与边框,控件行只留控件。
                 原先标签 / 输入框 / 按钮 / 一句很长的 checkbox 全挤在**一个 flex 行**里,
                 窗口一窄按钮就被压到折行(实测「检查更新」断成「检查更 / 新」),
                 整段读起来是一堵墙。现在长文案一律下沉到控件行**下面**独占一行。 -->
                <div class="plg-block">
                  <div class="plg-block-t">安装外部插件</div>
                  <div class="plg-row">
                    <input
                      v-model="installSpec"
                      class="inp grow"
                      placeholder="仓库地址(git@…/@版本或 40 位 commit sha)或本地目录绝对路径"
                      :disabled="installBusy"
                      @keyup.enter="doInstall()"
                    />
                    <button
                      v-if="isDesktop"
                      class="ghost"
                      :disabled="installBusy"
                      data-tip="选择本地插件源码目录"
                      @click="pickInstallDir()"
                    >
                      浏览…
                    </button>
                    <button
                      class="ghost solid"
                      :disabled="installBusy || !installSpec.trim()"
                      @click="doInstall()"
                    >
                      {{ installBusy ? "处理中…" : "安装" }}
                    </button>
                  </div>
                  <label
                    class="chk plg-hint"
                    data-tip="只下载作者发布的预编译产物,不执行仓库里的构建脚本(本机可以没有 go/node/make)。要求 plugin.yaml 声明了当前平台;下载源由作者声明,gah 不验签名 —— 装的那一刻没有独立校验。"
                  >
                    <input
                      type="checkbox"
                      v-model="installPrebuilt"
                      :disabled="installBusy"
                      data-testid="install-prebuilt"
                    />
                    <span>只装预编译产物(不跑它的构建脚本)</span>
                  </label>
                  <p v-if="installOk" class="dim">{{ installOk }}</p>
                  <p v-if="installWarn" class="warn-line">{{ installWarn }}</p>
                  <p v-if="installErr" class="err-line">{{ installErr }}</p>
                </div>
                <!-- 检查更新(批一 §1.5):**只问不装**。没有自动更新通道 —— 在不签名的前提下,
                 那是一条无认证的、持续性的远程代码执行通道(理由见 internal/install/updatecheck.go)。
                 网络只在这个按钮被点时发生一次。 -->
                <div v-if="installRows.length" class="plg-block">
                  <div class="plg-block-t">检查更新</div>
                  <div class="plg-row">
                    <button
                      class="ghost"
                      :disabled="updChecking"
                      @click="doCheckPluginUpdates()"
                    >
                      {{ updChecking ? "检查中…" : "检查更新" }}
                    </button>
                  </div>
                  <p class="dim plg-hint">
                    gah
                    不自动更新插件:这里只查远端当前指向,装新版要你自己重跑安装命令
                  </p>
                  <p v-if="updErr" class="err-line">{{ updErr }}</p>
                  <ul v-if="updChecks.length" class="install-list">
                    <li v-for="c in updChecks" :key="c.plugin_id" class="irow">
                      <span class="pname">{{ c.plugin_id }}</span>
                      <span
                        class="psub"
                        :class="{ 'warn-line': c.status === 'tag_changed' }"
                      >
                        {{ c.repo }}{{ c.ref ? "@" + c.ref : "" }} ——
                        {{ c.message }}
                      </span>
                    </li>
                  </ul>
                </div>
                <details v-if="installRows.length" class="plg-block dim">
                  <summary>
                    已装外部插件({{ installRows.length }};白名单{{
                      installRows[0]?.enforced ? "强制" : "未启用"
                    }})
                  </summary>
                  <!-- 分组(批四 P3):两个集合的**处置完全不同** ——
                   「随 gah 附带」由 embed 每次启动与嵌入清单比对,用户碰它没有意义;
                   「你安装的」才需要登记 / 启停 / 卸载。混在一张表里,用户每次都要
                   先读一列才知道哪些按钮对自己有意义。 -->
                  <p v-if="bundledPlugins.length" class="dim">
                    随 gah 附带的插件({{ bundledPlugins.length }})
                  </p>
                  <ul v-if="bundledPlugins.length" class="install-list">
                    <li
                      v-for="r in bundledPlugins"
                      :key="'b-' + r.id"
                      class="irow"
                    >
                      <span class="pname">{{ r.id }}</span>
                      <span class="psub"
                        >{{ r.protocol || "bridge" }} · {{ r.binary }} · 随 gah
                        附带(由 embed 每次启动校验,不必手动登记)</span
                      >
                    </li>
                  </ul>
                  <p v-if="userPlugins.length" class="dim">
                    你安装的插件({{ userPlugins.length }})
                  </p>
                  <ul v-if="userPlugins.length" class="install-list">
                    <li v-for="r in userPlugins" :key="r.id" class="irow">
                      <span class="pname">{{ r.id }}</span>
                      <span class="psub"
                        >{{ r.protocol || "bridge" }} · {{ r.binary }} ·
                        <!-- 三态(启用 / 已停用 / 二进制缺失):三种情况**处置完全不同**,
                         而「工具不出现」这一个症状对应它们 —— 不分开呈现,用户只能猜。 -->
                        {{ pluginStateLabel(r) }}
                        <template v-if="r.audit_source">
                          · {{ r.audit_source }} @
                          {{ (r.audit_time || "").slice(0, 16) }}</template
                        >
                        <!-- 装机来源(批一):「它当初是从哪一份代码装的」—— 与上面的白名单状态
                         答的不是同一个问题,故另起一行而不是挤在同一行里。 -->
                        <template v-if="r.source_repo">
                          · 来源 {{ sourceLine(r) }}</template
                        >
                        <template v-if="!r.compat_ok">
                          ·
                          <span class="warn-line"
                            >协议版本 {{ r.api_version }} 不在本版 gah
                            的支持范围内</span
                          ></template
                        >
                      </span>
                      <span class="iact">
                        <button
                          v-if="!r.trusted && r.loadable"
                          class="ghost"
                          @click="doTrustPlugin(r.binary)"
                        >
                          登记
                        </button>
                        <button
                          v-if="r.trusted"
                          class="ghost"
                          @click="doUntrustPlugin(r.binary)"
                        >
                          撤销登记
                        </button>
                        <!-- 停用与卸载**文案不同**(批二):停用留文件与登记,卸载删全部。 -->
                        <button
                          v-if="!r.disabled && r.loadable"
                          class="ghost"
                          @click="doDisablePlugin(r.binary)"
                        >
                          停用
                        </button>
                        <!-- 「启用」在**未登记**时必然失败:Enable → reload → loadOne → verifyTrust 被完整性闸拒。
                         实测(2026-10-04 用户报):停用 + 撤销登记后点启用,报的是登记错误而不是启用错误,
                         而按钮看上去完全可点。所以这里**禁用并说明原因**,而不是让人点一次换一条看不懂的报错。 -->
                        <button
                          v-if="r.disabled"
                          class="ghost"
                          :disabled="!r.trusted"
                          :data-tip="
                            r.trusted
                              ? '加载这个插件'
                              : '需先登记:未登记的插件过不了完整性闸,启用必然失败'
                          "
                          @click="doEnablePlugin(r.binary)"
                        >
                          启用
                        </button>
                        <!-- 破坏性动作用**语义色**,不用灰:灰色读起来就是「禁用」。
                         刻意**不在已登记/已启用时禁用它** —— 那恰恰是最需要它的时刻
                         (一个行为不良的插件正是要靠卸载来止损),禁掉它等于把止损手段藏起来。 -->
                        <button
                          class="ghost danger"
                          @click="doUninstallPlugin(r.id)"
                        >
                          卸载并删除
                        </button>
                      </span>
                    </li>
                  </ul>
                </details>
                <!-- UI 插件(2026-10-03):信任模型 + 摘要 + **重扫入口** + 失败原因。
                 重扫解决的是「装了插件但没生效、只能靠刷新页面」;失败清单解决的是
                 「没生效」与「没被扫到」在界面上分不出来 —— 这两者的处置完全不同。 -->
                <!-- UI 插件(批九):**补上安装入口**。此前这一段只有「重新扫描」,
                 而它对**从没装过**的用户毫无用处(扫一个空目录),安装与放行都只在命令行有
                 —— 用户看到「UI 插件」却找不到任何能开始的动作。
                 UI 插件经动态 import() 进主页面,与宿主**同源同权限**,所以确认文案要自己讲清。 -->
                <div class="plg-block">
                  <div class="plg-block-t">UI 插件</div>
                  <div class="plg-row">
                    <input
                      v-model="uiSpec"
                      class="inp grow"
                      placeholder="仓库地址或本地目录绝对路径"
                      :disabled="uiBusy"
                      @keyup.enter="doUIInstall()"
                    />
                    <button
                      v-if="isDesktop"
                      class="ghost"
                      :disabled="uiBusy"
                      data-tip="选择本地 UI 插件源码目录"
                      @click="pickUIDir()"
                    >
                      浏览…
                    </button>
                    <button
                      class="ghost solid"
                      :disabled="uiBusy || !uiSpec.trim()"
                      @click="doUIInstall()"
                    >
                      {{ uiBusy ? "处理中…" : "安装" }}
                    </button>
                    <button
                      class="ghost"
                      :disabled="uiScanBusy"
                      @click="rescanUIPlugins()"
                    >
                      {{ uiScanBusy ? "扫描中…" : "重新扫描" }}
                    </button>
                  </div>
                  <p class="dim plg-hint">
                    UI 插件经<strong>动态 import()</strong> 进入主页面 ——
                    与宿主<strong>同源同权限</strong>, 可读全部会话内容、可用
                    cookie 调全部 API。装完重载页面即生效,不必重启 gah。
                  </p>
                  <p v-if="uiOk" class="dim">{{ uiOk }}</p>
                  <p v-if="uiErr" class="err-line">{{ uiErr }}</p>

                  <ul v-if="uiPluginList.length" class="install-list">
                    <li v-for="p in uiPluginList" :key="p.id" class="irow">
                      <span class="pname">{{ p.id }}</span>
                      <span class="psub">
                        v{{ p.version || "?" }} · {{ p.slots }} 槽位 ·
                        <template v-if="p.disabled">已停用</template>
                        <template v-else-if="p.trusted">已登记</template>
                        <template v-else>未登记(完整性闸拦下,不加载)</template>
                        <template v-if="p.reject"> —— {{ p.reject }}</template>
                      </span>
                      <span class="iact">
                        <button
                          v-if="!p.trusted && !p.disabled"
                          class="ghost"
                          @click="doUITrust(p.id)"
                        >
                          登记
                        </button>
                        <button
                          v-if="p.disabled"
                          class="ghost"
                          @click="doUIToggle(p.id, true)"
                        >
                          启用
                        </button>
                        <button
                          v-if="!p.disabled"
                          class="ghost"
                          @click="doUIToggle(p.id, false)"
                        >
                          停用
                        </button>
                        <button class="ghost danger" @click="doUIUninstall(p.id)">
                          卸载并删除
                        </button>
                      </span>
                    </li>
                  </ul>
                  <p v-else-if="uiScanBusy || !uiBusy" class="dim plg-hint">
                    还没装 UI 插件。
                  </p>
                  <p v-if="uiPluginTrustEnforced" class="warn-line plg-hint">
                    完整性闸已启用:手工放进
                    <code>ui-plugins/</code> 的插件默认**不加载**。
                    确认某个可信之后,在上面点「登记」放行(命令行等价物
                    <code>gah -trust-ui-plugin &lt;id&gt;</code>)。
                  </p>
                </div>
                <!-- 批四的闸说明已移进上面的 UI 子块内(原先它在块外,与「重新扫描」那段
                 隔着一整个已装列表,读起来像另一件事)。 -->
                <!-- 原先这里还有一条「已停用的 UI 插件」独立列表。批九**删掉**:installed 列表
                 每行已带 disabled 状态与启停按钮,两条列表会把同一个插件显示两遍。 -->
                <p v-if="uiPluginTrustNote" class="dim">
                  UI 插件(ui-plugins):{{ uiPluginTrustNote }}
                </p>
                <p v-if="uiPluginErrors.length" class="err-line">
                  {{ uiPluginErrors.length }} 个槽位加载失败:
                  <template v-for="(e, i) in uiPluginErrors" :key="e.id + e.slot">
                    {{ i ? ";" : "" }}{{ e.id }}·{{ e.slot }} — {{ e.reason }}
                  </template>
                </p>
                <!-- 产物完整性提示(R10 ⑤-3):sha256 覆盖范围与降级原因如实显示;值供人比对,
                 本面板不做"通过/不通过"判断(同源页面自证不构成安全边界) -->
                <details v-if="uiPluginDigests.length" class="dim">
                  <summary>
                    产物校验值(sha256,{{ uiPluginDigests.length }} 个 UI
                    插件;可与发布方公布的比对)
                  </summary>
                  <ul class="digests">
                    <li
                      v-for="d in uiPluginDigests"
                      :key="d.id"
                      :title="d.sha256 || '无摘要'"
                    >
                      {{ digestLine(d) }}
                    </li>
                  </ul>
                </details>
                <!-- data-testid:本段新增了「安装插件」的输入框,`[data-sec="plugin"] input`
                 已经**不唯一**(布局护栏按它 fill,装到安装框上,筛选就失效)。给筛选框一个
                 稳定锚点,别让"谁排在前面"决定语义。 -->
                <div v-if="plugins.length > PLUGIN_CAP" class="row">
                  <input
                    v-model="pluginFilter"
                    class="inp grow"
                    data-testid="plugin-filter"
                    placeholder="筛选插件 ID / 类型 / 状态"
                  />
                </div>
                <div class="plist">
                  <div v-for="p in pluginShown" :key="p.ID" class="prow">
                    <div class="pmain">
                      <span class="pname">{{ p.ID }}</span>
                      <span class="psub"
                        >{{ p.Type }} · {{ p.State }} ·
                        {{ manageMeta(p).label }}</span
                      >
                      <!-- 被拒(白名单不符等):必须显示原因与补救办法 ——
                       否则表现只是「插件不见了」,用户无从判断是没装还是被拦。 -->
                      <span v-if="p.rejected" class="pwhy" :title="p.rejected"
                        >被拒绝:{{ p.rejected }}</span
                      >
                    </div>
                    <!-- 外部化/场景专用:只读标记,web 侧禁用启停 -->
                    <button
                      v-if="p.manage === 'external' || p.manage === 'scenario'"
                      class="ghost"
                      :class="{ ro: true }"
                      :data-tip="manageMeta(p).tip"
                      disabled
                      @click="pluginToggle(p)"
                    >
                      只读
                    </button>
                    <button
                      v-else
                      class="ghost"
                      :class="{ 'danger-text': p.State === 'loaded' }"
                      :data-tip="p.State === 'loaded' ? '卸载(确认)' : '加载启用'"
                      @click="pluginToggle(p)"
                    >
                      {{ p.State === "loaded" ? "卸载" : "启用" }}
                    </button>
                  </div>
                  <p v-if="!plugins.length" class="dim">插件管理不可用</p>
                  <p v-else-if="!pluginHits.length" class="dim">没有匹配的插件</p>
                  <!-- 长列表折叠(前 5 条之外的默认不渲染);≤5 条时 pluginHidden 恒为 0,不出现按钮 -->
                  <button
                    v-if="pluginHidden > 0"
                    class="ghost more"
                    @click="pluginExpanded = true"
                  >
                    展开全部(还有 {{ pluginHidden }} 个)
                  </button>
                  <button
                    v-else-if="pluginExpanded && pluginHits.length > PLUGIN_CAP"
                    class="ghost more"
                    @click="pluginExpanded = false"
                  >
                    收起
                  </button>
                </div>
                <div class="row acts">
                  <button class="ghost" @click="doReload">重载指令文件</button>
                </div>
              </section>
</template>

<style scoped src="../settings/settings-panel.css"></style>
