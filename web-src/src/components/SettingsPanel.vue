<script setup lang="ts">
// 可视化设置抽屉(状态栏 ⚙ 入口;App 持有 open)。
// 分组:模型/推理(thinking·sandbox)/历史与压缩/Provider/插件与指令。
// 破坏性动作(删 provider、卸载插件、压缩)经全局确认条(askConfirm)。
import {
  computed,
  inject,
  nextTick,
  onMounted,
  onUnmounted,
  provide,
  ref,
  watch,
} from "vue";
import {
  api,
  rolePackDownloadUrl,
  rolePackName,
  skillPackDownloadUrl,
  skillPackName,
} from "../api";
import { byteLength } from "../bytes";
import {
  autostartState,
  checkUpdate,
  installUpdate,
  isDesktop,
  pickDirectory,
  updateState,
  type UpdateSnapshot,
} from "../desktop";
import {
  currentModelValue,
  modelOptionValue,
  modelRank,
  withCurrentModel,
} from "../modelsel";
import type { ModelOption } from "../modelsel";
import { parseHeaderLines } from "../providerheaders";
import { settingSections } from "../registry";
import { SETTINGS_CTX, type SettingsCtx } from "./settings/ctx";
import SettingsPluginSection from "./settings/SettingsPluginSection.vue";
import SettingsMcpSection from "./settings/SettingsMcpSection.vue";
import SettingsScheduleSection from "./settings/SettingsScheduleSection.vue";
import SettingsRoleSection from "./settings/SettingsRoleSection.vue";
import SettingsAboutSection from "./settings/SettingsAboutSection.vue";
import SettingsBackupSection from "./settings/SettingsBackupSection.vue";
import SettingsProviderSection from "./settings/SettingsProviderSection.vue";
import SettingsMemorySection from "./settings/SettingsMemorySection.vue";
import SettingsInstrSection from "./settings/SettingsInstrSection.vue";
import SettingsReasonSection from "./settings/SettingsReasonSection.vue";
import SettingsModelSection from "./settings/SettingsModelSection.vue";
import SettingsHistorySection from "./settings/SettingsHistorySection.vue";
import { hasSessionOverride } from "../scope";
import {
  loadUIPlugins,
  uiPluginDigests,
  uiPluginErrors,
  uiPluginTrustNote,
} from "../plugins";
import { digestLine } from "../plugininfo";
import {
  PROVIDER_PRESETS,
  explainProbeError,
  type ProviderPreset,
} from "../providers";
import { fmtAbs, fmtNextRun, statusLabel } from "../schedule";
import {
  DEFAULT_FORM,
  LUNAR_FESTIVALS,
  LUNAR_MONTHS,
  PRESETS,
  REPEATS,
  applyPreset as withPreset,
  costHint,
  formLunarMD,
  formLunarPhrase,
  lunarDayText,
  previewLines,
  skipHint,
  structToCron,
  todayStr,
  viewToForm,
} from "../schedule";
import type { ScheduleForm } from "../schedule";
import type { ScheduleView } from "../types";
import {
  MCP_MODE_DIRECT,
  MCP_MODE_SEARCH,
  canEdit,
  draftFrom,
  draftKey,
  fmtCommand,
  modeHint,
  modeLabel,
  serverStateLabel,
  sourceLabel,
  isRemote,
  toSavePayload,
  validateDraft,
  viewNotices,
  type McpDraft,
} from "../mcp";
import type {
  AskConfirm,
  InstallView,
  McpServer,
  McpView,
  PluginInfo,
  ProviderInfo,
  ProviderModelGroup,
  RolePackResult,
  RoleSpec,
  Schedule,
  SkillInfo,
  StateView,
  ToolDef,
  TrashRoleEntry,
  TrashSkillEntry,
  UIPluginView,
  UpdateCheck,
} from "../types";

const props = defineProps<{
  open: boolean;
  state: StateView;
  focus?: string; // 打开时定位到某一段(首启引导传 'provider')
  schedTick?: number; // 定时计划运行信号(App 收到 schedule/run 帧后 +1)
}>();
const emit = defineEmits<{ (e: "close"): void; (e: "changed"): void }>();

const ask = inject<(a: AskConfirm) => void>("askConfirm");

// 桌面壳专属能力:判定与调用都收在 ../desktop(配了单测 —— 那条判定错了就会表现为
// 「桌面版里没有桌面功能」,而页面上无从自证)。
const updBusy = ref(false);
const updMsg = ref("");
const updOk = ref(false);
// updAvailable 有更新待确认:此时底栏多一个「立即升级」按钮,且**必须**先经过二次确认。
// 2026-10-03 用户要求:「检查更新后,应先提示是否需要更新,确认升级后,再下载安装」。
const updAvailable = ref("");

// —— 插件安装(2026-10-03)——
// 两段式安装:先 preview 取确认文案(与服务端 ConfirmPrompt 同一份),用户点头后才真装。
// 服务端**没有**确认服务,故必须由前端带 confirmed:true;忘了弹确认 ⇒ 400,而不会静默安装。
const installSpec = ref("");
// installPrebuilt 走作者发布的预编译产物(批三):不执行仓库里的构建脚本。
// 默认**关**:它要求 plugin.yaml 声明了当前平台的产物,而多数插件没有;而且产物是
// **下载来的**(作者声明来源 + 不验签名),这是一次真实的取舍,不该替用户默认。
const installPrebuilt = ref(false);
const installBusy = ref(false);
const installRows = ref<InstallView[]>([]);
const installErr = ref("");
const installOk = ref("");
// installWarn 独立于 installOk:成功回执会被下一条操作冲掉,这类长期影响必须占一行不混进去。
const installWarn = ref("");

async function refreshInstallList(): Promise<void> {
  try {
    // ⚠️ **形状必须在这里归一**,不能把响应字段直接塞进 ref(2026-10-04 v0.5.0 事故)。
    //
    // 事由:`uiPluginDisabled` 直接接 `st.disabled`。只要响应不是预期形状(桩返回 `{}`、
    // 旧版服务端、代理层兜底),它就是 `undefined` ⇒ 模板里的 `uiPluginDisabled.length`
    // 在**渲染期**抛 TypeError ⇒ Vue 的更新中断 ⇒ **整个设置面板不出现** ——
    // 而用户看到的只是「点设置没反应」,面板里一条错误都没有。
    //
    // 判据:渲染期任何一处异常都会让**整块 UI 消失**,而不只是那一个控件坏掉。
    // 所以凡是进模板的数组,一律在**边界**上收敛成数组,不给 undefined 留路。
    const rows = await api.pluginInstallList();
    installRows.value = Array.isArray(rows) ? rows : [];
    await refreshUIState();
  } catch (e) {
    installErr.value = "读取已装插件失败:" + (e as Error).message;
  }
}

async function doInstall(): Promise<void> {
  const spec = installSpec.value.trim();
  if (!spec || installBusy.value) return;
  installErr.value = "";
  installOk.value = "";
  installWarn.value = "";
  installBusy.value = true;
  try {
    const pv = await api.pluginInstallPreview(spec, installPrebuilt.value);
    // 二次确认(与删 provider / 卸载角色同一处确认条):文案由服务端生成,
    // 三个入口(CLI / TUI / 面板)看到的是同一句话。
    await new Promise<void>((done) => {
      guard(pv.prompt, true, () => {
        done();
        void doPluginInstallNow(spec);
      });
    });
  } catch (e) {
    installErr.value = "安装失败:" + (e as Error).message;
    // 漂移拒绝要给一条**可点的出路**:再输一遍同样的命令只会再被拒一次。
    // 判据取服务端文案里的标记词而不是猜错误类型(错误类型已在内核里定型,
    // 前端再判一次就会与内核漂)。
    const msg = (e as Error).message || "";
    if (msg.includes("--accept-drift")) {
      guard(
        "这个 tag 现在指向了不同的 commit(见上面那条错)。确认要换成作者现在这一版吗?" +
          "\n\n" +
          msg,
        true,
        () => {
          void doPluginInstallNow(spec, true);
        },
      );
    }
  } finally {
    installBusy.value = false;
  }
}

async function doPluginInstallNow(
  spec: string,
  acceptDrift?: boolean,
): Promise<void> {
  try {
    const r = await api.pluginInstall(spec, acceptDrift, installPrebuilt.value);
    installOk.value =
      `已安装 ${r.id} → ${r.dir}` + (r.hint ? "(" + r.hint + ")" : "");
    // 补依赖是**供应链面被扩宽**的事实,不能混在一句成功回执里(用户扫一眼就过)。
    installWarn.value = r.tidied
      ? "注意:仓库的 go.mod 不完整,构建前补跑过 go mod tidy —— 这个插件引入了仓库原本没声明的模块依赖。"
      : "";
    // 预编译装的产物是**下载来的**,作者声明来源 + 不验签名 —— 装完再换会被下一次
    // 加载挡住,但那是唯一的缓解,必须说出来。
    if (r.prebuilt)
      installWarn.value =
        "产物来自作者发布的预编译产物(" +
        r.prebuilt +
        "):未执行任何构建命令,但**装的那一刻没有独立校验**(下载源由作者声明,gah 不验签名)。" +
        installWarn.value;
    // 原先这里的 else 分支写成了 `installSpec.value = ''`,于是**只有补过依赖时**才清输入框
    // —— 正常路径(没 tidy)装完输入框还留着,看着像没装成。
    installSpec.value = "";
    await refreshInstallList();
  } catch (e) {
    installErr.value = "安装失败:" + (e as Error).message;
  }
}

// —— 检查更新(批一 §1.5:check-then-ask,**不是自动更新**)——
// 网络只在点这个按钮时发生一次(服务端只发 git ls-remote,不 clone)。
// 结果里 `tag_changed` 必须显眼:它对应「同名 tag 指向了别的代码」。
const updChecking = ref(false);
const updChecks = ref<UpdateCheck[]>([]);
const updErr = ref("");

async function doCheckPluginUpdates(): Promise<void> {
  if (updChecking.value) return;
  updChecking.value = true;
  updErr.value = "";
  try {
    const r = await api.pluginUpdateCheck();
    // 同上:进模板的数组在边界收敛(checks 非数组会让 `updChecks.length` 在渲染期抛错)。
    updChecks.value = Array.isArray(r?.checks) ? r.checks : [];
    if (r?.notice) installWarn.value = r.notice;
  } catch (e) {
    updErr.value = "检查失败:" + (e as Error).message;
  } finally {
    updChecking.value = false;
  }
}

// 官方件与「你安装的」分组(批四 P3)。
//
// 为什么分组而不是一个混合列表加一列「来源」:这两个集合的**处置完全不同** ——
// 官方件由 embed 每次启动比对嵌入清单,用户碰它没有意义;而「你安装的」才需要
// 启停、信任、卸载。混在一张表里,用户每次都要先读一列才知道哪些按钮对自己有意义。
const bundledPlugins = computed(() =>
  installRows.value.filter((r) => r.origin === "official"),
);
const userPlugins = computed(() =>
  installRows.value.filter((r) => r.origin !== "official"),
);

// 停用的 UI 插件必须仍然看得见(它在 /api/ui-plugins 里已被过滤掉了)。
// 「我停用的那个」从清单里消失,等于用户以为自己停错了。
const uiPluginDisabled = ref<string[]>([]);
const uiOk = ref("");
const uiErr = ref("");
// uiPluginTrustEnforced:完整性闸是否强制。老路「手工拷一个 UI 插件目录进
// ui-plugins/ 就能用」在批四被断掉 —— 面板必须**主动说明**怎么放行,
// 否则用户只看到「我放的插件不见了」,以为产品坏了(静默拒绝是最坏的处置)。
const uiPluginTrustEnforced = ref(false);

// —— UI 插件:安装 / 卸载 / 登记 / 启停(批九补齐 GUI 入口)——
//
// 为什么之前面板上只有「重新扫描」:UI 插件的安装与放行只在命令行有,而「重新扫描」
// 对一个**从没装过**的用户毫无用处(扫一个空目录)。入口缺失不是功能缺失,更糟:
// 用户看到「UI 插件」这一段却找不到任何能开始的动作。
const uiSpec = ref("");
const uiBusy = ref(false);
const uiPluginList = ref<UIPluginView[]>([]);

async function doUIInstall(): Promise<void> {
  const spec = uiSpec.value.trim();
  if (!spec || uiBusy.value) return;
  guard(UIInstallConfirmText(spec), true, async () => {
    uiBusy.value = true;
    uiErr.value = "";
    try {
      const r = await api.uiPluginInstall(spec);
      uiOk.value =
        `已安装 UI 插件 ${r.id}(覆盖 ${r.slots} 个槽位)` +
        (r.hint ? " · " + r.hint : "");
      uiSpec.value = "";
      await refreshUIState();
    } catch (e) {
      uiErr.value = "UI 插件安装失败:" + (e as Error).message;
    } finally {
      uiBusy.value = false;
    }
  });
}

function doUIUninstall(id: string): void {
  guard(
    `卸载 UI 插件 ${id}?\n\n· 删掉它的目录\n· 撤销完整性闸里的登记\n\n随时可重装。`,
    true,
    async () => {
      try {
        await api.uiPluginUninstall(id);
        uiOk.value = "已卸载 " + id;
        await refreshUIState();
      } catch (e) {
        uiErr.value = "卸载失败:" + (e as Error).message;
      }
    },
  );
}

// doUITrust 放行一个手工放置的 UI 插件(只登记**当前那一份**的摘要)。
function doUITrust(id: string): void {
  guard(
    `把 UI 插件 ${id} 登记进完整性闸?\n\n它会经动态 import() 进入主页面 —— 与宿主同源同权限,` +
      `能读页面全部会话内容、可用 cookie 调全部 API。只登记你**看过**的这一份。`,
    true,
    async () => {
      try {
        await api.uiPluginTrust(id);
        uiOk.value = "已登记 " + id + "(重载页面后生效)";
        await refreshUIState();
      } catch (e) {
        uiErr.value = "登记失败:" + (e as Error).message;
      }
    },
  );
}

// UIInstallConfirmText UI 插件安装的确认文案(自己生成:服务端的 Prompt 形状是进程型插件的,
// 文案里提到「构建命令」与「白名单二进制」,对 UI 插件是错的)。
function UIInstallConfirmText(spec: string): string {
  return (
    `安装 UI 插件?\n来源:${spec}\n` +
    `它会经**动态 import()** 进入主页面 —— 与宿主**同源同权限**:` +
    `可读取页面上的全部会话内容,可用 cookie 调全部 API(含 /api/input,` +
    `即向模型投喂 prompt ⇒ 经工具执行等同本地代码执行)。\n` +
    `构建脚本会在本机跑 npm;装完的产物哈希会进完整性闸,装完再换会被下一次加载挡住。`
  );
}

async function refreshUIState(): Promise<void> {
  try {
    const st = await api.uiPluginState();
    uiPluginList.value = Array.isArray(st.installed) ? st.installed : [];
    uiPluginDisabled.value = Array.isArray(st.disabled) ? st.disabled : [];
    uiPluginTrustEnforced.value = st.enforced === true;
  } catch {
    // 状态读不出来时**不**把已装清单清空:那会让「我装了 5 个」在一闪之间变成「一个都没有」。
  }
}

async function doUIToggle(id: string, enable: boolean): Promise<void> {
  const verb = enable ? "启用" : "停用";
  guard(
    enable
      ? `启用 UI 插件 ${id}?它会经动态 import() 进入主页面 —— 与宿主同源同权限,能调全部 API`
      : `停用 UI 插件 ${id}?\n\n· 不再下发(主页面不再加载它)\n· **文件与完整性闸条目都留着**\n\n重新启用不需要重新登记。`,
    true,
    async () => {
      try {
        await api.uiPluginToggle(id, enable);
        installOk.value = `已${verb} UI 插件 ${id}`;
        await refreshInstallList();
      } catch (e) {
        installErr.value = `${verb}失败:` + (e as Error).message;
      }
    },
  );
}

// pluginStateLabel 三态标签(批二)。
//
// 「工具不出现」这一个症状对应三种情况,而它们的处置完全不同:
// 被完整性闸拒 ⇒ 要登记;被停用 ⇒ 要启用;二进制没了 ⇒ 要重装。合成一句「没加载」
// 会让用户在三者之间猜。**停用排在最前**:那是他自己刚刚做的动作,最容易被忘。
function pluginStateLabel(r: InstallView): string {
  if (r.disabled) return "已停用(文件在,没跑)";
  if (!r.loadable) return "二进制缺失";
  return r.trusted ? "已启用 · 白名单已登记" : "未登记(会被完整性闸拒)";
}

// sourceLine 一个已装插件的来源三件(仓库 · ref · commit 短显示)。
function sourceLine(r: InstallView): string {
  if (!r.source_repo) return "(无来源记录:手工放置或早期安装)";
  const ref = r.source_ref ? "@" + r.source_ref : "";
  const sha = r.source_commit ? " · " + r.source_commit : "";
  const moved = r.drifted ? " · 会移动" : "";
  const pre = r.prebuilt ? " · 下载的预编译产物" : "";
  return (
    r.source_repo + ref + "(" + (r.source_kind || "") + sha + moved + pre + ")"
  );
}

// doUninstallPlugin 卸载(含白名单与来源账条目撤销),二次确认。
//
// 文案必须与「停用」**明确区分**:停用留文件,卸载删文件。用户以为点的是后者而实际
// 是前者,重启后插件又回来了 —— 或者反过来,以为还能停用结果被删了。
function doUninstallPlugin(id: string): void {
  guard(
    `卸载并删除插件 ${id}?\n\n· 删掉它的目录文件\n· 撤销哈希白名单与来源账条目\n· 已经跑的进程会被先停掉\n\n` +
      "只想临时关掉它(文件留着、以后不用重新登记),用「停用」而不是「卸载」。",
    true,
    async () => {
      try {
        await api.pluginUninstall(id);
        installOk.value =
          "已卸载 " + id + "(进程已停,白名单与来源账条目一并撤销)";
        await refreshInstallList();
      } catch (e) {
        installErr.value = "卸载失败:" + (e as Error).message;
      }
    },
  );
}

