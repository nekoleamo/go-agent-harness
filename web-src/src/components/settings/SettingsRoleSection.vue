<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { computed, inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";
import ScopeNote from "../ScopeNote.vue";

const { ROLE_AP, ROLE_SB, THINK, THINK_LABEL, agentsDirty, agentsDraft, busy, byteLength, createRole, createSkill, deleteRole, deleteSkill, discardAgents, discardSkill, excludedCount, exportRole, exportSkillPack, fmtTrashTime, groupLabel, mounted, onPackPick, onSkPackPick, openMove, openSkill, packAs, packBusy, removeStaleMount, removeStaleTool, renameRole, restoreAllTools, restoreTrash, revertRoleDef, rf, roleBaseline, roleCurrent, roleDangling, roleDefDirty, roleDetail, roleErr, roleGroups, roleHint, roleIDDraft, roleIsCurrent, roleLib, roleMax, roleMsg, roleProblems, roleReady, roleSaving, roleTierNote, roles, saveAgents, saveRoleDefNow, saveSkill, selRole, selectRole, showPackImport, showRoleNew, showSkPackImport, showSkillNew, skDirty, skEdit, skErr, skMove, skMoveDirty, skNew, skPackAs, skPackBusy, skillsByRole, staleMounts, staleTools, submitMove, tierBrief, toggleInherit, toggleMount, toggleTool, toggleTools, toggleTrash, toolAll, toolExcluded, toolQuery, toolShown, toolsErr, toolsLoaded, toolsOpen, trashCount, trashErr, trashOpen, trashReady, trashRoles, trashSkills, useDefaultPool, useReplacePool, useRole } = inject(SETTINGS_CTX) as SettingsCtx;
// 模板里要对它做 map ⇒ 箭头函数参数的类型必须具体(解构出来的 any 会触发 noImplicitAny)。
// 用 computed 而不是直接 `roleProblems as T[]`:前者让“这里是数组”在类型与运行时都成立
// (inject 拿到的是 ref,模板靠 proxyRefs 解包;直接断言虽然也能跑,但把类型检查骗过去了,
// 下次真把数组字段写成 ref 时会静默失效)。
const roleProblemList = computed(() => (roleProblems.value ?? []) as { id: string }[]);
</script>

<template>
  <section v-if="roleReady" data-sec="role" class="sec">
                <h3 class="h">
                  角色
                  <button
                    class="link"
                    data-tip="导入角色包(单文件 zip,别人分享给你的)"
                    @click="showPackImport = !showPackImport"
                  >
                    {{ showPackImport ? "收起" : "⤒ 导入" }}
                  </button>
                  <button
                    class="link"
                    data-tip="新建一个角色"
                    @click="showRoleNew = !showRoleNew"
                  >
                    {{ showRoleNew ? "收起" : "＋ 新建" }}
                  </button>
                </h3>
                <!-- 作用域声明(第一百三十八批):角色此前是本面板里**唯一**会话级的控件(其余四项在
                     第一百三十四批已收口到全局),与其它段的说法不一致。现在对齐:这里改全局默认,
                     本会话另选角色去右上角「当前会话设置」。 -->
                <ScopeNote what="角色" />
                <p class="dim">
                  角色 = 人设（身份句）+ 工作规则（AGENTS.md）+
                  技能挂载。切换后<strong>下一轮</strong>生效，不换会话（回合历史与工作区都不动，与切换工作区不同）。列表顶部「默认（基线）」=
                  不启用任何角色：全局指令 + 项目规则 + 默认技能池。
                </p>
                <div v-if="roleErr" class="serr">{{ roleErr }}</div>
                <p v-if="roleMsg" class="dim ok">{{ roleMsg }}</p>
                <p v-if="roleSaving" class="dim" data-testid="role-saving">
                  提交中…
                </p>
                <p v-if="roleProblemList.length" class="dim">
                  部分角色文件读不了（已跳过）：{{
                    roleProblemList.map((p) => p.id).join("、")
                  }}
                </p>

                <!-- 导入角色包:一个 zip 里装着定义 + 工作规则 + 私有技能(第九十三批)。
                 「导入为」留空 = 用包里的标识;同名时后端会拒,再问一次是否覆盖(旧份进回收站)。 -->
                <div v-if="showPackImport" class="add-form">
                  <label class="fld">
                    <span class="fld-lab">角色包文件(.zip)</span>
                    <input
                      class="inp"
                      type="file"
                      accept=".zip,application/zip"
                      :disabled="packBusy"
                      data-testid="pack-file"
                      @change="onPackPick"
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab"
                      >导入为（可选：留空 = 用包里的标识）</span
                    >
                    <input
                      v-model="packAs"
                      class="inp mono"
                      placeholder="finance-copy"
                      :disabled="packBusy"
                    />
                  </label>
                  <p class="dim">
                    包里是别人导出的角色（人设 + 工作规则 +
                    私有技能）；导入<strong>不会</strong>动你现有角色，
                    同名时默认拒绝，确认后才覆盖（旧份进回收站可恢复）。
                    想并存一份就填「导入为」换一个标识。
                  </p>
                  <p v-if="packBusy" class="dim" data-testid="pack-busy">
                    导入中…
                  </p>
                </div>

                <div v-if="showRoleNew" class="add-form">
                  <label class="fld">
                    <span class="fld-lab">标识（小写字母/数字/连字符）</span>
                    <input
                      v-model="rf.id"
                      class="inp mono"
                      placeholder="finance"
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">显示名（留空取标识）</span>
                    <input v-model="rf.name" class="inp" placeholder="财务分析" />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">一句话定位（可选）</span>
                    <input
                      v-model="rf.description"
                      class="inp"
                      placeholder="记账与报表分析"
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">人设（身份句，一句话即可）</span>
                    <input
                      v-model="rf.identity"
                      class="inp"
                      placeholder="你是资深财务分析师，先对齐口径再给数。"
                    />
                  </label>
                  <label class="chk">
                    <input v-model="rf.exclude_global" type="checkbox" />
                    <span>不注入全局 AGENTS.md（非开发角色建议勾上）</span>
                  </label>
                  <p class="dim">
                    新建后会同时生成一份可改的 AGENTS.md
                    模板（空文件不会进系统提示）。
                  </p>
                  <div class="form-acts">
                    <button
                      class="ghost solid"
                      :disabled="busy || !rf.id"
                      @click="createRole"
                    >
                      创建角色
                    </button>
                  </div>
                </div>

                <p v-if="roleDangling" class="dim">
                  偏好里的当前角色
                  <span class="mono">{{ roleCurrent }}</span>
                  已经不存在了（角色目录被外部删除），本轮按基线运行（只有全局与项目指令）；点下面「默认（基线）」的「切换」即可切回来。
                </p>

                <div class="plist">
                  <!-- 合成行(不落盘):不启用角色也是一种确定的形态,给它一个名字与一键切回的落点 -->
                  <div class="prow scrow" :class="{ off: !roleBaseline }">
                    <div class="pmain">
                      <span class="sname">
                        默认（基线）
                        <span class="sstate mono">none</span>
                        <span v-if="roleBaseline" class="sstate ss-ok">当前</span>
                      </span>
                      <span class="psub"
                        >不注入角色设定：全局指令 + 项目规则</span
                      >
                      <span class="psub">技能：默认池（全部共享技能）</span>
                    </div>
                    <div class="sacts">
                      <button
                        v-if="!roleBaseline"
                        class="ghost"
                        data-tip="下一轮生效，不换会话"
                        @click="useRole('')"
                      >
                        切换
                      </button>
                    </div>
                  </div>
                  <p v-if="roleHint" class="dim" data-testid="role-hint">
                    还没挑过角色。下面
                    {{ roles.length }}
                    个预置角色按场景分组，<strong>挑一个</strong>就能换身份、工作规则与技能面；
                    不挑也能用（走默认：全局指令 +
                    全部技能）。选中后本提示不再出现。
                  </p>
                  <template v-for="sec in roleGroups" :key="sec.group">
                    <div
                      v-if="sec.items.length > 1 || sec.group !== '其它'"
                      class="srow-label"
                    >
                      {{ sec.group }}
                      <span class="dim">{{ sec.items.length }}</span>
                    </div>
                    <div
                      v-for="r in sec.items"
                      :key="r.id"
                      class="prow scrow"
                      :class="{ off: !roleIsCurrent(r) }"
                    >
                      <div class="pmain">
                        <span class="sname">
                          {{ r.name || r.id }}
                          <span class="sstate mono">{{ r.id }}</span>
                          <span v-if="roleIsCurrent(r)" class="sstate ss-ok"
                            >当前</span
                          >
                          <span v-if="r.seed" class="sstate">预置</span>
                        </span>
                        <span v-if="r.description" class="psub">{{
                          r.description
                        }}</span>
                        <span class="psub">
                          技能：{{
                            r.skills_set
                              ? r.skills?.length
                                ? r.skills.join("、")
                                : "未挂载"
                              : "默认池（全部库技能）"
                          }}
                          <span v-if="r.skills_inherit">+ 默认池</span>
                        </span>
                        <span v-if="r.exclude_global" class="psub"
                          >不注入全局 AGENTS.md</span
                        >
                        <span v-if="r.model || r.thinking" class="psub">
                          模型：{{ r.model || "跟随会话" }} · 思考：{{
                            r.thinking
                              ? THINK_LABEL[r.thinking] || r.thinking
                              : "跟随会话"
                          }}
                        </span>
                        <span v-if="r.approval || r.sandbox" class="psub"
                          >收紧：{{ tierBrief(r) }}</span
                        >
                      </div>
                      <div class="sacts">
                        <button
                          v-if="!roleIsCurrent(r)"
                          class="ghost"
                          data-tip="下一轮生效，不换会话"
                          @click="useRole(r.id)"
                        >
                          切换
                        </button>
                        <button class="ghost" @click="selectRole(r)">
                          {{ selRole === r.id ? "收起" : "编辑" }}
                        </button>
                        <button
                          class="ghost"
                          :disabled="packBusy"
                          data-tip="导成单个 zip（定义 + 工作规则 + 私有技能），可分享给别的 gah"
                          @click="exportRole(r)"
                        >
                          导出
                        </button>
                        <button
                          class="ghost danger-text"
                          data-tip="删除角色（需确认，移入回收站）"
                          @click="deleteRole(r)"
                        >
                          删除
                        </button>
                      </div>
                    </div>
                  </template>
                  <p v-if="!roles.length" class="dim">
                    还没有角色：点上方「＋ 新建」建一个；首次启动会释放 12
                    个预置角色（通用 / 工程 / 数据 / 写作 / 学习五组）。
                  </p>
                </div>

                <p v-if="roleCurrent" class="row acts">
                  <button
                    class="ghost"
                    data-tip="回到基线（不注入任何角色设定）"
                    @click="useRole('')"
                  >
                    停用当前角色
                  </button>
                </p>

                <!-- 详情编辑（列表不带正文，展开时才拉） -->
                <div v-if="roleDetail" class="role-detail">
                  <h3 class="h">编辑：{{ roleDetail.name || roleDetail.id }}</h3>
                  <div class="row">
                    <span class="lab-inline">显示名</span>
                    <input
                      class="inp grow"
                      :value="roleDetail.name"
                      @change="
                        (e) =>
                          (roleDetail!.name = (
                            e.target as HTMLInputElement
                          ).value)
                      "
                    />
                  </div>
                  <div class="row">
                    <span class="lab-inline">一句话定位</span>
                    <input
                      class="inp grow"
                      :value="roleDetail.description"
                      @change="
                        (e) =>
                          (roleDetail!.description = (
                            e.target as HTMLInputElement
                          ).value)
                      "
                    />
                  </div>
                  <div class="row">
                    <span class="lab-inline">人设</span>
                    <input
                      class="inp grow"
                      :value="roleDetail.identity"
                      @change="
                        (e) =>
                          (roleDetail!.identity = (
                            e.target as HTMLInputElement
                          ).value)
                      "
                    />
                  </div>
                  <label class="chk">
                    <!-- 选项型控件一律**只改本地草稿**,由下面的「保存定义」一次性写盘(2026-10-04 用户实测)。
                     此前这里是 @change="saveRoleDef(...)" = 选中即写盘,而同一区的文本框却要点保存 ——
                     两套语义并排,用户无法形成稳定预期;且选项里恰好是**权限档**。 -->
                    <input
                      type="checkbox"
                      :checked="!!roleDetail.exclude_global"
                      :disabled="roleSaving"
                      @change="
                        roleDetail!.exclude_global = (
                          $event.target as HTMLInputElement
                        ).checked
                      "
                    />
                    <span>不注入全局 AGENTS.md</span>
                  </label>
                  <!-- 角色携带模型/思考档(第八十六批):留空 = 跟随会话。
                   这两个输入框不是"建议值"而是真生效的值(每回合注入请求),故文案要写清。 -->
                  <div class="row">
                    <span class="lab-inline">模型</span>
                    <input
                      class="inp grow mono"
                      :value="roleDetail.model || ''"
                      :disabled="roleSaving"
                      placeholder="留空 = 跟随会话模型"
                      @change="
                        roleDetail!.model = (
                          $event.target as HTMLInputElement
                        ).value
                      "
                    />
                  </div>
                  <div class="row">
                    <span class="lab-inline">思考</span>
                    <div class="seg">
                      <button
                        class="seg-it"
                        :class="{ on: !roleDetail.thinking }"
                        data-tip="跟随会话思考档"
                        :disabled="roleSaving"
                        @click="roleDetail!.thinking = ''"
                      >
                        跟随会话
                      </button>
                      <button
                        v-for="t in THINK"
                        :key="t"
                        class="seg-it"
                        :class="{ on: roleDetail.thinking === t }"
                        :data-tip="'角色固定为 ' + THINK_LABEL[t]"
                        :disabled="roleSaving"
                        @click="roleDetail!.thinking = t"
                      >
                        {{ THINK_LABEL[t] }}
                      </button>
                    </div>
                  </div>
                  <p class="dim">
                    模型/思考档按角色生效：每回合的请求都按这里填的值走（会话档被它覆盖，停用角色后恢复）。
                    模型名没有任何 provider
                    能接时本轮会退回会话模型并提示（不会硬失败）。
                  </p>

                  <!-- 权限档(第九十二批):角色只能收紧,不能放宽 —— 值域由 SDK 定死,
                   后端对 open/full-access 直接拒(400),故这里只列更严的档。 -->
                  <h3 class="h">权限档（只能收紧）</h3>
                  <div class="row">
                    <span class="lab-inline">审批</span>
                    <select
                      class="sel grow"
                      :value="roleDetail.approval || ''"
                      :disabled="roleSaving"
                      @change="
                        roleDetail!.approval = (
                          $event.target as HTMLSelectElement
                        ).value
                      "
                    >
                      <option value="">跟随全局</option>
                      <option
                        v-for="a in ROLE_AP"
                        :key="'ra-' + a.v"
                        :value="a.v"
                      >
                        {{ a.label }}
                      </option>
                    </select>
                  </div>
                  <div class="row">
                    <span class="lab-inline">沙箱</span>
                    <select
                      class="sel grow"
                      :value="roleDetail.sandbox || ''"
                      :disabled="roleSaving"
                      @change="
                        roleDetail!.sandbox = (
                          $event.target as HTMLSelectElement
                        ).value
                      "
                    >
                      <option value="">跟随全局</option>
                      <option
                        v-for="s in ROLE_SB"
                        :key="'rs-' + s.v"
                        :value="s.v"
                      >
                        {{ s.label }}
                      </option>
                    </select>
                  </div>
                  <p v-if="roleTierNote" class="dim" data-testid="role-tier-note">
                    当前实际生效：{{ roleTierNote }}（角色收紧中）
                  </p>
                  <p class="dim">
                    实际档位取「全局档」与「角色档」里更严的那个；停用角色后回到全局档。
                    审批收紧到「严格」时不弹确认框，危险命令直接拒——子代理与定时任务同样按它跑。
                  </p>
                  <div class="row acts">
                    <button
                      class="ghost solid"
                      :disabled="busy || roleSaving || !roleDefDirty"
                      :data-tip="
                        roleDefDirty
                          ? '把上面这些改动写进角色定义'
                          : '没有未保存的改动'
                      "
                      @click="saveRoleDefNow()"
                    >
                      保存定义
                    </button>
                    <button
                      v-if="roleDefDirty"
                      class="ghost"
                      :disabled="roleSaving"
                      @click="revertRoleDef()"
                    >
                      放弃改动
                    </button>
                    <span v-if="roleDefDirty" class="dirty">未保存</span>
                  </div>

                  <label class="fld">
                    <span class="fld-lab">
                      工作规则（AGENTS.md）{{ byteLength(agentsDraft) }} /
                      {{ roleMax }} 字节
                      <span v-if="agentsDirty" class="dirty">未保存</span>
                      <span
                        v-if="byteLength(agentsDraft) > roleMax"
                        class="err-text"
                        >超出上限</span
                      >
                    </span>
                    <textarea
                      v-model="agentsDraft"
                      class="inp mono"
                      rows="8"
                      placeholder="写这个角色的做事规程（逐字进系统提示，越短越省）"
                    ></textarea>
                  </label>
                  <div class="form-acts">
                    <button
                      class="ghost solid"
                      :disabled="busy || !selRole"
                      @click="saveAgents"
                    >
                      保存工作规则
                    </button>
                    <button
                      v-if="agentsDirty"
                      class="ghost danger-text"
                      @click="discardAgents"
                    >
                      放弃修改
                    </button>
                  </div>

                  <div class="row">
                    <span class="lab-inline">改标识</span>
                    <input v-model="roleIDDraft" class="inp mono grow" />
                    <button
                      class="ghost"
                      :disabled="
                        roleIDDraft.trim() === roleDetail.id ||
                        !roleIDDraft.trim()
                      "
                      @click="renameRole"
                    >
                      改标识
                    </button>
                  </div>
                  <p class="dim">改标识 = 角色目录改名（当前角色会跟着改）。</p>

                  <!-- 技能挂载 -->
                  <h3 class="h">技能挂载</h3>
                  <div class="row">
                    <span class="lab-inline">挂载方式</span>
                    <div class="seg">
                      <button
                        class="seg-it"
                        :class="{ on: !roleDetail.skills_set }"
                        :disabled="roleSaving"
                        data-tip="不写 skills 键 = 用默认池（全部库技能）"
                        @click="useDefaultPool"
                      >
                        默认池
                      </button>
                      <button
                        class="seg-it"
                        :class="{ on: roleDetail.skills_set }"
                        :disabled="roleSaving"
                        data-tip="写了 skills 键 = 只挂勾选的"
                        @click="useReplacePool"
                      >
                        替换
                      </button>
                    </div>
                  </div>
                  <label class="chk">
                    <input
                      type="checkbox"
                      :checked="!!roleDetail.skills_inherit"
                      :disabled="roleSaving"
                      @change="
                        toggleInherit(($event.target as HTMLInputElement).checked)
                      "
                    />
                    <span>再并入默认池（替换之外额外挂上全部库技能）</span>
                  </label>
                  <p class="dim">
                    角色私有技能（roles/&lt;id&gt;/skills/）只增不减；同名技能按目录顺序首个生效，重名会在日志里告警。
                  </p>
                  <div v-if="!roleDetail.skills_set" class="dim">
                    当前为默认池，勾选任一项即切到「替换」。
                  </div>
                  <div class="m-list">
                    <template v-for="[g, list] in skillsByRole" :key="g">
                      <p class="dim">{{ groupLabel(g) }}</p>
                      <div
                        v-for="s in list"
                        :key="(s.role || '') + '/' + s.name"
                        class="m-item"
                      >
                        <label class="chk grow">
                          <input
                            type="checkbox"
                            :checked="mounted(roleDetail, s.name)"
                            :disabled="roleSaving"
                            @change="
                              toggleMount(
                                s.name,
                                ($event.target as HTMLInputElement).checked,
                              )
                            "
                          />
                          <span class="m-lab">{{ s.name }}</span>
                        </label>
                        <span v-if="s.description" class="dim grow">{{
                          s.description
                        }}</span>
                        <button
                          class="ghost"
                          data-tip="看/改 SKILL.md 原文"
                          @click="openSkill(s.name, s.role || '')"
                        >
                          原文
                        </button>
                        <button
                          v-if="!s.role"
                          class="ghost"
                          data-tip="导出为技能包(.zip,含 SKILL.md,可分享)"
                          @click="exportSkillPack(s.name)"
                        >
                          导出包
                        </button>
                        <button
                          class="ghost"
                          data-tip="改目录名 / 换归属库（会同步改角色挂载；目标同名会被拒）"
                          @click="openMove(s.name, s.role || '')"
                        >
                          改名/移动
                        </button>
                        <button
                          class="ghost danger-text"
                          data-tip="删除技能（需确认）"
                          @click="deleteSkill(s.name, s.role || '')"
                        >
                          删除
                        </button>
                      </div>
                    </template>
                    <p v-if="!roleLib.length" class="dim">
                      技能库为空：可在下方新建，或把技能放到
                      skills/&lt;名&gt;/SKILL.md。
                    </p>
                  </div>
                  <div v-if="staleMounts.length" class="m-list">
                    <p class="dim">
                      已失效挂载（技能已不存在）：保存其它改动不受影响，建议清掉。
                    </p>
                    <div
                      v-for="n in staleMounts"
                      :key="'stale-' + n"
                      class="m-item"
                    >
                      <span class="m-lab">{{ n }}</span>
                      <button
                        class="ghost danger-text"
                        :disabled="roleSaving"
                        data-tip="从挂载清单里移除这个名字"
                        @click="removeStaleMount(n)"
                      >
                        移除
                      </button>
                    </div>
                  </div>

                  <!-- 工具排除(第九十一批):默认全给,勾上 = 不给(模型看不见也调不动) -->
                  <h3 class="h">
                    工具
                    <button
                      class="link"
                      data-tip="默认全部工具可用;勾上即排除"
                      @click="toggleTools"
                    >
                      {{ toolsOpen ? "收起" : "配置" }}
                    </button>
                  </h3>
                  <p class="dim">
                    默认该角色可用全部工具。勾掉即排除：模型看不见也调不动(凭记忆调用会被显式拒绝)。
                    <span v-if="excludedCount"
                      >已排除 {{ excludedCount }} 项。</span
                    >
                    <button
                      v-if="excludedCount"
                      class="ghost"
                      :disabled="roleSaving"
                      data-tip="清空排除清单，恢复全部工具"
                      @click="restoreAllTools"
                    >
                      全部恢复
                    </button>
                  </p>
                  <div v-if="toolsOpen">
                    <div v-if="toolsErr" class="serr">{{ toolsErr }}</div>
                    <label class="fld">
                      <span class="fld-lab">筛选（名字/描述）</span>
                      <input
                        v-model="toolQuery"
                        class="inp"
                        placeholder="shell / file / mcp_"
                      />
                    </label>
                    <p v-if="toolsLoaded && !toolAll.length" class="dim">
                      没有已注册的工具。
                    </p>
                    <div v-if="toolShown.length" class="m-list">
                      <div
                        v-for="t in toolShown"
                        :key="'tool-' + t.name"
                        class="m-item"
                      >
                        <label class="chk grow">
                          <input
                            type="checkbox"
                            :checked="toolExcluded(t.name)"
                            :disabled="roleSaving"
                            @change="
                              toggleTool(
                                t.name,
                                ($event.target as HTMLInputElement).checked,
                              )
                            "
                          />
                          <span class="m-lab mono">{{ t.name }}</span>
                        </label>
                        <span v-if="t.description" class="dim grow">{{
                          t.description
                        }}</span>
                        <span v-if="toolExcluded(t.name)" class="dirty"
                          >已排除</span
                        >
                      </div>
                    </div>
                    <p v-else-if="toolsLoaded" class="dim">没有匹配的工具。</p>
                    <div v-if="staleTools.length" class="m-list">
                      <p class="dim">
                        已排除但当前不存在（插件卸载/改名后残留）：不报错，建议清掉。
                      </p>
                      <div
                        v-for="n in staleTools"
                        :key="'stale-tool-' + n"
                        class="m-item"
                      >
                        <span class="m-lab mono">{{ n }}</span>
                        <span class="dim grow">该工具当前不存在</span>
                        <button
                          class="ghost danger-text"
                          :disabled="roleSaving"
                          data-tip="从排除清单里移除这个名字"
                          @click="removeStaleTool(n)"
                        >
                          清除
                        </button>
                      </div>
                    </div>
                  </div>

                  <!-- 技能改名/跨库移动(第八十四批)：目录名即身份，frontmatter 的 name 由服务端同步改 -->
                  <div v-if="skMove" class="add-form">
                    <label class="fld">
                      <span class="fld-lab">
                        改名 / 移动:{{ skMove.name }}
                        <span class="dim"
                          >(目录名即身份,会同步改挂载它的角色)</span
                        >
                      </span>
                    </label>
                    <label class="fld">
                      <span class="fld-lab">新名称（小写字母/数字/._-）</span>
                      <input
                        v-model="skMove.toName"
                        class="inp mono"
                        :placeholder="skMove.name"
                      />
                    </label>
                    <label class="fld">
                      <span class="fld-lab">移到哪个库</span>
                      <select v-model="skMove.toRole" class="sel">
                        <option value="">共享技能库（所有角色可见）</option>
                        <option
                          v-for="r in roles"
                          :key="'mv-' + r.id"
                          :value="r.id"
                        >
                          角色私有 · {{ r.name || r.id }}
                        </option>
                      </select>
                    </label>
                    <div class="form-acts">
                      <button
                        class="ghost solid"
                        :disabled="busy || !skMoveDirty"
                        @click="submitMove"
                      >
                        确认改名/移动
                      </button>
                      <button class="ghost" @click="skMove = null">取消</button>
                    </div>
                  </div>

                  <!-- 技能原文编辑（覆盖写） -->
                  <div v-if="skEdit" class="add-form">
                    <label class="fld">
                      <span class="fld-lab">
                        编辑 {{ skEdit.name }} 的 SKILL.md（原文，保存即覆盖）
                        <span v-if="skDirty" class="dirty">未保存</span>
                      </span>
                      <textarea
                        v-model="skEdit.content"
                        class="inp mono"
                        rows="10"
                      ></textarea>
                    </label>
                    <div class="form-acts">
                      <button
                        class="ghost solid"
                        :disabled="busy"
                        @click="saveSkill"
                      >
                        保存技能
                      </button>
                      <button
                        v-if="skDirty"
                        class="ghost danger-text"
                        @click="discardSkill"
                      >
                        放弃修改
                      </button>
                      <button class="ghost" @click="skEdit = null">收起</button>
                    </div>
                  </div>
                </div>

                <!-- 新建技能（共享库 / 角色私有） -->
                <h3 class="h">
                  技能库
                  <button
                    class="link"
                    data-tip="新建一个技能"
                    @click="showSkillNew = !showSkillNew"
                  >
                    {{ showSkillNew ? "收起" : "＋ 新建技能" }}
                  </button>
                  <!-- 技能包(第一百零六批):只管**共享库**;角色私有技能随角色包导出 -->
                  <button
                    class="link"
                    data-tip="导入一个 gah 技能包(.zip,内含单个 SKILL.md)"
                    @click="showSkPackImport = !showSkPackImport"
                  >
                    {{ showSkPackImport ? "收起" : "＋ 导入技能包" }}
                  </button>
                </h3>
                <div v-if="showSkPackImport" class="add-form">
                  <p class="dim">
                    包里只有那个技能的 <code>SKILL.md</code>(frontmatter + 正文)——
                    导入端
                    <strong>不执行任何脚本</strong
                    >。同名已存在时后端会先拒，再问你要不要覆盖(旧份进回收站)。
                  </p>
                  <label class="fld">
                    <span class="fld-lab">技能包文件(.zip)</span>
                    <input
                      class="inp"
                      type="file"
                      accept=".zip,application/zip"
                      :disabled="skPackBusy"
                      data-testid="skpack-file"
                      @change="onSkPackPick"
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab"
                      >导入为（可选：留空 = 用包里的名称）</span
                    >
                    <input
                      v-model="skPackAs"
                      class="inp mono"
                      placeholder="weekly-report-copy"
                      :disabled="skPackBusy"
                    />
                  </label>
                  <p v-if="skPackBusy" class="dim">导入中…</p>
                </div>
                <div v-if="skErr" class="serr">{{ skErr }}</div>
                <div v-if="showSkillNew" class="add-form">
                  <label class="fld">
                    <span class="fld-lab">名称（小写字母/数字/._-）</span>
                    <input
                      v-model="skNew.name"
                      class="inp mono"
                      placeholder="weekly-report"
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">什么时候用（触发词，逗号分隔）</span>
                    <input
                      v-model="skNew.triggers"
                      class="inp"
                      placeholder="写周报, 汇总进度"
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">做什么（一句话）</span>
                    <input
                      v-model="skNew.description"
                      class="inp"
                      placeholder="按项目汇总本周进展与风险"
                    />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">正文（步骤）</span>
                    <textarea
                      v-model="skNew.body"
                      class="inp"
                      rows="4"
                      placeholder="1. 读本周提交…"
                    ></textarea>
                  </label>
                  <label class="fld">
                    <span class="fld-lab">放哪里</span>
                    <select v-model="skNew.role" class="sel">
                      <option value="">共享技能库（所有角色可见）</option>
                      <option v-for="r in roles" :key="r.id" :value="r.id">
                        角色私有 · {{ r.name || r.id }}
                      </option>
                    </select>
                  </label>
                  <div class="form-acts">
                    <button
                      class="ghost solid"
                      :disabled="busy || !skNew.name"
                      @click="createSkill"
                    >
                      创建技能
                    </button>
                  </div>
                </div>

                <!-- 回收站(第八十三批):删除的角色/技能都能从这里恢复 -->
                <h3 class="h">
                  回收站
                  <button
                    class="link"
                    data-tip="删除的角色与技能(移入 .trash,可恢复)"
                    @click="toggleTrash"
                  >
                    {{ trashOpen ? "收起" : "查看" }}
                  </button>
                </h3>
                <p v-if="!trashOpen" class="dim">
                  删除的角色与技能不会真删:移入
                  <span class="mono">.trash/</span>(各自保留最近 20
                  份),在这里可以恢复。
                </p>
                <div v-if="trashOpen">
                  <div v-if="trashErr" class="serr">{{ trashErr }}</div>
                  <template v-if="trashReady">
                    <p v-if="!trashCount" class="dim">回收站是空的。</p>
                    <template v-else>
                      <div v-if="trashRoles.length" class="m-list">
                        <p class="dim">角色</p>
                        <div
                          v-for="t in trashRoles"
                          :key="'tr-' + t.name"
                          class="m-item"
                        >
                          <span class="m-lab">{{ t.id || t.name }}</span>
                          <span v-if="!t.id" class="dim grow"
                            >目录名不合约定,无法还原</span
                          >
                          <span v-else-if="t.deleted_at" class="dim grow"
                            >删除于 {{ fmtTrashTime(t.deleted_at) }}</span
                          >
                          <button
                            v-if="t.id"
                            class="ghost"
                            data-tip="移回 roles/&lt;id&gt;/(同名已存在会被拒)"
                            @click="restoreTrash('role', t)"
                          >
                            恢复
                          </button>
                        </div>
                      </div>
                      <div v-if="trashSkills.length" class="m-list">
                        <p class="dim">技能</p>
                        <div
                          v-for="t in trashSkills"
                          :key="'ts-' + t.role + '/' + t.name"
                          class="m-item"
                        >
                          <span class="m-lab">{{ t.skill || t.name }}</span>
                          <span class="dim grow"
                            >{{ t.role ? "角色私有 · " + t.role : "共享技能库"
                            }}<template v-if="t.deleted_at">
                              · 删除于 {{ fmtTrashTime(t.deleted_at) }}</template
                            ></span
                          >
                          <span v-if="!t.skill" class="dim"
                            >目录名不合约定,无法还原</span
                          >
                          <button
                            v-else
                            class="ghost"
                            data-tip="移回原技能库(同名已存在会被拒)"
                            @click="restoreTrash('skill', t)"
                          >
                            恢复
                          </button>
                        </div>
                      </div>
                    </template>
                  </template>
                </div>
              </section>
</template>

<style scoped src="../settings/settings-panel.css"></style>