// doDisablePlugin 停用(文件留着,只是不跑)。二次确认。
function doDisablePlugin(name: string): void {
  guard(
    `停用插件 ${name}?\n\n· 停掉它的进程并撤销它的工具\n· **文件、白名单条目、来源账条目都留着**\n\n` +
      "重新启用时不需要重新登记哈希。彻底删掉用「卸载并删除」。",
    true,
    async () => {
      try {
        await api.pluginDisable(name);
        installOk.value =
          "已停用 " + name + "(文件与登记都留着,重新启用不需要重新登记)";
        await refreshInstallList();
      } catch (e) {
        installErr.value = "停用失败:" + (e as Error).message;
      }
    },
  );
}

// doEnablePlugin 启用。启用前会**重验漂移**(tag 被改过 ⇒ 服务端会拒绝并给出路)。
function doEnablePlugin(name: string): void {
  guard(
    `启用插件 ${name}?启用前会检查它当初记录的 tag 是否还指向同一个 commit(被改过会拒绝)`,
    true,
    async () => {
      try {
        await api.pluginEnable(name);
        installOk.value = "已启用 " + name;
        await refreshInstallList();
      } catch (e) {
        // 漂移拒绝要原样透出:那里面有一条用户可执行的路子(重新安装),吞掉就等于没给。
        installErr.value = "启用失败:" + (e as Error).message;
      }
    },
  );
}

// doTrustPlugin 登记进哈希白名单(被完整性闸拒了、但你确认来源可信时用)。
function doTrustPlugin(name: string): void {
  guard(
    `把 ${name} 登记进哈希白名单?登记的是**当前盘上那一份**的 sha256`,
    true,
    async () => {
      try {
        await api.pluginTrust(name);
        installOk.value = "已登记 " + name;
        await refreshInstallList();
      } catch (e) {
        installErr.value = "登记失败:" + (e as Error).message;
      }
    },
  );
}

// doUntrustPlugin 撤销登记(之后该插件会被拒绝加载)。
function doUntrustPlugin(name: string): void {
  guard(`从白名单移除 ${name}?移除后它会被拒绝加载`, true, async () => {
    try {
      await api.pluginUntrust(name);
      installOk.value = "已移除 " + name;
      await refreshInstallList();
    } catch (e) {
      installErr.value = "撤销失败:" + (e as Error).message;
    }
  });
}

// pickInstallDir 桌面端用原生选择器挑本地插件源码目录;浏览器形态返回空(手打路径)。
async function pickInstallDir(): Promise<void> {
  if (!isDesktop) return;
  const dir = await pickDirectory("选择本地插件源码目录(含 plugin.yaml)");
  if (dir) installSpec.value = dir;
}

// pickUIDir 桌面端选本地 UI 插件源码目录(与外部插件那条同款,但**各自一个状态变量** ——
// 共用一个会让「选了 A 的目录,装 B 的插件」这种错配发生而不报错)。
async function pickUIDir(): Promise<void> {
  if (!isDesktop) return;
  const dir = await pickDirectory("选择本地 UI 插件源码目录(含 manifest.json)");
  if (dir) uiSpec.value = dir;
}

// uiScanBusy UI 插件重扫进行中(按钮置灰;防连点)。
const uiScanBusy = ref(false);

// rescanUIPlugins 重扫 UI 插件并重装槽位。
//
// 为什么必须有它:插件清单是**页面启动时扫一次**的(`main.ts` 的 loadUIPlugins),
// 装完新插件只能靠刷新页面;而用户在设置面板里刚装完、看着「没变化」,
// 会以为装失败了 —— 实际只是没重扫。补这个入口把「刷新页面」变成一个按钮。
// 注意:重扫**只重装槽位覆盖**,不清空已注册的默认实现(registry 的 registerSlot
// 自带优先级比较,同优先级后注册者胜 ⇒ 重扫是幂等叠加,不是"再装一遍同样的东西")。
async function rescanUIPlugins(): Promise<void> {
  if (uiScanBusy.value) return;
  uiScanBusy.value = true;
  try {
    await loadUIPlugins();
  } finally {
    uiScanBusy.value = false;
  }
}

// doCheckUpdate 桌面版检查更新:壳侧走同一条 checkForUpdates(与托盘菜单同一实现)。
// **只查不装**:有更新时壳返回 available,由用户点「立即升级」并确认后才下载安装。
async function doCheckUpdate() {
  if (!isDesktop) {
    updOk.value = false;
    updMsg.value = "当前环境不支持检查更新(需桌面版)";
    return;
  }
  updBusy.value = true;
  updMsg.value = "正在检查…";
  updOk.value = false;
  updAvailable.value = "";
  try {
    const r = await checkUpdate();
    updAvailable.value =
      r?.status === "available" ? String(r?.version ?? "新版本") : "";
    updOk.value = r?.status === "upToDate" || r?.status === "installed";
    updMsg.value = String(r?.message ?? "检查完成");
  } catch (e) {
    updOk.value = false;
    updAvailable.value = "";
    updMsg.value =
      "检查更新失败:" + (e instanceof Error ? e.message : String(e));
  } finally {
    updBusy.value = false;
  }
}

// doInstallUpdate 确认升级:二次确认(有副作用的操作必须问)→ 下载安装 → 壳自动重启。
// 二次确认走既有的全局确认条 guard(与增删改前置同一处):升级会换掉整个应用本体,
// 属于必须由人点头的动作。取消只是不 run —— updAvailable 保留,按钮还在,可随时再升。
function doInstallUpdate(): void {
  const ver = updAvailable.value;
  if (!ver) return;
  guard(`升级到 ${ver}(会替换应用本体并重启)`, false, () => {
    void doInstallNow();
  });
}

// doInstallNow 确认之后真正下载安装的那一段(与确认弹层解耦)。
async function doInstallNow(): Promise<void> {
  updBusy.value = true;
  updMsg.value = "正在下载安装…";
  try {
    const r = await installUpdate();
    updOk.value = r?.status === "installed";
    updAvailable.value = r?.status === "installed" ? "" : updAvailable.value;
    updMsg.value = String(r?.message ?? "安装完成");
  } catch (e) {
    updOk.value = false;
    updMsg.value = "安装失败:" + (e instanceof Error ? e.message : String(e));
  } finally {
    updBusy.value = false;
  }
}
// 开机自启的实际状态(「关于 gah」展示)。托盘勾选态可能与系统实际状态不同步,故直接问系统。
// 2026-09-17 真机反馈:「勾选开机自启后,关于 gah 的内容未有变化」—— 因为那段里根本没有自启信息。
const autoState = ref<"" | "on" | "off">("");
// lastUpdSeq 面板已经展示过的检查轮次:轮询只贴更新的轮次,不把面板自己刚给出的结论冲掉。
const lastUpdSeq = ref(0);
let stateTimer: ReturnType<typeof setInterval> | undefined;
// 两处短时提示的句柄:组件若被销毁(v-if 重建),已排队的清理定时器不该再去写已卸载的 ref。
let mcpMsgTimer = 0;
let infoTimer = 0;

// applyUpdateSnapshot 把壳侧检查更新快照贴到面板上。进行中始终跟随(busy 由壳侧真源定);
// 结束时只认比本地见过的更新的那一轮 —— 否则轮询会把面板自己刚写的错误提示覆盖回去。
function applyUpdateSnapshot(u: UpdateSnapshot): void {
  if (u.busy) {
    updBusy.value = true;
    updOk.value = false;
    updMsg.value = "正在检查…";
    return;
  }
  if (u.seq <= lastUpdSeq.value) return;
  lastUpdSeq.value = u.seq;
  updBusy.value = false;
  updAvailable.value =
    u.status === "available" ? String(u.version ?? "新版本") : "";
  updOk.value = u.status === "upToDate" || u.status === "installed";
  updMsg.value = u.message || "检查完成";
}

// refreshDesktopState 面板打开期间轮询壳侧状态(开机自启 + 检查更新)。
// 为何要轮询:托盘与设置面板是两个视图。真机反馈「设置界面开着时从托盘勾开机自启/点检查
// 更新,面板要重新打开才同步」—— 现在托盘动作改的是壳侧状态,面板 1.5 秒内跟上。
// 走已验证的同步命令通道,不用事件通道(插件事件通道在真机上静默失败过)。
async function refreshDesktopState(): Promise<void> {
  autoState.value = await autostartState();
  const u = await updateState();
  if (u) applyUpdateSnapshot(u);
}

// v2 扩展点:设置面板区段(每次渲染求值,保持实时)
const panelSections = settingSections();
const err = ref("");
const info = ref(""); // 操作成功/结果提示(短时)
const busy = ref(false);

// —— 数据 ——
const models = ref<ProviderModelGroup[]>([]);
const providers = ref<ProviderInfo[]>([]);
const plugins = ref<PluginInfo[]>([]);
const histN = ref(0); // 0 全部 / N 最近 / -1 禁止

// —— 操作表单(Provider 新增)与 W3 首启引导 ——
const showAdd = ref(false);

// providerNames 已配置的 provider 名集合(判断某个预设是不是「已添加」)。
//
// 为什么需要它:**同名保存不是新增而是更新**(后端 providerfile.Add 对同名走 mergeFields
// 并激活)—— 用户在已有 DeepSeek 的情况再点一次 DeepSeek 预设,表单会静默把旧记录的
// api_key 换掉。不说清就会以为「多了���个」,而实际是「少了原来那个配置」。
const providerNames = computed(() => new Set(providers.value.map((p) => p.Name)));
// pfConflicts 表单里当前名称命中已存在的 provider ⇒ 保存是更新它(显示为空即新增)。
const pfConflicts = computed(
  () => providers.value.find((p) => p.Name === pf.value.name.trim())?.Name ?? "",
);
const pf = ref({
  name: "",
  base_url: "",
  api_key: "",
  model: "",
  /**
   * 自定义请求头,**一行一个** `键: 值`(textarea 形态,因为它天生是多行且用户量少)。
   * 选这个形态而不是两个输入框:头值里常带冒号/等号/占位符,单行输入框容易看不出问题在哪;
   * 而解析失败时能指着行号说「第 2 行格式不对」。
   */
  headers: "",
});
const presets = PROVIDER_PRESETS;

// —— 分段导航(左栏;窄窗口退化为顶部芯片条) ——
// 面板此前是一条长滚动列:43 个插件条目把「关于 gah / 检查更新」顶到必须长滚的位置。
// 现在段与导航共用一份键(段上写 data-sec),跳转与高亮都从 DOM 反查 —— 少一份可能失配的映射表。
const bodyEl = ref<HTMLElement | null>(null);
const activeSec = ref("model");
// navItems 导航项(顺序 = 段在 DOM 里的顺序;条件渲染的段跟着一起隐藏)
const navItems = computed(() => {
  const items: { key: string; label: string }[] = [
    { key: "model", label: "模型" },
    { key: "reason", label: "推理" },
  ];
  if (instrReady.value) items.push({ key: "instr", label: "指令" });
  if (memReady.value) items.push({ key: "memory", label: "记忆" });
  if (roleReady.value) items.push({ key: "role", label: "角色" });
  items.push(
    { key: "history", label: "会话历史" },
    { key: "provider", label: "Provider" },
    { key: "backup", label: "数据备份" },
  );
  if (schedReady.value) items.push({ key: "schedule", label: "计划" });
  items.push(
    { key: "mcp", label: "MCP server" },
    { key: "plugin", label: "插件" },
  );
  if (isDesktop) items.push({ key: "about", label: "关于 gah" });
  for (const s of panelSections)
    items.push({ key: "ext-" + s.key, label: s.title ?? s.key });
  return items;
});
function secEls(): HTMLElement[] {
  return Array.from(
    bodyEl.value?.querySelectorAll<HTMLElement>("section[data-sec]") ?? [],
  );
}
// jumpTo 跳到某段。打开面板时的定位要即时(否则与抽屉入场动画叠在一起),用户点导航要平滑。
//
// navTarget = 平滑滚动进行中的目标段:期间**只**高亮它。不锁的话滚动反查会把高亮中途
// 改到途经的段上(点「角色」会先亮一下「指令」),动画被拖慢/中断时停下来高亮还停在错的段
// —— CI 上实测踩到(macOS runner 400ms 内没滚完,断言拿到「指令」)。
let navTarget = "";
let navSettle = 0;
function unlockNav(): void {
  navTarget = "";
  if (navSettle) {
    clearTimeout(navSettle);
    navSettle = 0;
  }
}
function jumpTo(key: string, smooth = true): void {
  const el = bodyEl.value?.querySelector<HTMLElement>(
    `section[data-sec="${key}"]`,
  );
  if (!el) return; // 该段在当前装配下不渲染(如未装配 host-schedule):静默跳过
  activeSec.value = key;
  if (!smooth) {
    unlockNav();
    el.scrollIntoView({ block: "start", behavior: "auto" });
    return;
  }
  navTarget = key;
  if (navSettle) clearTimeout(navSettle);
  // 兜底:目标段始终到不了容器上沿(内容不够高的末段)或滚动事件不来时,700ms 后交还反查 ——
  // 交还时补跑一次反查,否则锁在错的位置(高亮停在目标段、视图却在别处)。
  navSettle = window.setTimeout(() => {
    unlockNav();
    onBodyScroll();
  }, 700);
  el.scrollIntoView({ block: "start", behavior: "smooth" });
}
// onBodyScroll 滚动时反查「当前段」= 视口顶部之上最后一段。用 rAF 合并同一帧内的多次滚动事件,
// 每帧只读一次几何(读与写不交叉,不触发强制同步布局)。
let navRaf = 0;
function onBodyScroll(): void {
  if (navRaf) return;
  navRaf = requestAnimationFrame(() => {
    navRaf = 0;
    const c = bodyEl.value;
    if (!c) return;
    const top = c.getBoundingClientRect().top;
    const secs = secEls();
    if (navTarget) {
      // 平滑滚动进行中:高亮只认目标段(途经段不算);到位(±2px)即解锁,交还下面的反查。
      const t = secs.find((el) => el.dataset.sec === navTarget);
      if (t && Math.abs(t.getBoundingClientRect().top - top) > 2) {
        activeSec.value = navTarget;
        return;
      }
      unlockNav();
    }
    // 已滚到底:末段顶不到容器上沿(内容不够高),但用户点它时它就是「当前段」——
    // 不特判的话高亮会因上面的阈值判据退回倒数第二段。
    if (c.scrollTop + c.clientHeight >= c.scrollHeight - 2) {
      const last = secs[secs.length - 1]?.dataset.sec;
      if (last) activeSec.value = last;
      return;
    }
    let cur = "";
    for (const el of secs) {
      if (el.getBoundingClientRect().top - top <= 32)
        cur = el.dataset.sec ?? "";
      else break; // 段按 DOM 顺序排列,首个在阈值之下即收工
    }
    if (cur) activeSec.value = cur;
  });
}
const keyInput = ref<HTMLInputElement | null>(null);
const applied = ref<ProviderPreset | null>(null); // 最近选中的预设(用于「本地免 Key」提示)
const lastSaved = ref(""); // 刚保存的 provider(失败后「重新自检」用)
const probe = ref<{ ok: boolean; text: string; raw?: string } | null>(null);
// onboardHint 空状态引导的一句话:选中预设后给该预设的说明
const onboardHint = computed(() =>
  applied.value
    ? applied.value.note
    : "不知道选哪个就用 DeepSeek;不想花钱可选 Ollama 在本机跑模型。",
);

const THINK = ["off", "low", "medium", "high"] as const;
const THINK_LABEL: Record<string, string> = {
  off: "关闭",
  low: "低",
  medium: "中",
  high: "高",
};
const SB = [
  { v: "read-only", label: "只读" },
  { v: "workspace-write", label: "工作区" },
  { v: "full-access", label: "完全" },
] as const;
const AP = [
  { v: "open", label: "开放" },
  { v: "smart", label: "智能" },
  { v: "strict", label: "严格" },
] as const;
const SB_ZH: Record<string, string> = {
  "read-only": "只读",
  "workspace-write": "工作区",
  "full-access": "完全",
};
const AP_ZH: Record<string, string> = {
  open: "开放",
  smart: "智能",
  strict: "严格",
};
// 角色可声明的收紧档(第九十二批):只列**比缺省更严**的那几个。
// 不摆 open/full-access:那是放宽,后端直接 400 —— 界面摆一个点了必失败的按钮就是骗人。
const ROLE_AP = [
  { v: "smart", label: "智能（危险命令逐条确认）" },
  { v: "strict", label: "严格（危险命令直接拒）" },
] as const;
const ROLE_SB = [
  { v: "read-only", label: "只读（只能读，不能改）" },
  { v: "workspace-write", label: "工作区可写" },
] as const;

async function load(): Promise<void> {
  err.value = "";
  try {
    // 模型聚合/插件/provider 任一失败降级:非核心(如未装配 MultiProviderService → 501)
    const [m, pl, pr] = await Promise.allSettled([
      api.models(),
      api.plugins(),
      api.providers(),
    ]);
    await loadBackups(); // M18 备份列表(未装配降级静默)
    await loadMemory(); // 记忆段(未装配 ctx.memory → memReady=false,整段隐藏)
    await loadRoles(); // 角色段(未装配 ctx.roles → roleReady=false,整段隐藏)
    if (m.status === "fulfilled") models.value = m.value.providers ?? [];
    if (pl.status === "fulfilled") plugins.value = pl.value ?? [];
    if (pr.status === "fulfilled") providers.value = pr.value ?? [];
    if (!probe.value) refreshProbeFromActive(); // 活跃 provider 拉不到模型时直接给出原因
  } catch (e) {
    err.value = (e as Error).message;
  }
}
function activeProvider(): ProviderInfo | undefined {
  return providers.value.find((p) => p.Active);
}
function guard(title: string, danger: boolean, run: () => void): void {
  if (!ask) {
    run();
    return;
  }
  ask({ title, danger, run });
}

// —— 草稿保护 ——
// 面板里的四处可编辑内容都是「写进磁盘就长期生效」的东西(角色工作规则 / 技能 SKILL.md /
// 全局指令 / MCP server 配置),而**切换目标**(换角色、换技能、重拉配置)会重新拉服务端
// 内容覆盖草稿 —— 用户的修改就这么没了,连一声响都没有。所以:① 每处都记一份「服务端上一版」
// 用于算脏;② 脏的时候切换目标前问一声;③ 面板里常驻「未保存」标记(知道自己脏,才有得选)。
// 反面:给一切都加确认弹层 = 噪音。所以只拦**真会丢内容**的入口,关面板/切分区不拦
// (组件实例常驻、草稿留在内存里,关掉再打开还在)。真正会丢的是**页面卸载**(刷新/关标签),
// 那条走浏览器的 beforeunload,见 onBeforeUnload。
type DraftKind = "role" | "skill" | "mcp";
// draftLabels 未保存草稿的标签 —— **只列这次真会被丢弃的**:
//   role = 角色工作规则(切角色/换技能要重拉详情)、skill = 技能 SKILL.md、mcp = MCP server 配置。
//   全局指令**不在此列**:没有任何一条「换目标」路径会丢它(只由它自己的保存/放弃清掉),
//   列上去等于说「继续将丢弃全局指令」而实际不会 —— 白吓一跳,还让人以为已经放弃了。
function draftLabels(kinds: DraftKind[]): string[] {
  const out: string[] = [];
  if (kinds.includes("role") && roleDefDirty.value)
    out.push("角色「" + selRole.value + "」的定义(名称/权限档/模型等)");
  if (kinds.includes("role") && agentsDirty.value)
    out.push("角色「" + selRole.value + "」的工作规则");
  if (kinds.includes("skill") && skDirty.value)
    out.push("技能「" + (skEdit.value?.name ?? "") + "」的 SKILL.md");
  if (kinds.includes("mcp") && mcpDirty.value) out.push("MCP server 配置");
  return out;
}
// withDrafts 有未保存草稿时先确认(丢弃是用户的决定,不是面板替他做的)。
function withDrafts(
  run: () => void,
  kinds: DraftKind[] = ["role", "skill"],
): void {
  const d = draftLabels(kinds);
  if (!d.length) {
    run();
    return;
  }
  guard("有未保存的修改:" + d.join("、") + "。继续将丢弃这些修改?", true, run);
}

// —— 模型/思考/沙箱 ——
// modelOption 的形状在 modelsel.ts(ModelOption):tags/warn/usable 全部来自**后端算好的**
// verdict(Go 侧 sdk.AssessModel 是单一事实源);前端只渲染与排序,不自己判「能不能当 agent 用」。
const modelOptions = ref<ModelOption[]>([]);
function buildModelOptions(): void {
  const opts: ModelOption[] = [];
  for (const g of models.value) {
    for (const md of g.Models) {
      const v = md.Verdict;
      const tags = v?.Tags ?? [];
      opts.push({
        label:
          g.Name +
          " · " +
          md.ID +
          (tags.length ? " · " + tags.join(" · ") : ""),
        value: modelOptionValue(g.Name, md.ID),
        // 没有 verdict(旧后端/枚举失败)时不当成不可用 —— 那会把列表清空
        usable: v ? v.Usable : true,
        free: v ? v.Free : false,
        autoRouter: v?.AutoRouter ?? false,
        contextWindow: v?.ContextWindow ?? 0,
        tags,
        warn: v?.Warn ?? "",
      });
    }
  }
  // 排序:能用当 agent 用 > 免费 > 上下文大。权重口径与 TUI 选择器一致
  // (host-internal-commands 的 modelRankOf);顺序只影响观感,不影响正确性 ——
  // 真正的过滤条件用的是后端给的 usable,不在前端重算。
  opts.sort((a, b) => modelRank(a) - modelRank(b));
  // 当前模型可能不在枚举里(手填/provider 未列全):补一条「(当前)」,保证高亮总有落点。
  // 真源只有一个 —— state.model(运行时真正在用的),不是 provider 配置里的 Model 默认值:
  // 旧实现两者混用,不一致时高亮永远匹配不上(见 modelsel.ts 顶部注释)。
  modelOptions.value = withCurrentModel(
    opts,
    props.state.model,
    activeProvider()?.Name ?? "",
  );
}
const modelVal = ref("");
// 模型选择:输入筛选(modelFilter 实时过滤选项,provider·模型名均可匹配;命中即点选)
const modelFilter = ref("");
// 「只看能当 agent 用的」**默认开**(2026-10-06)。开着的理由:OpenRouter 这类聚合端点
// 一次给 400+ 个模型,其中不少既没工具调用能力、上下文也偏小,平铺出来等于没法选。
// 不藏被过滤掉的那部分 —— 开关旁边写明还剩多少条,关掉就能看到全部(并在标签上标明原因)。
const onlyUsableModels = ref(true);
const filteredModels = computed(() => {
  const f = modelFilter.value.trim().toLowerCase();
  let list = modelOptions.value;
  if (onlyUsableModels.value) list = list.filter((o) => o.usable !== false);
  if (!f) return list;
  return list.filter(
    (o) =>
      o.label.toLowerCase().includes(f) ||
      o.value.toLowerCase().includes(f) ||
      (o.tags ?? []).join(" ").toLowerCase().includes(f),
  );
});
const hiddenModelCount = computed(
  () => modelOptions.value.length - filteredModels.value.length,
);
// noPriceInfoProviders 哪些端点的模型列表**完全不带价格信息**。
//
// 为什么要有这一行:免费徽标是靠端点 /models 返回的 pricing 算出来的(没有硬编码任何
// 「某家免费」—— 那些政策会过期,百炼的额度有效期就已经从 365 天改成 90 天)。代价是:
// 端点不给价格时,面板上就不会出现「免费」徽标,而**用户无从区分**「这些真不免费」与
// 「这个网关没告诉我们价格」。直说比让用户怀疑徽标坏了强。
const noPriceInfoProviders = computed(() =>
  models.value
    .filter((g) => (g.Models ?? []).length > 0 && !g.Models.some((m) => m.PriceKnown))
    .map((g) => g.Name),
);
// curModelLabel 当前生效模型的可读标签(输入框占位 + 「当前生效」行)。
// 真源是 state.model(运行时真正在用的),不是筛选框内容 —— 真机上「当前模型」标签后面
// 就是个空筛选框,用户会读成「已选了模型但当前模型没显示」,故把当前值显式摆出来。
const curModelLabel = computed(() => {
  const hit = modelOptions.value.find((o) => o.value === modelVal.value);
  const m = (props.state.model ?? "").trim();
  // 角色指定模型时只报模型名:provider 前缀是**会话通用适配器**的域短名,
  // 角色模型可能走另一个前缀路由(claude-* 等),拼上去就是编(与 TUI 同一条纪律)。
  if (props.state.model_from === "role") return m || hit?.value || "";
  if (hit) return hit.label;
  if (!m) return "";
  const p = activeProvider();
  return p?.Name ? p.Name + " · " + m : m;
});
// 列表点选:记录选中并应用(与原生 select @change 同语义;选中后清空筛选恢复可视)
function pickModel(o: { label: string; value: string }): void {
  modelVal.value = o.value;
  modelFilter.value = "";
  void applyModel();
}
// modelLabelOf:模型 id → 人读的标签(带 provider 前缀)。全局提示里要说清「改成了什么」,
// 而 state 只回显**生效值**(独立会话看不到全局值),所以这里从选项表里查标签。
function modelLabelOf(mid: string): string {
  const hit = modelOptions.value.find((m) => m.value.endsWith("|" + mid) || m.value === mid)
  return hit?.label ?? mid
}

// 下面两个写盘函数共用一条纪律:改全局默认**之前**先记下本会话是否独立。
// 本会话有独立设置时,改全局不会改变它 —— 不据实说明就是「点了没生效」的静默矛盾
// (与第九十二批角色收紧那条同一种毛病:说法与实际生效的档位对不上)。

async function applyModel(): Promise<void> {
  const v = modelVal.value;
  if (!v) return;
  const [pname, mid] = v.split("|");
  busy.value = true;
  // 改之前快照:本会话是否独立、它当时在用什么(改完之后这些都不会因全局变化而变)
  const wasIndependent = hasSessionOverride(props.state);
  const wasUsing = props.state.model;
  try {
    if (
      pname &&
      !activeProvider()?.Name?.includes(pname) &&
      providers.value.some((p) => p.Name === pname)
    ) {
      await api.providerUse(pname); // 先切所属 provider(活跃态与端点)
    }
    await api.control({ model: mid, session: "" }); // session:"" = 写全局默认
    emit("changed");
    // 只说「已更新」不说更新成了什么 —— 独立会话的用户在界面上**看不到**全局新值
    // (本会话仍在用自己的),不给新值就成了「不知道改到哪去了」。
    info.value = wasIndependent
      ? `全局默认已改为「${modelLabelOf(mid)}」。本会话有独立设置,仍在用「${wasUsing}」—— 想去掉这层覆盖,点右上角的「当前会话设置」。`
      : "";
  } catch (e) {
    err.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
// onSyncToggle 联动开关勾选(R10 ②-2):模板里不写类型断言,取 checked 的脏活留在脚本里。
function onSyncToggle(e: Event): void {
  applyCtl({ sandbox_sync: (e.target as HTMLInputElement).checked });
}
async function applyCtl(body: {
  thinking?: string;
  sandbox?: string;
  approval?: string;
  sandbox_sync?: boolean;
}): Promise<void> {
  // 联动开关是**全局的**(它决定全局默认怎么派生),不带 session;档位三类带 session:""
  // 表示写全局默认(第一百三十四批)。改之前先记下本会话是否独立,改完据实提示。
  const globalOnly = Object.keys(body).length === 1 && "sandbox_sync" in body;
  const wasIndependent = hasSessionOverride(props.state);
  try {
    await api.control(globalOnly ? body : { ...body, session: "" });
    emit("changed");
    if (!globalOnly && wasIndependent) {
      const what = body.thinking
        ? `思考档`
        : body.sandbox
          ? `沙箱档`
          : `审批档`
      info.value = `全局默认的${what}已更新。本会话有独立设置,不受影响(点右上角的「当前会话设置」可去掉那层覆盖)。`;
    } else if (!globalOnly) {
      info.value = "";
    }
  } catch (e) {
    err.value = (e as Error).message;
  }
}

// —— 全局指令(第八十一批 · 形态 A) ——
// $GAH_HOME/AGENTS.md 是"对所有角色生效"的基线层(角色未声明 exclude_global 时都注入),
// 此前只能在文件系统上手改 —— 面板有角色级规则编辑器,却没有这一份全局的。
// 纯文件读写,与 ctx.roles 是否装配无关(未装配角色服务时这一段照样能用)。
const instrReady = ref(false);
const instrPath = ref("");
const instrMax = ref(32768);
const instrExists = ref(false);
const instrOver = ref(false); // 文件本身超限(手改的大文件):能看能改,但保存会被拒
const instrDraft = ref("");
const instrSaved = ref(""); // 服务端上一版正文(草稿与它比才有"改没改过")
const instrEdit = ref(false);
const instrErr = ref("");
const instrMsg = ref("");
const instrWarn = ref(""); // 文件写了但本轮提示没跟着变(重载失败):成功样式会谎报"已生效'
const instrBytes = computed(() => byteLength(instrDraft.value));
// instrDirty 有未保存改动。**收起编辑区不清草稿**(留在内存里,再展开还在) —— 清掉草稿的
// 出口只有两个:「保存」与显式「放弃修改」,不留静默丢失路径。
const instrDirty = computed(() => instrDraft.value !== instrSaved.value);

// discardInstr 显式放弃草稿(回到磁盘上的内容;编辑区**留着** —— 用户可能想从磁盘版本接着改)。
function discardInstr(): void {
  guard("放弃尚未保存的全局指令修改?", true, () => {
    instrErr.value = "";
    instrDraft.value = instrSaved.value;
    void loadInstructions();
  });
}

// loadInstructions 拉全局指令(读失败 → 整段隐藏,不摆空壳;不覆盖正在编辑的草稿)
async function loadInstructions(): Promise<void> {
  try {
    const v = await api.instructions();
    if (!v || typeof v.text !== "string") {
      instrReady.value = false;
      return;
    }
    instrReady.value = true;
    instrPath.value = v.path;
    instrMax.value = v.max_bytes || 32768;
    instrExists.value = !!v.exists;
    instrOver.value = !!v.over;
    // 先按**上一版**基准算脏(顺序要紧:先写 instrSaved 会把"初始空草稿"自己算成脏,于是永远不回填)
    const dirty = instrDirty.value;
    instrSaved.value = v.text;
    if (!dirty) instrDraft.value = v.text;
  } catch {
    instrReady.value = false;
  }
}

// saveInstructions 保存全局指令:逐字进系统提示且影响所有角色 → 二次确认 + 上限前置校验。
// 服务端回 warning = 文件写了但重载没成(本轮提示仍是旧内容),照实说,不谎报"已生效"。
function saveInstructions(): void {
  const text = instrDraft.value;
  if (byteLength(text) > instrMax.value) {
    instrErr.value = `全局指令超上限:${byteLength(text)} > ${instrMax.value} 字节(会逐字进系统提示,请精简)`;
    return;
  }
  guard(
    "保存全局指令?(写入 " + instrPath.value + ",对所有角色生效)",
    false,
    () => {
      instrErr.value = "";
      instrMsg.value = "";
      instrWarn.value = "";
      void (async () => {
        try {
          const r = await api.instructionsSave(text);
          if (r.warning) {
            instrMsg.value = "";
            instrWarn.value =
              "文件已写入,但未生效:" + r.warning + "(面板显示的是磁盘上的内容)";
          } else {
            instrWarn.value = "";
            instrMsg.value = "全局指令已保存并生效";
          }
          instrEdit.value = false;
          await loadInstructions();
        } catch (e) {
          instrErr.value = (e as Error).message;
        }
      })();
    },
  );
}

// —— 跨会话记忆(记忆治理面板) ——
// 与 `/memory` 命令**同一份实现**(两边都只调 ctx.memory):此前只能在命令里管,
// 面板没有对应段落 —— 于是“治理”这个动作对只在 Web 端的人根本不存在。
// 未装配 ctx.memory(host-memory 未启用)→ 503 → 整段隐藏(不摆空壳)。
const memReady = ref(false);
const memEnabled = ref(true);
const memBudget = ref(2048);
const memUser = ref<string[]>([]);
const memProject = ref<string[]>([]);
const memPath = ref("");
const memErr = ref("");
const memMsg = ref("");
const memDraft = ref("");
const memBusy = ref(false);

// memSource 拆出行尾的「(来源: 会话 x)」—— 治理的关键动作是“按来源整段删”,
// 面板得能一键删掉“某个会话写进来的全部记忆”,而不只是逐条删。
function memSource(line: string): string {
  const m = /\(来源:\s*会话\s+(\S+?)\s*\)\s*$/.exec(line);
  return m ? m[1] : "";
}
function memContent(line: string): string {
  return line
    .replace(/\(来源:\s*会话\s+\S+?\s*\)\s*$/, "")
    .replace(/^\d+\.\s*/, "");
}
// memHasCandidates 该构建是否提供候选能力(字段组缺席 = 老版本;不渲染空壳)
const memHasCandidates = computed(() => Array.isArray(memCand.value));
const memCand = ref<string[]>([]);

// memoryRun 之后要把候选组也同步过来(它与记忆列表在同一个响应里)
function memSync(v: {
  candidates?: string[];
  candidate_used?: number;
  candidate_limit?: number;
  candidate_today?: number;
  candidate_today_max?: number;
}): void {
  if (Array.isArray(v.candidates)) {
    memCand.value = v.candidates;
    memUsed.value = v.candidate_used ?? v.candidates.length;
    memLimit.value = v.candidate_limit ?? 0;
    memToday.value = v.candidate_today ?? 0;
    memTodayMax.value = v.candidate_today_max ?? 0;
  }
}
const memUsed = ref(0);
const memLimit = ref(0);
const memToday = ref(0);
const memTodayMax = ref(0);

// memoryPropose 提一条候选。候选**不进上下文**,要确认(accept)之后才生效 ——
// 界面上把这句话放在按钮旁边,不然用户会以为「提了就已经记住了」。
function memoryPropose(): void {
  const content = memDraft.value.trim();
  if (!content) {
    memErr.value = "候选内容为空";
    return;
  }
  void memoryRun({ action: "propose", content }, () => {
    memDraft.value = "";
    return "已放进候选池(**还没进上下文**):确认之后才生效";
  });
}

function memoryAccept(index: number): void {
  void memoryRun(
    { action: "accept", index },
    () => "已转正为记忆(之后每轮带进上下文)",
  );
}

function memoryReject(index: number): void {
  void memoryRun({ action: "reject", index }, () => "已丢弃候选(没有进记忆)");
}

function memoryAcceptAll(): void {
  guard(
    "把候选池里**全部**候选转正为记忆?(之后每轮都会带进上下文)",
    true,
    () => {
      void memoryRun({ action: "accept_all" }, (n) =>
        n ? `已转正 ${n} 条为记忆` : "候选池是空的,没东西可转正",
      );
    },
  );
}

function memoryRejectAll(): void {
  guard("丢弃候选池里**全部**候选?(不进记忆)", true, () => {
    void memoryRun({ action: "reject_all" }, (n) => `已丢弃 ${n} 条候选`);
  });
}

const memSources = computed(() => {
  const seen = new Set<string>();
  for (const l of memUser.value) {
    const s = memSource(l);
    if (s) seen.add(s);
  }
  return Array.from(seen);
});

// loadMemory 拉记忆视图(读失败 → 整段隐藏)。**不覆盖正在输入的草稿**。
async function loadMemory(): Promise<void> {
  try {
    const v = await api.memory();
    if (!v || !Array.isArray(v.user)) {
      memReady.value = false;
      return;
    }
    memReady.value = true;
    memEnabled.value = !!v.enabled;
    memBudget.value = v.budget || 2048;
    memUser.value = v.user;
    memProject.value = Array.isArray(v.project) ? v.project : [];
    memPath.value = v.user_path;
    memSync(v);
  } catch {
    memReady.value = false;
  }
}

// memoryRun 发一个动作并把回执(刷新后的视图)并进状态;写动作统一走这里。
async function memoryRun(
  body: Parameters<typeof api.memoryAct>[0],
  ok: (deleted?: number) => string,
): Promise<void> {
  memBusy.value = true;
  memErr.value = "";
  memMsg.value = "";
  try {
    const v = await api.memoryAct(body);
    memEnabled.value = !!v.enabled;
    memUser.value = Array.isArray(v.user) ? v.user : [];
    memProject.value = Array.isArray(v.project) ? v.project : [];
    memSync(v);
    memMsg.value = ok(v.deleted);
  } catch (e) {
    memErr.value = (e as Error).message;
  } finally {
    memBusy.value = false;
  }
}

function memoryAdd(): void {
  const content = memDraft.value.trim();
  if (!content) {
    memErr.value = "记忆内容为空(写一句“是什么、为什么”即可)";
    return;
  }
  void memoryRun({ action: "add", content }, () => {
    memDraft.value = "";
    return "已记住";
  });
}

function memoryRemove(index: number, line: string): void {
  guard(
    "删除这条记忆?" + memContent(line) + "(删了就没了,不留回收站)",
    true,
    () => {
      void memoryRun({ action: "remove", index }, () => "已删除");
    },
  );
}

function memoryRemoveSource(src: string): void {
  guard("删掉会话 " + src + " 写进来的**全部**记忆?", true, () => {
    void memoryRun({ action: "remove_source", source: src }, (n) =>
      n ? "已删掉 " + n + " 条来自该会话的记忆" : "没有来自该会话的记忆",
    );
  });
}

// memoryToggle 一键关掉注入:关的是“注入”,不是“数据”(文件仍在,可随时再开)。
function memoryToggle(on: boolean): void {
  void memoryRun({ action: "toggle", enabled: on }, () =>
    on
      ? "已开启注入(每轮按预算注入记忆)"
      : "已关闭注入(记忆文件保留,不再进系统提示)",
  );
}

// —— 角色(第七十九批 1b) ——
// 角色 = 人设(身份句)+ 工作规则(AGENTS.md)+ 技能挂载;后端未装配 ctx.roles 时整段隐藏。
const roles = ref<RoleSpec[]>([]);
const roleLib = ref<SkillInfo[]>([]);
const roleCurrent = ref("");
const roleMax = ref(32768);
const roleProblems = ref<{ id: string; error: string }[]>([]);
const roleReady = ref(false); // 首次 /api/roles 拿到对象 = 该环境支持角色(503/形状不对 → 不渲染空壳)
const roleErr = ref("");
const roleMsg = ref("");
const selRole = ref(""); // 展开编辑的角色 id
const roleDetail = ref<RoleSpec | null>(null);
const agentsDraft = ref("");
const agentsSaved = ref(""); // 服务端上一版工作规则
const roleIDDraft = ref("");
const showRoleNew = ref(false);
const rf = ref({
  id: "",
  name: "",
  identity: "",
  description: "",
  exclude_global: false,
});
const skNew = ref({
  name: "",
  description: "",
  triggers: "",
  body: "",
  role: "",
});
const showSkillNew = ref(false);
const skErr = ref("");
const skEdit = ref<{ name: string; role: string; content: string } | null>(
  null,
);
const skSaved = ref(""); // 服务端上一版技能正文
// 改名/移动(第八十四批):技能身份 = 目录名 + 归属库。一次表单两件都能改
// (只改名/只换库/两件一起),提交走一条 relocate 端点。
const skMove = ref<{
  name: string;
  role: string;
  toName: string;
  toRole: string;
} | null>(null);
// roleDefSaved 角色**定义**的已保存基线(不含 AGENTS.md,那是 agentsSaved 管的)。
//
// 为什么要有它(2026-10-04 用户实测):这一区的控件此前是**两套语义**——
// 显示名/定位/人设三个文本框只改本地草稿(要点「保存定义」),而审批档、沙箱档、思考档、
// 模型、exclude_global 这些**选项型控件却是选中即写盘**。用户看到的正是「文本框要点确定、
// 选项一点就生效」的自相矛盾,而且选项里恰好是**权限档**(选「严格」= 危险命令直接拒)。
//
// 统一成草稿式:所有控件只改本地,「保存定义」一次性写盘。未保存有常驻标记,
// 切角色/换技能前会问一声(进 draftLabels)。
// 基线存**值快照**而不是只存一个 key:「放弃改动」要把值拨回去,只存 key 拨不回去
// (第一版就是个空操作 —— 从当前值构造 back 再盖回去,等于什么都没做)。
const roleDefSaved = ref<Partial<RoleSpec>>({});
function roleDefSnapshot(d: RoleSpec | null): Partial<RoleSpec> {
  const out: Record<string, unknown> = {};
  for (const k of ROLE_DEF_FIELDS) out[k] = d ? (d[k] ?? "") : "";
  return out as Partial<RoleSpec>;
}
// 参与基线的字段:少写一个就会出现「改了它却算不出未保存」。
const ROLE_DEF_FIELDS: (keyof RoleSpec)[] = [
  "name",
  "description",
  "identity",
  "exclude_global",
  "model",
  "thinking",
  "approval",
  "sandbox",
];
function roleDefKey(d: RoleSpec | null): string {
  if (!d) return "";
  return ROLE_DEF_FIELDS.map((k) => String(d[k] ?? "")).join("\u0001");
}
const roleDefDirty = computed(
  () =>
    !!roleDetail.value &&
    roleDefKey(roleDetail.value) !== roleDefKey(roleDefSaved.value as RoleSpec),
);

// agentsDirty / skDirty 未保存改动(凡切目标前都要问一声:刚写的东西不能静默丢)
const agentsDirty = computed(
  () => !!selRole.value && agentsDraft.value !== agentsSaved.value,
);
const skDirty = computed(
  () => !!skEdit.value && (skEdit.value?.content ?? "") !== skSaved.value,
);
// skMoveDirty 改名/移动表单是否真有变化(无变化则提交禁用:后端也会拒)
const skMoveDirty = computed(() => {
  const m = skMove.value;
  if (!m) return false;
  return m.toName.trim() !== m.name || m.toRole !== m.role;
});

// —— 回收站(第八十三批) ——
// 删除角色/技能都是"移进 .trash"(可恢复),但之前面板里没有列表也没恢复动作 ——
// 文案写着"可恢复"却无入口。这里补上:展开时才拉,不在每次轮询里拖大响应。
const trashOpen = ref(false);
const trashReady = ref(false); // 首次拉成功(形状对)→ 才渲染区块
const trashRoles = ref<TrashRoleEntry[]>([]);
const trashSkills = ref<TrashSkillEntry[]>([]);
const trashErr = ref("");
const trashCount = computed(
  () => trashRoles.value.length + trashSkills.value.length,
);

// loadRoles 拉角色列表 + 技能库(503 = 未装配 → 整段隐藏;其余错误照常提示)。
async function loadRoles(): Promise<void> {
  try {
    const v = await api.roles();
    if (!v || !Array.isArray(v.roles)) {
      roleReady.value = false; // 形状不对(旧后端/未装配):不渲染
      return;
    }
    roleReady.value = true;
    roles.value = v.roles;
    roleLib.value = v.library ?? [];
    roleCurrent.value = v.current ?? "";
    roleMax.value = v.max_agents_bytes || 32768;
    roleProblems.value = v.problems ?? [];
  } catch {
    roleReady.value = false; // 503/网络错:静默隐藏(不把「这个环境没角色功能」当故障报)
  }
}
// roleGroups 角色按 group 分节(12 个预置角色之后,平铺列表已经认不出"该挑哪个")。
// 顺序:分组按其角色在列表里的**首次出现**排序(不给分组表设死顺序 —— 新增预置角色
// 落在哪个组就出现在那个组,不用改前端)。无 group 的角色收在「其它」一节。
const roleGroups = computed(() => {
  const order: string[] = [];
  const by = new Map<string, RoleSpec[]>();
  for (const r of roles.value) {
    const g = (r.group || "").trim() || "其它";
    if (!by.has(g)) {
      by.set(g, []);
      order.push(g);
    }
    by.get(g)!.push(r);
  }
  return order.map((g) => ({ group: g, items: by.get(g)! }));
});

// roleIsCurrent 当前角色高亮(切换是改状态但可一键切回,故不进确认弹层)
function roleIsCurrent(r: RoleSpec): boolean {
  return roleCurrent.value === r.id;
}

// roleHint 未选过角色时的引导条(第一百零二批)。
//
// 判据只用**已有状态**:当前角色为空 = 从没挑过角色。选中之后这条自然永久消失,
// 因此不需要额外的「不再提示」偏好字段 —— 省一个持久化状态,也就省一个将来要迁移的东西。
// 刻意做成面板里的一行提示而不是启动浮层:浮层会打断第一次对话,而角色这件事
// **不挑也能用**(走基线:全局指令 + 全部技能),不该挡路。
const roleHint = computed(() => roleBaseline.value && roles.value.length > 1);

// roleBaseline 当前处于"基线态"(没有启用任何角色)= 列表顶部那条合成行是否标「当前」。
// 为什么在列表里给它一个位置:基线不是"什么都没配",而是**确定的运行形态**(全局指令 +
// 项目规则 + 默认技能池),它此前在面板里没有名字 —— 用户看不出"不启用角色"到底意味着什么,
// 也找不到一键切回去的落点(只有底部一个「停用当前角色」,而且只在有角色时才出现)。
// 它**只在展示层合成**:磁盘上没有任何对应实体(不落 role.yaml、不进 /api/roles 的 Roles),
// 切换就是 POST /use 空 id(服务端认 "-" 占位符 → 清偏好)。
const roleBaseline = computed(() => roleCurrent.value === "");

// roleDangling 偏好里的当前角色在列表里找不到(角色目录被外部删了 —— Store.Delete 拒绝删当前角色,
// 所以只可能是外部删文件)。这里**不静默改偏好**(偏好是用户状态,替他清掉下次他自己都查不出角色
// 为什么没了),只把事实说清楚 + 给出合成行的「切换」作为一键修复落点;运行侧由 host-roles 的
// BlockText 明示"本轮按基线运行"。
const roleDangling = computed(
  () =>
    !!roleCurrent.value && !roles.value.some((r) => r.id === roleCurrent.value),
);

// selectRole 展开某角色的编辑区(列表不带正文,展开时才拉详情)/ 再点一次收起。
// confirmed=true 表示"草稿丢弃已经过用户确认"(防止重入时再问一遍)。
async function selectRole(r: RoleSpec, confirmed = false): Promise<void> {
  if (!confirmed) {
    const d = draftLabels(["role", "skill"]);
    if (d.length) {
      withDrafts(() => void selectRole(r, true), ["role", "skill"]);
      return;
    }
  }
  if (selRole.value === r.id) {
    selRole.value = "";
    roleDetail.value = null;
    return;
  }
  roleErr.value = "";
  roleMsg.value = "";
  try {
    const d = await api.roleGet(r.id);
    roleDetail.value = d;
    roleDefSaved.value = roleDefSnapshot(d);
    agentsDraft.value = d.agents ?? "";
    agentsSaved.value = d.agents ?? "";
    roleIDDraft.value = d.id;
    skEdit.value = null;
    skSaved.value = "";
    selRole.value = r.id;
  } catch (e) {
    roleErr.value = (e as Error).message;
  }
}

// discardAgents 显式放弃工作规则草稿(回到服务端已保存的那一版)。
function discardAgents(): void {
  guard("放弃尚未保存的工作规则修改?", true, () => {
    agentsDraft.value = agentsSaved.value;
    roleErr.value = "";
  });
}

// —— 角色定义的**即时提交**(挂载勾选/并入/排除/模型/思考档:点一下发一次) ——
// 串行化 + 在途禁用。为何不能各发各的:挂载清单是**整份替换**(skills_set + skills),
// 连点两次会让后一个请求带着旧清单覆盖前一个(服务端的原子写只保证“单次写不丢”,救不了
// 客户端基于旧状态构造的补丁)—— 表现为“勾了两个技能只生效一个”。所以补丁改成**入队时现算**
// (builder,排队期间前面几次的写已经回填到 roleDetail),并按提交顺序逐个发;
// 在途期间相关控件 :disabled(点了不生效也要看得出来)。
const roleSaving = ref(false);
let rolePatchQueue: Promise<void> = Promise.resolve();
let roleSavingN = 0;
type RolePatch = Parameters<typeof api.roleUpdate>[1];
function queueRolePatch(build: () => RolePatch): Promise<void> {
  const id = selRole.value;
  if (!id) return Promise.resolve();
  roleSavingN++;
  roleSaving.value = true;
  const step = async (): Promise<void> => {
    // finally 必须盖住 build() 与「补丁为空」两条早退路径:计数不配对会让 roleSaving
    // 永远为真,整片控件从此点不动。
    try {
      const patch = build(); // 到执行时才读当前状态
      if (!patch || Object.keys(patch).length === 0) return;
      const d = await api.roleUpdate(id, patch);
      roleDetail.value = {
        ...(roleDetail.value as RoleSpec),
        ...d,
        agents: agentsDraft.value,
      };
      // 基线跟着服务端回包走:不刷新的话「未保存」标记在保存成功之后仍然亮着,
      // 而「保存定义」按钮又因为 !roleDefDirty 变灰 —— 看上去像存了又没存。
      roleDefSaved.value = roleDefSnapshot(roleDetail.value);
      await loadRoles();
      roleMsg.value = "已保存";
    } catch (e) {
      roleErr.value = (e as Error).message;
    } finally {
      roleSavingN--;
      roleSaving.value = roleSavingN > 0;
    }
  };
  rolePatchQueue = rolePatchQueue.then(step, step);
  return rolePatchQueue;
}
// saveRoleDefNow 把角色**定义**的整份草稿一次性写盘。
//
// 为什么不是「每个控件各自保存」:同一区里两种语义并排(文本框要点保存、选项一点就生效)
// 会让人无法形成稳定预期 —— 2026-10-04 用户实测报的就是这个。而选项里恰好是**权限档**
// (选「严格」= 危险命令直接拒、子代理与定时任务同样按它跑),最不该误触即生效。
async function saveRoleDefNow(): Promise<void> {
  const d = roleDetail.value;
  if (!d) return;
  await saveRoleDef({
    name: d.name ?? "",
    description: d.description ?? "",
    identity: d.identity ?? "",
    exclude_global: !!d.exclude_global,
    model: d.model ?? "",
    thinking: d.thinking ?? "",
    approval: d.approval ?? "",
    sandbox: d.sandbox ?? "",
  });
}

// revertRoleDef 放弃定义区的未保存改动(把本地值拨回已保存基线)。
//
// 为什么要有它:草稿式的代价是「改了没保存就走了」的风险,所以需要一个**不丢数据也不写盘**
// 的退出口 —— 只切角色会问「要不要丢」,但用户多数时候只是想撤销刚才那一下。
function revertRoleDef(): void {
  const d = roleDetail.value;
  if (!d) return;
  roleDetail.value = {
    ...d,
    ...roleDefSnapshot(roleDefSaved.value as RoleSpec),
  } as RoleSpec;
}

// —— 角色权限收紧(第九十二批) ——
// 角色只能把审批/沙箱**往里收**:实际档 = 全局档与角色档里更严的那个(后端合成,
// 前端不自己算 —— 算出来跟真正裁决的档位漂开就是假事实)。这里只做三件事:
// ① 下拉值域只给"更严"的档;② 把"当前实际生效"回显出来;③ 说清停用角色后回到全局档。
const roleTierNote = computed(() => {
  const st = props.state;
  const parts: string[] = [];
  if (st.approval_from === "role" && st.approval_effective) {
    parts.push(
      `审批 全局${AP_ZH[st.approval ?? ""] ?? st.approval} → 实际${AP_ZH[st.approval_effective] ?? st.approval_effective}`,
    );
  }
  if (st.sandbox_from === "role" && st.sandbox_effective) {
    parts.push(
      `沙箱 全局${SB_ZH[st.sandbox] ?? st.sandbox} → 实际${SB_ZH[st.sandbox_effective] ?? st.sandbox_effective}`,
    );
  }
  return parts.join(";");
});
// tierBrief 角色列表行里的收紧摘要(只列声明了什么,有效档由上面的 roleTierNote 负责)。
function tierBrief(r: RoleSpec): string {
  const a = r.approval ? "审批" + (AP_ZH[r.approval] ?? r.approval) : "";
  const b = r.sandbox ? "沙箱" + (SB_ZH[r.sandbox] ?? r.sandbox) : "";
  return a + (a && b ? " · " : "") + b || "跟随全局";
}

// saveRoleDef 提交角色定义的部分更新(只传改动的字段;更新后同步列表与详情)。
// 接受对象或 builder:依赖当前状态的字段(挂载清单)必须传 builder —— 否则排队期间算出的
// 旧快照会把前一次提交盖回去。
function saveRoleDef(patch: RolePatch | (() => RolePatch)): Promise<void> {
  return queueRolePatch(typeof patch === "function" ? patch : () => patch);
}

// saveAgents 保存工作规则(会逐字进系统提示 → 二次确认 + 上限提示)
function saveAgents(): void {
  const id = selRole.value;
  const text = agentsDraft.value;
  if (!id) return;
  if (byteLength(text) > roleMax.value) {
    roleErr.value = `工作规则超上限:${byteLength(text)} > ${roleMax.value} 字节(会逐字进系统提示,请精简)`;
    return;
  }
  guard(
    "保存「" +
      id +
      "」的工作规则?(写入 roles/" +
      id +
      "/AGENTS.md,下一轮系统提示生效)",
    false,
    async () => {
      roleErr.value = "";
      try {
        await api.roleSetAgents(id, text);
        agentsSaved.value = text;
        roleMsg.value = "工作规则已保存";
        await loadRoles();
      } catch (e) {
        roleErr.value = (e as Error).message;
      }
    },
  );
}

// useRole 切换**全局当前角色**(空 id = 停用回基线)。不换会话 —— 回合历史与工作区都不动。
//
// session:"" 是**必须的**(第一百三十八批):api.roleUse 不给 session 时默认绑当前页签,
// 而本面板按作用域纪律只改全局默认(与模型/思考/沙箱/审批一致);本会话要另选角色,
// 在右上角「当前会话设置」面板里选。
function useRole(id: string): void {
  roleErr.value = "";
  void (async () => {
    try {
      await api.roleUse(id, { session: "" });
      roleCurrent.value = id;
      roleMsg.value = id
        ? "已切换到「" + id + "」(下一轮生效,未改会话历史)"
        : "已停用角色(回到基线)";
      emit("changed"); // App 重新拉 /api/state → 状态栏徽标同步
      await loadRoles();
    } catch (e) {
      roleErr.value = (e as Error).message;
    }
  })();
}

// createRole 新建(可重复:ID 重复由后端显式报错)
function createRole(): void {
  roleErr.value = "";
  void (async () => {
    try {
      await api.roleCreate({ ...rf.value });
      roleMsg.value =
        "已新建角色「" + rf.value.id + "」(已给一份可改的规则模板)";
      const id = rf.value.id;
      showRoleNew.value = false;
      rf.value = {
        id: "",
        name: "",
        identity: "",
        description: "",
        exclude_global: false,
      };
      await loadRoles();
      if (id) await selectRole({ id } as RoleSpec);
    } catch (e) {
      roleErr.value = (e as Error).message;
    }
  })();
}

// renameRole 改 ID(目录改名;当前角色会跟随)
function renameRole(): void {
  const id = selRole.value;
  const next = roleIDDraft.value.trim();
  if (!id || next === id) return;
  guard(
    "把角色 " + id + " 的标识改为 " + next + "?(目录会改名,当前角色设置跟随)",
    false,
    async () => {
      roleErr.value = "";
      try {
        const d = await api.roleRename(id, { id: next });
        roleMsg.value = "已改标识:" + d.id;
        // 改名不是换角色:工作规则草稿必须留在手里 —— 旧实现接着 selectRole(d) 会撞
        // 「同 id = 收起」分支把编辑区收掉,再展开时又用服务端版本盖掉草稿。
        selRole.value = d.id;
        if (roleDetail.value)
          roleDetail.value = { ...roleDetail.value, id: d.id };
        roleIDDraft.value = d.id;
        await loadRoles();
      } catch (e) {
        roleErr.value = (e as Error).message;
      }
    },
  );
}

// deleteRole 删除(移入回收站;当前角色后端会拒)
function deleteRole(r: RoleSpec): void {
  guard(
    "删除角色「" + r.name + "」?(移入 roles/.trash/,可在下方回收站恢复)",
    true,
    async () => {
      roleErr.value = "";
      try {
        await api.roleDelete(r.id);
        if (selRole.value === r.id) {
          selRole.value = "";
          roleDetail.value = null;
        }
        roleMsg.value = "已删除「" + r.id + "」(在回收站里,可恢复)";
        await loadRoles();
        if (trashOpen.value) await loadTrash();
      } catch (e) {
        roleErr.value = (e as Error).message;
      }
    },
  );
}

// —— 角色包(第九十三批):导出/导入 ——
// 导出:浏览器直接下载;桌面壳**没有下载通道**(Tauri 未注册 wry 的 on_download ⇒ `<a download>`
// 点了什么都不会发生,见 Sidebar.vue 同款说明),改由服务端写文件 —— 先用原生选择器挑目录,
// 再跑 /role export(与 TUI 同一个实现,界面只负责给路径)。
// —— 技能包(第一百零六批):共享技能的导出/导入。角色私有技能随**角色包**走(那边已带),
// 所以这里只管共享库那一份 —— 两条路合起来分享没有缺口。
const skPackAs = ref(""); // 「导入为」目标技能名(留空 = 用包里的原名)
const skPackBusy = ref(false);
const showSkPackImport = ref(false);

// exportSkillPack 导出共享技能(浏览器直吃 /api/skillpack/<名>,含 Content-Disposition)。
// 导出的包**只有那个技能的 SKILL.md**;角色私有技能不在其中(用角色包导)。
function exportSkillPack(name: string): void {
  skErr.value = "";
  const a = document.createElement("a");
  a.href = skillPackDownloadUrl(name);
  a.download = skillPackName(name);
  document.body.appendChild(a);
  a.click();
  a.remove();
  roleMsg.value =
    "已开始下载 " +
    skillPackName(name) +
    "(包内只有该技能的 SKILL.md;导入端不执行任何脚本)";
}

// onSkPackPick 导入技能包。同名已存在时后端**显式拒绝**(400),再问一次是否覆盖 ——
// 不静默覆盖别人的技能(与本地写入、角色包同款口径)。
async function onSkPackPick(e: Event): Promise<void> {
  const input = e.target as HTMLInputElement;
  const file = input.files && input.files[0];
  if (!file) return;
  skPackBusy.value = true;
  skErr.value = "";
  roleMsg.value = "";
  const opts = skPackAs.value ? { as: skPackAs.value } : {};
  try {
    const res = await api.skillPackImport(file, opts);
    await loadRoles();
    roleMsg.value =
      "已导入技能 " +
      res.name +
      "(" +
      res.bytes +
      " 字节)" +
      (res.replaced ? "(覆盖了同名旧技能)" : "") +
      (res.warning ? "；注意:" + res.warning : "");
  } catch (err) {
    const msg = (err as Error).message;
    if (
      /已存在/.test(msg) &&
      confirm("同名技能已存在。要覆盖它吗?(旧份进回收站,可恢复)")
    ) {
      try {
        const res2 = await api.skillPackImport(file, {
          ...opts,
          overwrite: true,
        });
        await loadRoles();
        roleMsg.value = "已覆盖导入技能 " + res2.name + "(旧份在回收站可恢复)";
      } catch (e2) {
        skErr.value = "覆盖导入失败:" + (e2 as Error).message;
      }
    } else {
      skErr.value = "导入失败:" + msg;
    }
  } finally {
    skPackBusy.value = false;
    input.value = "";
  }
}

const packAs = ref(""); // 「导入为」目标 ID(留空 = 用包里的原始标识)
const packBusy = ref(false);
const showPackImport = ref(false);

function exportRole(r: RoleSpec): void {
  roleErr.value = "";
  roleMsg.value = "";
  if (!isDesktop) {
    const a = document.createElement("a");
    a.href = rolePackDownloadUrl(r.id);
    a.download = rolePackName(r.id);
    document.body.appendChild(a);
    a.click();
    a.remove();
    roleMsg.value =
      "已开始下载 " +
      rolePackName(r.id) +
      "(文件里含定义 + 工作规则 + 私有技能)";
    return;
  }
  void (async () => {
    const dir = await pickDirectory("选择角色包保存目录");
    if (!dir) {
      roleMsg.value = "已取消导出";
      return;
    }
    packBusy.value = true;
    try {
      const path = dir.replace(/[\\/]+$/, "") + "/" + rolePackName(r.id);
      const res = await api.commandRun("role", ["export", r.id, path]);
      if (res.error) roleErr.value = res.error;
      else roleMsg.value = res.output || "已导出到 " + path;
    } catch (e) {
      roleErr.value = (e as Error).message;
    } finally {
      packBusy.value = false;
    }
  })();
}

// afterPack 导入成功后的统一收尾:说清落在哪、带了什么、旧份去哪了,再刷新列表。
async function afterPack(res: RolePackResult): Promise<void> {
  const bits = ["已导入角色「" + (res.name || res.id) + "」(" + res.id + ")"];
  if (res.manifest?.id && res.manifest.id !== res.id)
    bits.push("包里原本是 " + res.manifest.id);
  bits.push("技能 " + (res.skills?.length ? res.skills.join("、") : "无"));
  if (res.replaced)
    bits.push("旧的那份已移入回收站(" + (res.backup_name || "") + "),可恢复");
  roleMsg.value = bits.join(" · ");
  packAs.value = "";
  showPackImport.value = false;
  await loadRoles();
  if (trashOpen.value) await loadTrash();
}

// importRolePack 导入。默认**不覆盖**:后端对同名目标显式 400,这里问过用户(并说明旧份进回收站)
// 才带 overwrite 重试 —— 覆盖是有副作用的动作,不能悄悄做。
async function importRolePack(file: File): Promise<void> {
  roleErr.value = "";
  roleMsg.value = "";
  const as = packAs.value.trim();
  packBusy.value = true;
  let askWho = "";
  try {
    if (!(await tryImport(file, as, false))) askWho = roleErr.value;
  } catch (e) {
    askWho = (e as Error).message;
    roleErr.value = "";
  } finally {
    packBusy.value = false;
  }
  if (!askWho) return;
  // 只对"同名已存在"问一次;别的错误(frontmatter 不一致/包坏了)直接摆出来,不该被覆盖确认掩住
  if (!askWho.includes("已存在")) {
    roleErr.value = askWho;
    return;
  }
  const m = /角色 ([^ ]+) 已存在/.exec(askWho);
  guard(
    (m
      ? "导入会覆盖已存在的角色「" + m[1] + "」？"
      : "导入会覆盖已存在的同名角色？") +
      "(旧的那份移入 roles/.trash/，可在下方回收站恢复。不想覆盖就改用「导入为」换一个标识)",
    true,
    () => {
      packBusy.value = true;
      void (async () => {
        try {
          await tryImport(file, as, true);
        } finally {
          packBusy.value = false;
        }
      })();
    },
  );
}

// tryImport 单次导入尝试:成功 → 收尾并返回 true;失败 → 把错误文案落到 roleErr 后返回 false
// (roleErr 就是给调用方看的"为什么没成",不再另设一个变量传话)。
async function tryImport(
  file: File,
  as: string,
  overwrite: boolean,
): Promise<boolean> {
  try {
    const res = await api.rolePackImport(file, {
      ...(as ? { as } : {}),
      overwrite,
    });
    await afterPack(res);
    return true;
  } catch (e) {
    roleErr.value = (e as Error).message;
    return false;
  }
}

function onPackPick(ev: Event): void {
  const el = ev.target as HTMLInputElement;
  const f = el.files?.[0];
  el.value = ""; // 清掉便于同一个文件再选一次
  if (f) void importRolePack(f);
}

// —— 技能挂载 ——
// 勾选即提交(可一键改回,不进确认弹层);「默认池 / 替换」是模式选择,同样即时。
function mounted(r: RoleSpec | null, name: string): boolean {
  return !!r?.skills?.includes(name);
}
async function toggleMount(name: string, on: boolean): Promise<void> {
  if (!roleDetail.value) return;
  await saveRoleDef(() => {
    const next = new Set(roleDetail.value?.skills ?? []);
    if (on) next.add(name);
    else next.delete(name);
    return { skills_set: true, skills: Array.from(next).sort() };
  });
  roleMsg.value = "挂载已更新(下一轮生效)";
}
function useDefaultPool(): void {
  void saveRoleDef(() => ({ skills_set: false }));
}
function useReplacePool(): void {
  void saveRoleDef(() => ({
    skills_set: true,
    skills: roleDetail.value?.skills ?? [],
  }));
}
function toggleInherit(on: boolean): void {
  void saveRoleDef(() => ({ skills_inherit: on }));
}
// staleMounts 挂载清单里**库里已经不存在**的技能名(技能被删/改名后残留)。
// 为什么要单列出来:下面的勾选列表只按「库里的技能」渲染,这些名字没有对应行 ⇒
// 既看不见也取消不掉(勾选提交时会带着它们一起回去),只能靠切「默认池」绕。
const staleMounts = computed(() => {
  const d = roleDetail.value;
  if (!d || !d.skills_set) return [];
  const known = new Set(roleLib.value.map((s) => s.name));
  return (d.skills ?? []).filter((n) => !known.has(n));
});

// —— 工具排除(第九十一批) ——
// 与技能挂载**方向相反**:技能是"默认池/替换/再并入"的挂载清单,工具是**排除清单**
// (默认全给,勾上 = 排除)。为何不做成白名单:工具面随插件装卸频繁变化,白名单会让
// 新装的插件对老角色**静默不可见**("这个角色莫名少了个工具",用户归因不了)。
const toolsOpen = ref(false);
const toolAll = ref<ToolDef[]>([]);
const toolsErr = ref("");
const toolQuery = ref("");
const toolsLoaded = ref(false);
// loadTools 拉**全量**工具清单(管理面:被角色排除的也得列得出来,否则分不清
// "被角色排除"与"没装这个插件")。展开时才拉,不进每次轮询。
async function loadTools(): Promise<void> {
  toolsErr.value = "";
  try {
    const list = await api.tools(true);
    toolAll.value = Array.isArray(list) ? list : [];
    toolsLoaded.value = true;
  } catch (e) {
    toolsErr.value = (e as Error).message;
  }
}
function toggleTools(): void {
  toolsOpen.value = !toolsOpen.value;
  if (toolsOpen.value && !toolsLoaded.value) void loadTools();
}
// toolExcluded 当前角色的排除清单里有没有它(展示与提交同一判据:后端用 sdk.ToolVisible)。
function toolExcluded(name: string): boolean {
  return !!roleDetail.value?.tools_exclude?.includes(name);
}
// toolShown 搜索过滤(工具可能上百条;长清单会把面板撑得很长)。
const toolShown = computed(() => {
  const q = toolQuery.value.trim().toLowerCase();
  if (!q) return toolAll.value;
  return toolAll.value.filter(
    (t) =>
      t.name.toLowerCase().includes(q) ||
      (t.description ?? "").toLowerCase().includes(q),
  );
});
// excludedCount 已排除项数(含悬空名 —— 它们同样在 role.yaml 里占着一行)。
const excludedCount = computed(
  () => roleDetail.value?.tools_exclude?.length ?? 0,
);
// staleTools 排除清单里**当前不存在**的工具名(插件卸了/名字改了)。悬空名不报错
// (后端只校验形状,存量悬空名放行),但必须看得见、清得掉 —— 否则用户只能手改 role.yaml。
const staleTools = computed(() => {
  const d = roleDetail.value;
  if (!d || !toolsOpen.value || !toolsLoaded.value) return [];
  const known = new Set(toolAll.value.map((t) => t.name));
  return (d.tools_exclude ?? []).filter((n) => !known.has(n));
});
// toggleTool 勾选即提交(on=true = **排除**它);builder 形式 —— 排队的多个勾选按提交序
// 逐个基于最新状态重算(整份替换的字段不能用旧快照)。
async function toggleTool(name: string, on: boolean): Promise<void> {
  if (!roleDetail.value) return;
  await saveRoleDef(() => {
    const next = new Set(roleDetail.value?.tools_exclude ?? []);
    if (on) next.add(name);
    else next.delete(name);
    return { tools_exclude: Array.from(next).sort() };
  });
  roleMsg.value = "工具可见性已更新(下一轮生效)";
}
// restoreAllTools 一键全部恢复(清空排除清单)。
function restoreAllTools(): void {
  void saveRoleDef(() => ({ tools_exclude: [] }));
}
function removeStaleTool(name: string): void {
  void saveRoleDef(() => ({
    tools_exclude: (roleDetail.value?.tools_exclude ?? []).filter(
      (n) => n !== name,
    ),
  }));
}
// removeStaleMount 把失效挂载从清单里清掉(后端只对**新增**未知名严格,存量悬空名放行)。
function removeStaleMount(name: string): void {
  void saveRoleDef(() => ({
    skills_set: true,
    skills: (roleDetail.value?.skills ?? []).filter((n) => n !== name),
  }));
}

// skillsByRole 库技能按归属分组展示(共享库在前,各自角色私有在后)
const skillsByRole = computed(() => {
  const groups = new Map<string, SkillInfo[]>();
  for (const s of roleLib.value) {
    const k = s.role ?? "";
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k)!.push(s);
  }
  return Array.from(groups.entries()).sort((a, b) =>
    a[0] === "" ? -1 : b[0] === "" ? 1 : a[0].localeCompare(b[0]),
  );
});
function groupLabel(role: string): string {
  return role ? `角色私有 · ${role}` : "共享技能库";
}
// createSkill 新建技能(默认落共享库;展开角色详情时可勾选存为角色私有)
function createSkill(): void {
  skErr.value = "";
  const t = skNew.value.triggers
    .split(/[,，\n]/)
    .map((x) => x.trim())
    .filter(Boolean);
  void (async () => {
    try {
      const r = await api.skillCreate({
        role: skNew.value.role,
        name: skNew.value.name.trim(),
        description: skNew.value.description,
        triggers: t,
        body: skNew.value.body,
        overwrite: false,
      });
      roleMsg.value = r.warning ? r.warning : "技能已创建:" + r.name;
      showSkillNew.value = false;
      skNew.value = {
        name: "",
        description: "",
        triggers: "",
        body: "",
        role: "",
      };
      await loadRoles();
      if (roleDetail.value)
        await selectRole({ id: roleDetail.value.id } as RoleSpec);
    } catch (e) {
      skErr.value = (e as Error).message;
    }
  })();
}
// openSkill 读技能原文(编辑)。confirmed=true 表示草稿丢弃已经过用户确认。
// 注意闸门**不分“是不是同一个技能”**:同一个技能再点一次「原文」= 重读磁盘,
// 照样会把手里没保存的正文盖掉(旧实现用一个 `same` 条件把这一次跳过了)。
async function openSkill(
  name: string,
  role: string,
  confirmed = false,
): Promise<void> {
  if (!confirmed && skDirty.value) {
    withDrafts(() => void openSkill(name, role, true), ["skill"]);
    return;
  }
  skErr.value = "";
  try {
    const r = await api.skillGet(name, role);
    skEdit.value = { name, role, content: r.content };
    skSaved.value = r.content;
  } catch (e) {
    skErr.value = (e as Error).message;
  }
}
// discardSkill 显式放弃技能正文草稿。
function discardSkill(): void {
  guard("放弃尚未保存的技能修改?", true, () => {
    if (skEdit.value)
      skEdit.value = { ...skEdit.value, content: skSaved.value };
    skErr.value = "";
  });
}
function saveSkill(): void {
  const e = skEdit.value;
  if (!e) return;
  guard("覆盖写入技能 " + e.name + " 的 SKILL.md?", false, async () => {
    skErr.value = "";
    try {
      await api.skillCreate({
        role: e.role,
        name: e.name,
        content: e.content,
        overwrite: true,
      });
      roleMsg.value = "技能已保存:" + e.name;
      skSaved.value = e.content;
      skEdit.value = null;
      await loadRoles();
    } catch (err) {
      skErr.value = (err as Error).message;
    }
  });
}
function deleteSkill(name: string, role: string): void {
  guard(
    "删除技能 " + name + "?(移入技能库 .trash/,可在下方回收站恢复)",
    true,
    async () => {
      skErr.value = "";
      try {
        await api.skillDelete(name, role);
        roleMsg.value = "技能已删除:" + name + "(可在回收站恢复)";
        await loadRoles();
        if (trashOpen.value) await loadTrash();
      } catch (e) {
        skErr.value = (e as Error).message;
      }
    },
  );
}

// openMove 打开改名/移动表单(默认填现值);正文有未保存草稿时先问一声(与其他编辑入口同口径)。
function openMove(name: string, role: string, confirmed = false): void {
  if (!confirmed && skDirty.value) {
    withDrafts(() => openMove(name, role, true), ["skill"]);
    return;
  }
  skErr.value = "";
  skMove.value = { name, role, toName: name, toRole: role };
}

// refreshRoleDetail 重拉当前展开角色的详情。改技能名/库会连带改角色挂载清单,
// 不重拉就会让面板拿着旧清单,把已经被同步过的挂载误列成「已失效挂载」。
async function refreshRoleDetail(): Promise<void> {
  if (!selRole.value) return;
  try {
    roleDetail.value = await api.roleGet(selRole.value);
  } catch {
    // 详情拉不到不影响已提交成功的改动
  }
}

// submitMove 提交改名/移动。为什么二次确认:目录名即身份 —— 改名会同步改掉每个挂载它的
// 角色(面板没有一键回退),且目标同名会被后端拒绝(不覆盖现役技能)。
function submitMove(): void {
  const m = skMove.value;
  if (!m || !skMoveDirty.value) return;
  const to = m.toName.trim();
  const fromLib = m.role ? `角色私有 · ${m.role}` : "共享技能库";
  const toLib = m.toRole ? `角色私有 · ${m.toRole}` : "共享技能库";
  const what = to !== m.name ? `改名为「${to}」` : "保留原名";
  guard(
    `把 ${m.name}(${fromLib})${what}并移到「${toLib}」?改名会同步改掉挂载它的角色引用;目标同名会被拒。`,
    false,
    async () => {
      skErr.value = "";
      try {
        const r = await api.skillRelocate(m.name, {
          role: m.role,
          to_name: to,
          to_role: m.toRole,
        });
        const n = (r.mounts_updated ?? []).length;
        roleMsg.value =
          (r.warning ? r.warning + " · " : "") +
          `技能已归位:${m.name} → ${r.role ? "角色私有 · " + r.role + " / " : "共享库 / "}${r.name}` +
          (n ? `(已同步 ${n} 个角色的挂载)` : "");
        if (
          skEdit.value &&
          skEdit.value.name === m.name &&
          skEdit.value.role === m.role
        ) {
          skEdit.value = null; // 正在编辑的对象被挪走了:收起旧草稿(内容已随目录一起搬走)
          skSaved.value = "";
        }
        skMove.value = null;
        await loadRoles();
        await refreshRoleDetail();
        if (trashOpen.value) await loadTrash();
      } catch (e) {
        skErr.value = (e as Error).message;
      }
    },
  );
}

// toggleTrash 展开/收起回收站(展开时才拉)。
async function toggleTrash(): Promise<void> {
  trashOpen.value = !trashOpen.value;
  if (trashOpen.value) await loadTrash();
}

async function loadTrash(): Promise<void> {
  trashErr.value = "";
  try {
    const v = await api.trash();
    trashReady.value = true;
    trashRoles.value = v.roles ?? [];
    trashSkills.value = v.skills ?? [];
  } catch (e) {
    trashReady.value = false; // 拉不到就整块不渲染(不摆空壳)
    trashErr.value = (e as Error).message;
  }
}

// fmtTrashTime 回收站目录名尾部的时间戳(20260928-153005)→ 2026-09-28 15:30。
// 认不出(手工放进来的目录)就原样回空串。
function fmtTrashTime(s: string): string {
  const m = /^(\d{4})(\d{2})(\d{2})-(\d{2})(\d{2})/.exec(s || "");
  return m ? `${m[1]}-${m[2]}-${m[3]} ${m[4]}:${m[5]}` : "";
}

// restoreTrash 恢复一条回收站记录。name 是**回收站目录名**(不是原 ID/技能名)。
// 同名已存在时后端显式拒绝(不覆盖现役角色/技能)—— 弹层里先说明这一点。
function restoreTrash(
  kind: "role" | "skill",
  e: { name: string; id?: string; skill?: string; role?: string },
): void {
  const label = kind === "role" ? e.id || e.name : e.skill || e.name;
  guard(
    "恢复「" + label + "」?(从回收站移回原位;已存在同名项时会被拒)",
    false,
    async () => {
      trashErr.value = "";
      try {
        const r = await api.trashRestore({ kind, name: e.name, role: e.role });
        roleMsg.value = r.warning ? r.warning : "已恢复:" + label;
        await loadTrash();
        await loadRoles();
      } catch (err) {
        trashErr.value = (err as Error).message;
      }
    },
  );
}

// —— 数据备份(M18) ——
// 字段名与 sdk.BackupInfo 序列化一致(无 json tag → 大写);小写读会导致空名 + NaN KB。
const backups = ref<{ Name: string; Size: number; Time: number }[]>([]);
const backupMsg = ref("");
async function loadBackups(): Promise<void> {
  try {
    backups.value = (await api.backups()) ?? []; // ?? []:后端契约空数组,兜底防 null(渲染 .length 安全)
  } catch {
    backups.value = []; // 未装配(host-backup):面板显示暂无备份
  }
}
async function doBackupNow(): Promise<void> {
  busy.value = true;
  try {
    await api.backupNow();
    backupMsg.value = "已整体备份(含密钥,存于 GAH_HOME/backups)";
    showInfo("已整体备份");
    await loadBackups();
  } catch (e) {
    err.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
function doBackupRestore(): void {
  const name = backups.value[0]?.Name ?? "";
  if (!name) return;
  guard(
    "恢复备份 " + name + "?(覆盖当前数据;恢复前自动先备份当前态)",
    true,
    async () => {
      busy.value = true;
      try {
        await api.backupRestore(name);
        backupMsg.value = "已恢复 " + name + "(重启后完全生效)";
        showInfo("已恢复备份");
        await loadBackups();
      } catch (e) {
        err.value = (e as Error).message;
      } finally {
        busy.value = false;
      }
    },
  );
}
function fmtSize(n: number): string {
  if (n >= 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + " MB";
  return Math.max(1, Math.round(n / 1024)) + " KB";
}

// —— 历史与压缩 ——
async function applyHistory(): Promise<void> {
  try {
    const r = await api.settingsHistory(histN.value);
    showInfo(
      "历史注入 = " +
        (r.history === 0
          ? "全部"
          : r.history === -1
            ? "禁止"
            : "最近 " + r.history + " 条"),
    );
  } catch (e) {
    err.value = (e as Error).message;
  }
}
async function doCompact(): Promise<void> {
  busy.value = true;
  try {
    const r = await api.compact("手动压缩");
    showInfo("已压缩 " + r.folded + " 块事件");
    emit("changed");
  } catch (e) {
    err.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}
function compactNow(): void {
  guard(
    "压缩当前会话历史(旧内容折叠为摘要,不可逆)?",
    true,
    () => void doCompact(),
  );
}

// —— Provider(含 W3 首启引导/自检) ——
function toggleAdd(): void {
  showAdd.value = !showAdd.value;
  if (showAdd.value) void nextTick(() => keyInput.value?.focus());
}
// applyPreset 一键填入预设(只给 name/base_url:模型名会过期,保存后从实时列表里选)
function applyPreset(p: ProviderPreset): void {
  applied.value = p;
  // default_model 只有 OpenRouter 预设带(见 providers.ts 的理由);其余预设留空 ——
  // 模型名会过期,硬编码在预设里必然让用户撞上「模型不存在」。
  pf.value = {
    name: p.name,
    base_url: p.base_url,
    api_key: "",
    model: p.default_model ?? "",
    // 预设自带的头直接**填进表单**(可见可改),而不是悄悄在下发时补上:
    // 隐式行为出问题时用户无从查,而这两行字就是排查的起点。
    headers: p.default_headers ?? "",
  };
  showAdd.value = true;
  err.value = "";
  probe.value = null;
  void nextTick(() => keyInput.value?.focus());
}
// probeProvider 用刚刷新的聚合列表判断端点是否真通(失败给人话原因,不猜)
function probeProvider(name: string): void {
  const g = models.value.find((x) => x.Name === name);
  if (!g) {
    probe.value = {
      ok: false,
      text: "端点没出现在模型列表里:base_url 可能不可达或拼写有误",
    };
    return;
  }
  if (g.Err) {
    probe.value = { ok: false, text: explainProbeError(g.Err), raw: g.Err };
    return;
  }
  const n = (g.Models ?? []).length;
  probe.value = {
    ok: true,
    text:
      n > 0
        ? `已连通,拉到 ${n} 个模型:在上方「模型」下拉里选一个`
        : "已连通,但端点没返回模型:手动填模型名",
  };
}
// refreshProbeFromActive 当前活跃 provider 拉模型失败时把原因摆出来(打开设置就能看到为什么没模型)
function refreshProbeFromActive(): void {
  const g = models.value.find((x) => x.Name === activeProvider()?.Name);
  if (g?.Err)
    probe.value = { ok: false, text: explainProbeError(g.Err), raw: g.Err };
}
async function reprobe(): Promise<void> {
  if (!lastSaved.value) return;
  busy.value = true;
  try {
    await load();
    probeProvider(lastSaved.value);
  } finally {
    busy.value = false;
  }
}
async function doProviderUse(name: string): Promise<void> {
  try {
    await api.providerUse(name);
    emit("changed");
    await load();
  } catch (e) {
    err.value = (e as Error).message;
  }
}
async function doProviderDelete(p: ProviderInfo): Promise<void> {
  try {
    await api.providerDelete(p.Name);
    probe.value = null; // 被删端点的自检结论一并作废(不留别人的旧结论)
    showInfo("已删除 provider「" + p.Name + "」");
    emit("changed"); // 首屏/状态栏/模型下拉同步刷新
    await load();
  } catch (e) {
    err.value = (e as Error).message;
  }
}
function deleteProvider(p: ProviderInfo): void {
  guard(
    "删除 Provider「" +
      p.Name +
      "」?删除后不可恢复;若它是当前活跃的,会自动切到剩下的第一个,全部删完则回退环境变量/样板配置。",
    true,
    () => void doProviderDelete(p),
  );
}
async function addProvider(): Promise<void> {
  if (!pf.value.name || !pf.value.base_url) {
    err.value = "名称与 base_url 必填";
    return;
  }
  const name = pf.value.name;
  busy.value = true;
  try {
    // 解析放在 await 之前:格式错就该当场报,不该先发一个「保存成功」再告诉用户头没生效
    const headers = parseHeaderLines(pf.value.headers);
    await api.providerAdd({
      name: pf.value.name,
      base_url: pf.value.base_url,
      api_key: pf.value.api_key,
      model: pf.value.model || undefined,
      headers: Object.keys(headers).length ? headers : undefined,
    });
    showAdd.value = false;
    pf.value = { name: "", base_url: "", api_key: "", model: "", headers: "" };
    applied.value = null;
    probe.value = null;
    lastSaved.value = name;
    showInfo("已保存 provider(首个自动激活)");
    emit("changed");
    await load();
    probeProvider(name); // W3 连通性自检:失败给 401/404/DNS 人话
  } catch (e) {
    err.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}

// —— 定时计划(NOND-W4)——
//
// 设计前提:**默认所有使用者不会 cron**。所以界面里不存在「cron 表达式」这个词 ——
// 裸表达式只留在 /schedule 命令与模型 tool 侧(那两类使用者不是普通人)。
// 代价是「手写复杂表达式」这条路没了;换取的是「建错计划」这条路也没了。
// 三件套兑现代价:控件的表达力、后端算出的预览、以及随时可改可停。
const schedules = ref<Schedule[]>([]);
const schedErr = ref(""); // 计划段独立错误提示(不污染全局 err,便于定位)
const sf = ref({ name: "", cron: "", prompt: "" }); // 提交用(名称/描述);排期来自 schedForm
const schedForm = ref<ScheduleForm>({ ...DEFAULT_FORM }); // 控件态 = 表单唯一事实源
const schedEditing = ref(""); // 正在编辑的计划 id(空 = 新增)
const schedView = ref<ScheduleView | null>(null); // 后端解释结果(中文描述 + 预览)
const schedNL = ref(""); // 自然语言输入
const schedNLNote = ref(""); // NL 之后的回显(成功/没看懂都要说话)
const schedNLBusy = ref(false);
// schedRaw 非空 = 用**自定义表达式**保存。这是界面上唯一出现 cron 的地方,
// 它只服务一个真实用例:用户从教程/AI 那里抄来一条表达式,总得有地方粘贴。
const schedRaw = ref("");
const schedRawOpen = ref(false);
const schedRawNote = ref("");
const showSchedAdd = ref(false);
const schedReady = ref(true); // 后端未装配(ctx.schedule 503)时整段只给提示

// 「周一」而不是「一」:单字既不像词、又容易被当成破折号(实看截图时被读成「—」)。
const DOW_ZH = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"];
const DOW_ORDER = [1, 2, 3, 4, 5, 6, 0]; // 一二三四五六日(周日排最后,符合「周几」的直觉)
const HOURS = Array.from({ length: 24 }, (_, i) => i);
const MINUTES = [0, 15, 30, 45];
const MONTH_DAYS = Array.from({ length: 31 }, (_, i) => i + 1);
const NTHS = [
  { v: 1, t: "第一个" },
  { v: 2, t: "第二个" },
  { v: 3, t: "第三个" },
  { v: 4, t: "第四个" },
  { v: 5, t: "第五个" },
];
const EVERY_MIN = [2, 5, 10, 15, 30];
const EVERY_HOUR = [2, 3, 4, 6, 12];
async function loadSchedules(): Promise<void> {
  try {
    schedules.value = (await api.schedules()) ?? [];
    schedErr.value = "";
    schedReady.value = true;
  } catch (e) {
    // 503 = 宿主未装配 host-schedule:不是错误,静默隐藏本段
    if ((e as Error).message.includes("503")) {
      schedReady.value = false;
      return;
    }
    schedErr.value = (e as Error).message;
  }
}
// 控件 → 表达式 + 预览。
// 每次改动都问后端一句:一是拿权威校验(控件拼出的形态万一拼错,这里会报出来),
// 二是拿预览 —— 不会 cron 的用户没有别的验收手段,看不见就等于盲存。
async function refreshSchedulePreview(): Promise<void> {
  // 自定义表达式优先(用户明确在用它)
  if (schedRaw.value.trim()) {
    await resolveRaw(schedRaw.value.trim());
    return;
  }
  const f = schedForm.value;
  // 农历交给后端的自然语言解析:前端没有农历表,自己算不了 —— 硬写一份就是两套实现。
  const cron = structToCron(f);
  const req =
    f.repeat === "lunar_annual" ? { text: formLunarPhrase(f) } : { cron };
  if (!cron && f.repeat !== "lunar_annual") {
    schedView.value = null;
    return;
  }
  try {
    const v = await api.scheduleResolve(req);
    if (!v.ok || !v.repeat || v.repeat === "custom") {
      schedView.value = null;
      schedErr.value =
        v.reason ||
        (v.repeat === "custom"
          ? "这个日期每年都不存在(比如 2 月 30 日),换一个吧"
          : "这个排期暂时表达不了");
      return;
    }
    if (!v.next_runs || v.next_runs.length === 0) {
      schedView.value = null;
      schedErr.value = "这条排期不会触发(未来若干年内都没有匹配的日期)";
      return;
    }
    schedView.value = v as ScheduleView;
    schedErr.value = "";
  } catch (e) {
    schedView.value = null;
    schedErr.value = (e as Error).message;
  }
}

// resolveRaw 解析自定义表达式。能反解成档位就提示「可以用上面的选择器改」,
// 反解不出也允许原样保存 —— 用户明确要那条表达式,硬拦不如说清代价。
async function resolveRaw(expr: string): Promise<void> {
  try {
    const v = await api.scheduleResolve({ cron: expr });
    if (!v.ok) {
      schedView.value = null;
      schedErr.value = v.reason || "这条表达式看不懂";
      return;
    }
    schedView.value = v as ScheduleView;
    schedRawNote.value =
      v.repeat === "custom"
        ? "这条排期比较特殊,选择器里没有对应项;保存后列表显示「自定义排期」,只能停用或删除。"
        : `这条表达式等于「${v.label}」—— 可以用上面的选择器改。`;
    schedErr.value = "";
  } catch (e) {
    schedView.value = null;
    schedErr.value = (e as Error).message;
  }
}

function toggleDow(d: number): void {
  const cur = schedForm.value.dows;
  const i = cur.indexOf(d);
  schedForm.value = {
    ...schedForm.value,
    dows: (i >= 0 ? cur.filter((x) => x !== d) : [...cur, d]).sort(
      (a, b) => a - b,
    ),
  };
  void refreshSchedulePreview();
}

function useSchedPreset(patch: Partial<ScheduleForm>): void {
  // 预设会覆盖档位 → 自定义表达式那一支必须让位,否则两处排期打架
  schedRaw.value = "";
  schedRawNote.value = "";
  schedForm.value = withPreset(schedForm.value, patch);
  void refreshSchedulePreview();
}

// schedTypeChanged 换档位时的默认值与清理。
// 「自定义表达式」是互斥的一路:一旦回到档位,就不该再拿旧表达式去保存。
function schedTypeChanged(): void {
  schedRaw.value = "";
  schedRawNote.value = "";
  const cur = schedForm.value;
  if (cur.repeat === "once" && !cur.onceDate) {
    schedForm.value = { ...cur, onceDate: todayStr() };
  } else if (cur.repeat === "lunar_annual" && !cur.lunarFestival) {
    schedForm.value = { ...cur, lunarFestival: "中秋节" };
  }
  void refreshSchedulePreview();
}

// setLunarFestival 农历档位:选节日,或切到「自定义农历日期」。
function setLunarFestival(name: string): void {
  if (name === "") {
    // 切到自定义时把当前节日的月日当作起点(不留一个空的月日选择)
    const f = LUNAR_FESTIVALS.find(
      (x) => x.name === schedForm.value.lunarFestival,
    );
    schedForm.value = {
      ...schedForm.value,
      lunarFestival: "",
      month: f ? f.month : 8,
      day: f && f.day > 0 ? f.day : 1,
    };
  } else {
    schedForm.value = { ...schedForm.value, lunarFestival: name };
  }
  void refreshSchedulePreview();
}

function useRawExpression(): void {
  schedRawOpen.value = true;
  void refreshSchedulePreview();
}

function clearRawExpression(): void {
  schedRaw.value = "";
  schedRawNote.value = "";
  // 关掉粘贴区,回到选择器 —— 早先漏了这一行:按钮只清空输入框,粘贴区还开着,
  // 空输入框下看不出任何变化 = 用户报的「点改用选择器无反应」(第一百三十八批)。
  schedRawOpen.value = false;
  void refreshSchedulePreview();
}

// monthly_nth 的「周几」是单选下拉(weekly 才是多选 chip),这里做下拉 ↔ dows[] 转换。
const nthDow = computed({
  get: () => schedForm.value.dows[0] ?? 1,
  set: (v: number) => {
    schedForm.value = { ...schedForm.value, dows: [Number(v)] };
    void refreshSchedulePreview();
  },
});

// 自然语言只用来**填控件**,不直接执行 ——「晚上8点」是 20:00 还是 08:00 是高代价猜错,
// 必须让人在控件里看见结果,而不是相信一句「已识别」。
async function applyScheduleNL(): Promise<void> {
  const text = schedNL.value.trim();
  if (!text) return;
  schedNLBusy.value = true;
  try {
    const v = await api.scheduleResolve({ text });
    if (!v.ok || !v.repeat) {
      // 后端 reason 本身就是完整中文句(已含「没看懂」),别再包一层前缀。
      schedNLNote.value = v.reason || "没看懂这句话,可以直接用下面的选择器";
      return;
    }
    const f = viewToForm(v as ScheduleView);
    if (!f) {
      schedNLNote.value =
        "这句话解析出来的排期比较特殊,下面的选择器表达不了,请手动调整";
      return;
    }
    schedForm.value = f;
    schedNL.value = "";
    const hm =
      String(f.hour).padStart(2, "0") + ":" + String(f.minute).padStart(2, "0");
    schedNLNote.value = v.assumed_time
      ? `已按下面的设置排好(你没说几点,用的是 ${hm})`
      : "已按下面的设置排好,确认一下再保存";
    await refreshSchedulePreview();
  } catch (e) {
    schedNLNote.value = (e as Error).message;
  } finally {
    schedNLBusy.value = false;
  }
}

function resetSchedForm(): void {
  schedForm.value = { ...DEFAULT_FORM };
  schedView.value = null;
  schedNL.value = "";
  schedNLNote.value = "";
  schedRaw.value = "";
  schedRawNote.value = "";
  schedRawOpen.value = false;
  schedEditing.value = "";
  sf.value = { name: "", cron: "", prompt: "" };
}

async function openSchedAdd(): Promise<void> {
  if (showSchedAdd.value && !schedEditing.value) {
    showSchedAdd.value = false;
    return;
  }
  showSchedAdd.value = true;
  resetSchedForm();
  await refreshSchedulePreview();
}

// 编辑:回填控件。反解不出(复杂或限定月份)的计划只读提示 —— 简化成别的排期
// 等于骗用户「你看到的和实际跑的一样」。
async function editSchedule(s: Schedule): Promise<void> {
  if (schedEditing.value === s.id) {
    resetSchedForm();
    showSchedAdd.value = false;
    return;
  }
  showSchedAdd.value = true;
  schedEditing.value = s.id;
  schedNLNote.value = "";
  schedNL.value = "";
  sf.value = { name: s.name, cron: s.cron, prompt: s.prompt };
  if (s.lunar_date) {
    // 农历:cron 只有时分,月日在 lunar_date 上 —— 能对上节日就回填节日档
    const parts = s.lunar_date.split("-").map(Number);
    const mm = parts[0] ?? 8;
    const dd = parts[1] ?? 15;
    const fest = LUNAR_FESTIVALS.find((x) => x.month === mm && x.day === dd);
    const next = new Date(s.next_run || Date.now());
    schedForm.value = {
      ...DEFAULT_FORM,
      repeat: "lunar_annual",
      hour: Number.isNaN(next.getHours()) ? DEFAULT_FORM.hour : next.getHours(),
      minute: Number.isNaN(next.getMinutes()) ? 0 : next.getMinutes(),
      month: mm,
      day: dd,
      lunarFestival: fest ? fest.name : "",
    };
  } else if (s.once && s.once_date) {
    // 一次性:cron 形如 `0 9 20 11 *`(限定月份),控件表达不了 → 用 once_date 自己拼。
    schedForm.value = {
      ...DEFAULT_FORM,
      repeat: "once",
      onceDate: s.once_date,
    };
  } else {
    const v = await api.scheduleResolve({ cron: s.cron }).catch(() => null);
    const f = v && v.ok ? viewToForm(v as ScheduleView) : null;
    if (f) {
      schedForm.value = f;
    } else {
      // 选择器表达不了 → 落到**自定义表达式**入口(此前只能停用或删除,等于改不了)
      schedForm.value = { ...DEFAULT_FORM };
      schedRaw.value = s.cron;
      schedRawOpen.value = true;
      schedNLNote.value = "这条排期选择器里没有对应项,可以在这里改原始表达式。";
    }
  }
  await refreshSchedulePreview();
}

function cancelSchedEdit(): void {
  showSchedAdd.value = false;
  resetSchedForm();
}

// 已完成的一次性计划(only 一次且已触发):只读,只给删除。
function schedDone(s: Schedule): boolean {
  return !!s.once && !s.enabled;
}

function schedRowLabel(s: Schedule): string {
  if (schedDone(s)) return "已执行";
  if (!s.enabled) return "已停用";
  return s.cron_label || "自定义排期";
}

async function saveSchedule(): Promise<void> {
  const f = schedForm.value;
  // 自定义表达式原样提交(用户明确要它),不带 once/lunar
  if (schedRaw.value.trim()) {
    if (!sf.value.prompt.trim()) {
      schedErr.value = "请填写到点要执行的任务描述(它会作为输入发给模型)";
      return;
    }
    busy.value = true;
    try {
      const payload = {
        name: sf.value.name.trim(),
        cron: schedRaw.value.trim(),
        prompt: sf.value.prompt.trim(),
      };
      if (schedEditing.value) {
        await api.scheduleUpdate(schedEditing.value, payload);
      } else {
        await api.scheduleAdd(payload);
      }
      const wasEdit = !!schedEditing.value;
      showSchedAdd.value = false;
      resetSchedForm();
      schedErr.value = "";
      showInfo(wasEdit ? "已更新计划" : "已创建计划");
      await loadSchedules();
    } catch (e) {
      schedErr.value = (e as Error).message;
    } finally {
      busy.value = false;
    }
    return;
  }
  const cron = structToCron(f);
  if (!cron) {
    schedErr.value = "这一档还差一个选择(比如每周要选周几、只跑一次要选日期)";
    return;
  }
  if (!sf.value.prompt.trim()) {
    schedErr.value = "请填写到点要执行的任务描述(它会作为输入发给模型)";
    return;
  }
  busy.value = true;
  try {
    const payload: {
      name: string;
      cron: string;
      prompt: string;
      once?: boolean;
      once_date?: string;
      lunar_date?: string;
    } = {
      name: sf.value.name.trim(),
      cron,
      prompt: sf.value.prompt.trim(),
      once: f.repeat === "once",
      once_date: f.repeat === "once" ? f.onceDate : "",
      // 农历:cron 里只有时分,日期放这里(显式传空串以便清掉旧值)
      lunar_date: f.repeat === "lunar_annual" ? formLunarMD(f) : "",
    };
    if (schedEditing.value) {
      await api.scheduleUpdate(schedEditing.value, payload);
    } else {
      await api.scheduleAdd(payload);
    }
    const wasEdit = !!schedEditing.value;
    showSchedAdd.value = false;
    resetSchedForm();
    schedErr.value = "";
    showInfo(wasEdit ? "已更新计划" : "已创建计划");
    await loadSchedules();
  } catch (e) {
    schedErr.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}

async function toggleSchedule(s: Schedule): Promise<void> {
  // 已完成的一次性计划不能再启用:它只该跑一次,启用只会让人困惑于「为什么明天不跑」。
  if (schedDone(s)) {
    schedErr.value = "这是一条已经执行过的一次性计划,要再跑请新增一条";
    return;
  }
  try {
    await api.scheduleUpdate(s.id, { enabled: !s.enabled });
    await loadSchedules();
  } catch (e) {
    schedErr.value = (e as Error).message;
  }
}
async function doScheduleRun(id: string): Promise<void> {
  try {
    await api.scheduleRun(id);
    showInfo("已触发一次(结果在会话流与计划状态里)");
    await loadSchedules();
  } catch (e) {
    schedErr.value = (e as Error).message;
  }
}
function deleteSchedule(s: Schedule): void {
  guard("删除计划「" + s.name + "」?(定时触发从此消失)", true, async () => {
    try {
      await api.scheduleDelete(s.id);
      await loadSchedules();
    } catch (e) {
      schedErr.value = (e as Error).message;
    }
  });
}

// —— MCP server 配置(NOND-M1 第 2/3 步) ——
// 编辑态与磁盘态分离:改动全部落在本地草稿上,点「保存并重载」才写 mcp.yaml 并重启插件。
const mcpView = ref<McpView | null>(null);
const mcpDrafts = ref<McpDraft[]>([]);
// 磁盘当前内容指纹(脏检测基准;保存成功后刷新)。初值必须是「空清单」的指纹 ——
// 用 '' 会和 draftKey([]) 的 '[]' 不等,首帧即脏(打开面板先弹一次丢弃确认,按钮也提前亮)。
const mcpBase = ref(draftKey([]));
const mcpErr = ref("");
const mcpMsg = ref("");
const mcpDirty = computed(() => draftKey(mcpDrafts.value) !== mcpBase.value);
// 环境变量来源的条目只读(来自 GAH_MCP_COMMAND(S),不在文件里)
const mcpEnvRows = computed<McpServer[]>(() =>
  (mcpView.value?.servers ?? []).filter((s) => !canEdit(s)),
);
const mcpNotices = computed<string[]>(() =>
  mcpView.value ? viewNotices(mcpView.value) : [],
);

// loadMcp 拉 MCP 配置。`force=true` 才允许覆盖手里没保存的草稿(保存成功后/显式放弃后) ——
// 否则打开面板/切回来时会静默丢掉未保存的编辑(与另外三处文本框同口径)。
async function loadMcp(force = false): Promise<void> {
  if (!force && mcpDirty.value) {
    withDrafts(() => void loadMcp(true), ["mcp"]);
    return;
  }
  try {
    const v = await api.mcp();
    mcpView.value = v;
    mcpDrafts.value = v.servers.filter(canEdit).map(draftFrom);
    mcpBase.value = draftKey(mcpDrafts.value);
    mcpErr.value = "";
  } catch (e) {
    mcpErr.value = (e as Error).message;
  }
}
// discardMcp 显式放弃 MCP 草稿(回到磁盘那一份)。主动放弃也要问一声 —— 与另外两处一致。
function discardMcp(): void {
  guard(
    "放弃尚未保存的 MCP server 配置修改?(回到磁盘上的那一份)",
    true,
    () => void loadMcp(true),
  );
}
function mcpAdd(): void {
  mcpDrafts.value.push({
    name: "",
    command: "",
    enabled: true,
    mode: MCP_MODE_DIRECT,
  });
}
function mcpRemove(i: number): void {
  mcpDrafts.value.splice(i, 1);
}
// mcpRowState 草稿行的运行期状态(按 名称+模式 匹配当前视图;改了名字就是「未生效」)
function mcpRowState(r: McpDraft): string {
  const hit = (mcpView.value?.servers ?? []).find(
    (s) => s.name === r.name.trim() && s.mode === r.mode,
  );
  if (!hit) return "未生效";
  return serverStateLabel(hit);
}
function mcpRowStateClass(r: McpDraft): string {
  const hit = (mcpView.value?.servers ?? []).find(
    (s) => s.name === r.name.trim() && s.mode === r.mode,
  );
  if (!hit) return "ss-none";
  if (!hit.enabled) return "ss-none";
  return hit.loaded ? "ss-ok" : "ss-skipped";
}
async function saveMcp(): Promise<void> {
  const bad = validateDraft(mcpDrafts.value);
  if (bad) {
    mcpErr.value = bad;
    return;
  }
  busy.value = true;
  mcpMsg.value = "";
  try {
    // 整行命令交给后端按引号规则拆参数(与终端一致);args 不在这里拆。
    // 形状来源统一在 mcp.ts 的 toSavePayload(远程条目的 url/凭据由后端按名字补回)。
    const v = await api.mcpSave(toSavePayload(mcpDrafts.value));
    mcpView.value = v;
    mcpDrafts.value = v.servers.filter(canEdit).map(draftFrom);
    mcpBase.value = draftKey(mcpDrafts.value);
    mcpErr.value = "";
    mcpMsg.value = v.reload_err
      ? v.reload_err
      : "已保存并重载:模型工具表已更新";
    clearTimeout(mcpMsgTimer);
    mcpMsgTimer = window.setTimeout(() => (mcpMsg.value = ""), 8000);
  } catch (e) {
    mcpErr.value = (e as Error).message;
  } finally {
    busy.value = false;
  }
}

// —— 插件开关(manage=external/scenario 为只读,web 侧不给启停;由外部进程/场景装配管理)——
const MANAGE_META: Record<string, { label: string; tip: string }> = {
  host: { label: "宿主运行", tip: "" },
  external: {
    label: "外部进程",
    tip: "能力已由 host-bridge 外部进程(tool-basic 等)提供,勿在此启用",
  },
  scenario: {
    label: "场景装配",
    tip: "按 profile 场景启用(mock / mcp-serve / tui),勿在此启用",
  },
  web: { label: "未启用", tip: "" },
};
async function doPluginToggle(p: PluginInfo, on: boolean): Promise<void> {
  try {
    await api.pluginToggle(p.ID, on);
    emit("changed");
    await load();
  } catch (e) {
    err.value = (e as Error).message;
  }
}
function pluginToggle(p: PluginInfo): void {
  if (p.manage === "external" || p.manage === "scenario") return; // 只读,不触发
  const on = p.State !== "loaded";
  guard(
    (on ? "加载并启用插件「" : "卸载插件「") + p.ID + "」?",
    !on,
    () => void doPluginToggle(p, on),
  );
}
function manageMeta(p: PluginInfo): { label: string; tip: string } {
  return (
    MANAGE_META[p.manage] ?? {
      label: p.State === "loaded" ? "已加载" : "未启用",
      tip: "",
    }
  );
}
// 插件列表收纳:默认只列 5 条(≤5 条即全列 —— 折叠只在"长"的时候有意义),其余折进「展开全部」;
// 筛选框按 ID/类型/状态过滤。这是面板里最长的一段,不收起来左栏导航也救不了滚动。
const PLUGIN_CAP = 5;
const pluginFilter = ref("");
const pluginExpanded = ref(false);
const pluginHits = computed(() => {
  const f = pluginFilter.value.trim().toLowerCase();
  if (!f) return plugins.value;
  return plugins.value.filter((p) =>
    `${p.ID} ${p.Type} ${p.State}`.toLowerCase().includes(f),
  );
});
const pluginShown = computed(() =>
  pluginExpanded.value || pluginHits.value.length <= PLUGIN_CAP
    ? pluginHits.value
    : pluginHits.value.slice(0, PLUGIN_CAP),
);
const pluginHidden = computed(
  () => pluginHits.value.length - pluginShown.value.length,
);

// —— 指令热更 ——
async function doReload(): Promise<void> {
  try {
    await api.reload();
    showInfo("指令文件已重载");
  } catch (e) {
    err.value = (e as Error).message;
  }
}

function showInfo(s: string): void {
  info.value = s;
  clearTimeout(infoTimer);
  infoTimer = window.setTimeout(() => (info.value = ""), 3000);
}

onMounted(() => {
  void load();
  void loadMcp();
  void loadInstructions();
  void loadRoles();
  void refreshInstallList();
  window.addEventListener("keydown", onEsc, true);
  window.addEventListener("beforeunload", onBeforeUnload);
});
onUnmounted(() => {
  if (stateTimer) clearInterval(stateTimer);
  if (mcpMsgTimer) clearTimeout(mcpMsgTimer);
  if (infoTimer) clearTimeout(infoTimer);
  if (navRaf) cancelAnimationFrame(navRaf);
  window.removeEventListener("keydown", onEsc, true);
  window.removeEventListener("beforeunload", onBeforeUnload);
});
// 关闭语义(Win 端反馈):✕ / Esc / 点空白区域 —— 遮罩点击已按 2026-10-09 的统一交互启用
// (早前只留手动出口,理由是误触会丢正在编辑的表单;现在与本会话设置抽屉一致)。
// Esc 用捕获阶段并阻止冒泡,避免同时触发输入框的「Esc 清空」(草稿丢失)。
function onEsc(e: KeyboardEvent): void {
  if (!props.open || e.key !== "Escape") return;
  e.preventDefault();
  e.stopPropagation();
  emit("close");
}
// 页面卸载守卫(2026-10-09):草稿只在内存里,刷新/关标签/关窗口一卸载就没了,且重开就是服务端那版
// —— 悄无声息地丢。这里是**唯一**"关面板会丢"的路径,所以提示放这儿,不放点空白关闭上。
// 浏览器只给原生确认框、文案不可控;干净时直接 return,不给干净页面加无谓的拦截。
function anyDraftDirty(): boolean {
  return (
    instrDirty.value ||
    roleDefDirty.value ||
    agentsDirty.value ||
    skDirty.value ||
    skMoveDirty.value ||
    mcpDirty.value
  );
}
function onBeforeUnload(e: BeforeUnloadEvent): void {
  if (!anyDraftDirty()) return;
  e.preventDefault();
  e.returnValue = ""; // 规范:同时置非空 returnValue(部分浏览器只认它,否则拦不住)
}
// 打开时同步当前值与枚举
watch(
  () => props.open,
  (o) => {
    if (!o) return;
    void load();
    void loadSchedules();
    void loadMcp();
    void loadRoles();
    // 外部定位(首启引导 'provider' / 状态栏版本号 'about' / 看板「管理计划」'schedule'):
    // 段键就是导航键(同写 data-sec),段不渲染时 jumpTo 静默跳过。
    //
    // 没有外部定位时回到**上次停留的段**(第一百三十八批):正文 DOM 随 v-if 销毁重建,
    // scrollTop 归零,而导航高亮(activeSec)仍在上一段 —— 不滚过去就是用户报的
    // 「内容回滚到最上面、选项还选中着之前的」。activeSec 是组件级 ref,关开不丢。
    const target = props.focus ? (props.focus as string) : activeSec.value;
    void nextTick(() => jumpTo(target, false));
  },
);
// 定时跑完一轮(schedule/run SSE 帧)→ 刷新计划状态(上次运行/下次触发已变)
watch(
  () => props.schedTick,
  () => {
    if (props.open) void loadSchedules();
  },
);
watch(
  // state.model 必须在监听源里:选完模型后 state 才更新,漏掉它就会出现
  // 「已选了模型但当前模型不显示」(2026-09-16 真机)。
  () => [providers.value, models.value, props.state.model],
  () => {
    // 先重建选项(可能要补「(当前)」行),再算高亮 —— 顺序反了首轮就匹配不上
    buildModelOptions();
    modelVal.value = currentModelValue(
      modelOptions.value,
      props.state.model,
      activeProvider()?.Name ?? "",
    );
  },
  { immediate: true, deep: true },
);
// 打开面板即对齐开机自启与检查更新状态,并在开着期间持续跟随(托盘那边随时可能改)。
watch(
  () => props.open,
  (open) => {
    if (stateTimer) {
      clearInterval(stateTimer);
      stateTimer = undefined;
    }
    if (!open) return;
    void refreshDesktopState();
    stateTimer = setInterval(() => void refreshDesktopState(), 1500);
  },
  { immediate: true },
);
// —— 跨段上下文(D1:按分区拆子组件;状态仍留在父层)——
// 子组件用 `const { x } = inject(SETTINGS_CTX) as SettingsCtx` 直接拿到**同一批 ref / 常量**,
// 既不复制状态也不引入第二份真相。列表由 scripts/gen-settings-ctx.mjs 生成(勿手改)。
provide(SETTINGS_CTX, {
  AP,
  AP_ZH,
  DEFAULT_FORM,
  DOW_ORDER,
  DOW_ZH,
  EVERY_HOUR,
  EVERY_MIN,
  HOURS,
  LUNAR_FESTIVALS,
  LUNAR_MONTHS,
  MANAGE_META,
  MCP_MODE_DIRECT,
  MCP_MODE_SEARCH,
  MINUTES,
  MONTH_DAYS,
  NTHS,
  PLUGIN_CAP,
  PRESETS,
  PROVIDER_PRESETS,
  REPEATS,
  ROLE_AP,
  ROLE_DEF_FIELDS,
  ROLE_SB,
  SB,
  SB_ZH,
  SETTINGS_CTX,
  THINK,
  THINK_LABEL,
  UIInstallConfirmText,
  activeProvider,
  activeSec,
  addProvider,
  afterPack,
  agentsDirty,
  agentsDraft,
  agentsSaved,
  anyDraftDirty,
  api,
  applied,
  applyCtl,
  applyHistory,
  applyModel,
  applyPreset,
  applyScheduleNL,
  applyUpdateSnapshot,
  ask,
  autoState,
  autostartState,
  backupMsg,
  backups,
  bodyEl,
  buildModelOptions,
  bundledPlugins,
  busy,
  byteLength,
  canEdit,
  cancelSchedEdit,
  checkUpdate,
  clearRawExpression,
  compactNow,
  computed,
  costHint,
  createRole,
  createSkill,
  curModelLabel,
  currentModelValue,
  deleteProvider,
  deleteRole,
  deleteSchedule,
  deleteSkill,
  digestLine,
  discardAgents,
  discardInstr,
  discardMcp,
  discardSkill,
  doBackupNow,
  doBackupRestore,
  doCheckPluginUpdates,
  doCheckUpdate,
  doCompact,
  doDisablePlugin,
  doEnablePlugin,
  doInstall,
  doInstallNow,
  doInstallUpdate,
  doPluginInstallNow,
  doPluginToggle,
  doProviderDelete,
  doProviderUse,
  doReload,
  doScheduleRun,
  doTrustPlugin,
  doUIInstall,
  doUIToggle,
  doUITrust,
  doUIUninstall,
  doUninstallPlugin,
  doUntrustPlugin,
  draftFrom,
  draftKey,
  draftLabels,
  editSchedule,
  emit,
  err,
  excludedCount,
  explainProbeError,
  exportRole,
  exportSkillPack,
  filteredModels,
  fmtAbs,
  fmtCommand,
  fmtNextRun,
  fmtSize,
  fmtTrashTime,
  formLunarMD,
  formLunarPhrase,
  groupLabel,
  guard,
  hasSessionOverride,
  hiddenModelCount,
  histN,
  importRolePack,
  info,
  infoTimer,
  inject,
  installBusy,
  installErr,
  installOk,
  installPrebuilt,
  installRows,
  installSpec,
  installUpdate,
  installWarn,
  instrBytes,
  instrDirty,
  instrDraft,
  instrEdit,
  instrErr,
  instrExists,
  instrMax,
  instrMsg,
  instrOver,
  instrPath,
  instrReady,
  instrSaved,
  instrWarn,
  isDesktop,
  isRemote,
  jumpTo,
  keyInput,
  lastSaved,
  lastUpdSeq,
  load,
  loadBackups,
  loadInstructions,
  loadMcp,
  loadMemory,
  loadRoles,
  loadSchedules,
  loadTools,
  loadTrash,
  loadUIPlugins,
  lunarDayText,
  manageMeta,
  mcpAdd,
  mcpBase,
  mcpDirty,
  mcpDrafts,
  mcpEnvRows,
  mcpErr,
  mcpMsg,
  mcpMsgTimer,
  mcpNotices,
  mcpRemove,
  mcpRowState,
  mcpRowStateClass,
  mcpView,
  memBudget,
  memBusy,
  memCand,
  memContent,
  memDraft,
  memEnabled,
  memErr,
  memHasCandidates,
  memLimit,
  memMsg,
  memPath,
  memProject,
  memReady,
  memSource,
  memSources,
  memSync,
  memToday,
  memTodayMax,
  memUsed,
  memUser,
  memoryAccept,
  memoryAcceptAll,
  memoryAdd,
  memoryPropose,
  memoryReject,
  memoryRejectAll,
  memoryRemove,
  memoryRemoveSource,
  memoryRun,
  memoryToggle,
  modeHint,
  modeLabel,
  modelFilter,
  modelLabelOf,
  modelOptionValue,
  modelOptions,
  modelRank,
  modelVal,
  models,
  mounted,
  navItems,
  navRaf,
  navSettle,
  navTarget,
  nextTick,
  noPriceInfoProviders,
  nthDow,
  onBeforeUnload,
  onBodyScroll,
  onEsc,
  onMounted,
  onPackPick,
  onSkPackPick,
  onSyncToggle,
  onUnmounted,
  onboardHint,
  onlyUsableModels,
  openMove,
  openSchedAdd,
  openSkill,
  packAs,
  packBusy,
  panelSections,
  parseHeaderLines,
  pf,
  pfConflicts,
  pickDirectory,
  pickInstallDir,
  pickModel,
  pickUIDir,
  pluginExpanded,
  pluginFilter,
  pluginHidden,
  pluginHits,
  pluginShown,
  pluginStateLabel,
  pluginToggle,
  plugins,
  presets,
  previewLines,
  probe,
  probeProvider,
  props,
  provide,
  providerNames,
  providers,
  queueRolePatch,
  ref,
  refreshDesktopState,
  refreshInstallList,
  refreshProbeFromActive,
  refreshRoleDetail,
  refreshSchedulePreview,
  refreshUIState,
  removeStaleMount,
  removeStaleTool,
  renameRole,
  reprobe,
  rescanUIPlugins,
  resetSchedForm,
  resolveRaw,
  restoreAllTools,
  restoreTrash,
  revertRoleDef,
  rf,
  roleBaseline,
  roleCurrent,
  roleDangling,
  roleDefDirty,
  roleDefKey,
  roleDefSaved,
  roleDefSnapshot,
  roleDetail,
  roleErr,
  roleGroups,
  roleHint,
  roleIDDraft,
  roleIsCurrent,
  roleLib,
  roleMax,
  roleMsg,
  rolePackDownloadUrl,
  rolePackName,
  rolePatchQueue,
  roleProblems,
  roleReady,
  roleSaving,
  roleSavingN,
  roleTierNote,
  roles,
  saveAgents,
  saveInstructions,
  saveMcp,
  saveRoleDef,
  saveRoleDefNow,
  saveSchedule,
  saveSkill,
  schedDone,
  schedEditing,
  schedErr,
  schedForm,
  schedNL,
  schedNLBusy,
  schedNLNote,
  schedRaw,
  schedRawNote,
  schedRawOpen,
  schedReady,
  schedRowLabel,
  schedTypeChanged,
  schedView,
  schedules,
  secEls,
  selRole,
  selectRole,
  serverStateLabel,
  setLunarFestival,
  settingSections,
  sf,
  showAdd,
  showInfo,
  showPackImport,
  showRoleNew,
  showSchedAdd,
  showSkPackImport,
  showSkillNew,
  skDirty,
  skEdit,
  skErr,
  skMove,
  skMoveDirty,
  skNew,
  skPackAs,
  skPackBusy,
  skSaved,
  skillPackDownloadUrl,
  skillPackName,
  skillsByRole,
  skipHint,
  sourceLabel,
  sourceLine,
  staleMounts,
  staleTools,
  stateTimer,
  statusLabel,
  structToCron,
  submitMove,
  tierBrief,
  toSavePayload,
  todayStr,
  toggleAdd,
  toggleDow,
  toggleInherit,
  toggleMount,
  toggleSchedule,
  toggleTool,
  toggleTools,
  toggleTrash,
  toolAll,
  toolExcluded,
  toolQuery,
  toolShown,
  toolsErr,
  toolsLoaded,
  toolsOpen,
  trashCount,
  trashErr,
  trashOpen,
  trashReady,
  trashRoles,
  trashSkills,
  tryImport,
  uiBusy,
  uiErr,
  uiOk,
  uiPluginDigests,
  uiPluginDisabled,
  uiPluginErrors,
  uiPluginList,
  uiPluginTrustEnforced,
  uiPluginTrustNote,
  uiScanBusy,
  uiSpec,
  unlockNav,
  updAvailable,
  updBusy,
  updChecking,
  updChecks,
  updErr,
  updMsg,
  updOk,
  updateState,
  useDefaultPool,
  useRawExpression,
  useReplacePool,
  useRole,
  useSchedPreset,
  userPlugins,
  validateDraft,
  viewNotices,
  viewToForm,
  watch,
  withCurrentModel,
  withDrafts,
  withPreset,
} as SettingsCtx);

</script>

<template>
  <!-- 点空白区域关闭(与本会话设置抽屉同一交互,2026-10-09 按用户要求改;
       早前「遮罩点击不关闭」的取舍是为防误触丢正在编辑的表单,现按统一交互反转) -->
  <!-- 显隐走全局 pane 过渡(style.css):遮罩淡 + 抽屉横滑;关闭不再硬切 -->
  <Transition name="pane">
    <div v-if="open" class="mask" @click.self="emit('close')">
      <aside class="panel" role="dialog" aria-label="设置">
        <header class="head">
          <h2 class="title">设置</h2>
          <button class="x" data-tip="关闭设置" @click="emit('close')">
            ✕
          </button>
        </header>

        <div v-if="err" class="err">{{ err }}</div>
        <div v-if="info" class="info">{{ info }}</div>

        <!-- 左导航 + 右内容(窄窗口由 CSS 退化为顶部芯片条):长面板里「滚到底才能看到某段」
           由导航一次点击替代;高亮跟着滚动走。 -->
        <div class="cols">
          <nav class="nav" aria-label="设置分区">
            <button
              v-for="s in navItems"
              :key="s.key"
              class="nav-it"
              :class="{ on: activeSec === s.key }"
              @click="jumpTo(s.key)"
            >
              {{ s.label }}
            </button>
          </nav>
          <div ref="bodyEl" class="body" @scroll="onBodyScroll">
            <!-- 模型(输入筛选 + 匹配列表;模型多时原生 select 难找,点选即应用) -->
            <SettingsModelSection />

            <!-- 推理 -->
            <SettingsReasonSection />

            <!-- 历史与压缩 -->
            <!-- 角色(第七十九批 1b):人设 + 工作规则(AGENTS.md)+ 技能挂载。
             未装配 ctx.roles 的环境整段不渲染(roleReady=false),导航项也一并隐藏。 -->
            <!-- 全局指令(第八十一批):角色之外的基线层 —— 对所有角色生效,除非角色声明 exclude_global -->
            <SettingsInstrSection />

            <SettingsMemorySection />

            <SettingsRoleSection />

            <!-- 会话历史 -->
            <SettingsHistorySection />

            <!-- Provider(W3:首启引导 + 预设 + 保存后连通性自检) -->
            <SettingsProviderSection />

            <!-- 数据备份(M18) -->
            <SettingsBackupSection />

            <!-- 定时计划(NOND-W4,host-schedule 未装配时整段隐藏) -->
            <SettingsScheduleSection />

            <!-- MCP server 配置(NOND-M1 第 2/3 步:配置 + 状态 + 保存即重载) -->
            <SettingsMcpSection />

            <!-- 插件与指令 -->
            <SettingsPluginSection />

            <!-- 桌面壳专属:升级入口(浏览器直连时整段隐藏)。此前升级只在托盘菜单里,
             菜单弹不出来就等于没有入口,故补到界面上。 -->
            <SettingsAboutSection />

            <!-- v2 扩展点:设置面板区段(插件注入,每插件一节) -->
            <section
              v-for="s in panelSections"
              :key="s.key"
              :data-sec="'ext-' + s.key"
              class="sec"
            >
              <component :is="s.component" />
            </section>
          </div>
        </div>

        <!-- 固定底栏(仅桌面版):版本 + 检查更新常驻可见 —— 升级入口原本埋在面板最底,
           而插件列表一长就得滚到底才能点到它。 -->
        <footer v-if="isDesktop" class="foot">
          <div class="foot-row">
            <span class="mono ver">v{{ props.state.version || "dev" }}</span>
            <button class="ghost" :disabled="updBusy" @click="doCheckUpdate()">
              {{ updBusy ? "检查中…" : "检查更新" }}
            </button>
            <!-- 有更新才出现:检查只查不装,升级必须由用户点这一下并再确认一次 -->
            <button
              v-if="updAvailable"
              class="ghost solid"
              :disabled="updBusy"
              @click="doInstallUpdate()"
            >
              升级到 {{ updAvailable }}
            </button>
          </div>
          <p class="dim foot-msg" :class="{ ok: updOk }">
            {{ updMsg || "检查新版本;发现更新后由你确认再下载安装" }}
          </p>
        </footer>
      </aside>
    </div>
  </Transition>
</template>

<style scoped src="./settings/settings-panel.css"></style>
