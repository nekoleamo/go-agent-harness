<script setup lang="ts">
// 由 SettingsPanel.vue 拆出的分区(D1)。**状态仍在父层** —— 这里只是同一批 ref / 常量的
// 使用者(见 ../settings/ctx.ts 的说明)。样式与父面板同源(同一份 CSS 文件),拆分不改变外观。
import { inject } from "vue";
import { SETTINGS_CTX, type SettingsCtx } from "../settings/ctx";

const { DOW_ORDER, DOW_ZH, EVERY_HOUR, EVERY_MIN, HOURS, LUNAR_FESTIVALS, LUNAR_MONTHS, MINUTES, MONTH_DAYS, NTHS, PRESETS, REPEATS, applyScheduleNL, busy, cancelSchedEdit, clearRawExpression, costHint, deleteSchedule, doScheduleRun, editSchedule, fmtAbs, fmtNextRun, lunarDayText, nthDow, openSchedAdd, previewLines, refreshSchedulePreview, saveSchedule, schedDone, schedEditing, schedErr, schedForm, schedNL, schedNLBusy, schedNLNote, schedRaw, schedRawNote, schedRawOpen, schedReady, schedRowLabel, schedTypeChanged, schedView, schedules, setLunarFestival, sf, showSchedAdd, skipHint, statusLabel, structToCron, todayStr, toggleDow, toggleSchedule, useRawExpression, useSchedPreset } = inject(SETTINGS_CTX) as SettingsCtx;
// 同上:模板里 REPEATS.find((r) => ...) 的 r 需要具体类型。
const repeatList = REPEATS as { kind: string; hint?: string }[];
</script>

<template>
  <section v-if="schedReady" data-sec="schedule" class="sec">
                <h3 class="h">
                  计划
                  <button
                    class="link"
                    data-tip="新增定时计划"
                    @click="openSchedAdd()"
                  >
                    {{ showSchedAdd && !schedEditing ? "收起" : "＋ 新增" }}
                  </button>
                </h3>
                <!-- 无人值守行为明示(方案 W4 要求:计划详情里说清楚会发生什么) -->
                <p class="dim">
                  到点自动把描述发给模型跑一轮(走与手动输入完全相同的回合入口)。无人值守运行:需审批的动作一律拒绝(没人在场回答确认),沙箱档位沿用当前设置。
                </p>
                <div v-if="schedErr" class="serr">{{ schedErr }}</div>

                <div v-if="showSchedAdd" class="add-form">
                  <p v-if="schedEditing" class="dim">
                    正在修改已有计划(保存会覆盖原来的排期)
                  </p>

                  <label class="fld">
                    <span class="fld-lab">多久跑一次</span>
                    <select
                      v-model="schedForm.repeat"
                      class="sel"
                      @change="schedTypeChanged()"
                    >
                      <option v-for="r in REPEATS" :key="r.kind" :value="r.kind">
                        {{ r.label }}
                      </option>
                    </select>
                  </label>
                  <p class="dim sched-note">
                    {{ repeatList.find((r) => r.kind === schedForm.repeat)?.hint }}
                  </p>

                  <div v-if="schedForm.repeat === 'weekly'" class="chip-row">
                    <button
                      v-for="d in DOW_ORDER"
                      :key="d"
                      class="chip"
                      :class="{ on: schedForm.dows.includes(d) }"
                      type="button"
                      @click="toggleDow(d)"
                    >
                      {{ DOW_ZH[d] }}
                    </button>
                  </div>

                  <div
                    v-else-if="schedForm.repeat === 'monthly_nth'"
                    class="fld-row"
                  >
                    <label class="fld">
                      <span class="fld-lab">第几个</span>
                      <select
                        v-model.number="schedForm.nth"
                        class="sel"
                        @change="refreshSchedulePreview()"
                      >
                        <option v-for="n in NTHS" :key="n.v" :value="n.v">
                          {{ n.t }}
                        </option>
                      </select>
                    </label>
                    <label class="fld">
                      <span class="fld-lab">周几</span>
                      <select v-model="nthDow" class="sel">
                        <option v-for="d in DOW_ORDER" :key="d" :value="d">
                          {{ DOW_ZH[d] }}
                        </option>
                      </select>
                    </label>
                  </div>

                  <!-- 每年某天(公历):cron 能表达,后端反解得出来 -->
                  <div
                    v-else-if="schedForm.repeat === 'annual_date'"
                    class="fld-row"
                  >
                    <label class="fld">
                      <span class="fld-lab">几月</span>
                      <select
                        v-model.number="schedForm.month"
                        class="sel"
                        @change="refreshSchedulePreview()"
                      >
                        <option v-for="m in 12" :key="m" :value="m">
                          {{ m }} 月
                        </option>
                      </select>
                    </label>
                    <label class="fld">
                      <span class="fld-lab">几号</span>
                      <select
                        v-model.number="schedForm.day"
                        class="sel"
                        @change="refreshSchedulePreview()"
                      >
                        <option v-for="d in MONTH_DAYS" :key="d" :value="d">
                          {{ d }} 号
                        </option>
                      </select>
                    </label>
                  </div>

                  <!-- 农历节日/农历某天:日期在公历上每年都在变,cron 表达不了 -->
                  <template v-else-if="schedForm.repeat === 'lunar_annual'">
                    <label class="fld">
                      <span class="fld-lab">哪个农历日子</span>
                      <select
                        :value="schedForm.lunarFestival"
                        class="sel"
                        @change="
                          setLunarFestival(
                            ($event.target as HTMLSelectElement).value,
                          )
                        "
                      >
                        <option
                          v-for="f in LUNAR_FESTIVALS"
                          :key="f.name"
                          :value="f.name"
                        >
                          {{ f.name }}
                        </option>
                        <option value="">自定义农历日期…</option>
                      </select>
                    </label>
                    <div v-if="!schedForm.lunarFestival" class="fld-row">
                      <label class="fld">
                        <span class="fld-lab">农历月</span>
                        <select
                          v-model.number="schedForm.month"
                          class="sel"
                          @change="refreshSchedulePreview()"
                        >
                          <option
                            v-for="(m, i) in LUNAR_MONTHS"
                            :key="m"
                            :value="i + 1"
                          >
                            {{ m }}
                          </option>
                        </select>
                      </label>
                      <label class="fld">
                        <span class="fld-lab">农历日</span>
                        <select
                          v-model.number="schedForm.day"
                          class="sel"
                          @change="refreshSchedulePreview()"
                        >
                          <option :value="0">最后一天(除夕这类)</option>
                          <option v-for="d in 30" :key="d" :value="d">
                            {{ lunarDayText(d) }}
                          </option>
                        </select>
                      </label>
                    </div>
                    <p class="dim sched-note">
                      农历日期每年在公历上都不一样,所以预览里显示的正是它实际会跑的那几天。
                    </p>
                  </template>

                  <label
                    v-else-if="schedForm.repeat === 'monthly_day'"
                    class="fld"
                  >
                    <span class="fld-lab">几号</span>
                    <select
                      v-model.number="schedForm.day"
                      class="sel"
                      @change="refreshSchedulePreview()"
                    >
                      <option v-for="d in MONTH_DAYS" :key="d" :value="d">
                        {{ d }} 号
                      </option>
                    </select>
                  </label>

                  <div v-else-if="schedForm.repeat === 'once'" class="fld-row">
                    <label class="fld">
                      <span class="fld-lab">哪一天(到点跑一轮就结束)</span>
                      <input
                        v-model="schedForm.onceDate"
                        class="inp"
                        type="date"
                        :min="todayStr()"
                        @change="refreshSchedulePreview()"
                      />
                    </label>
                    <label class="fld">
                      <span class="fld-lab">几点</span>
                      <select
                        v-model.number="schedForm.hour"
                        class="sel"
                        @change="refreshSchedulePreview()"
                      >
                        <option v-for="h in HOURS" :key="h" :value="h">
                          {{ String(h).padStart(2, "0") }} 点
                        </option>
                      </select>
                    </label>
                    <label class="fld">
                      <span class="fld-lab">分</span>
                      <select
                        v-model.number="schedForm.minute"
                        class="sel"
                        @change="refreshSchedulePreview()"
                      >
                        <option v-for="m in MINUTES" :key="m" :value="m">
                          {{ String(m).padStart(2, "0") }} 分
                        </option>
                      </select>
                    </label>
                  </div>

                  <label
                    v-else-if="schedForm.repeat === 'every_n_min'"
                    class="fld"
                  >
                    <span class="fld-lab">每隔几分钟</span>
                    <select
                      v-model.number="schedForm.every"
                      class="sel"
                      @change="refreshSchedulePreview()"
                    >
                      <option v-for="n in EVERY_MIN" :key="n" :value="n">
                        {{ n }} 分钟
                      </option>
                    </select>
                  </label>

                  <div
                    v-else-if="schedForm.repeat === 'every_n_hour'"
                    class="fld-row"
                  >
                    <label class="fld">
                      <span class="fld-lab">每隔几小时</span>
                      <select
                        v-model.number="schedForm.every"
                        class="sel"
                        @change="refreshSchedulePreview()"
                      >
                        <option v-for="n in EVERY_HOUR" :key="n" :value="n">
                          {{ n }} 小时
                        </option>
                      </select>
                    </label>
                    <label class="fld">
                      <span class="fld-lab">第几分钟</span>
                      <select
                        v-model.number="schedForm.minute"
                        class="sel"
                        @change="refreshSchedulePreview()"
                      >
                        <option v-for="m in MINUTES" :key="m" :value="m">
                          {{ String(m).padStart(2, "0") }} 分
                        </option>
                      </select>
                    </label>
                  </div>

                  <!-- 时刻:不用原生 type=time(外观与 12/24 小时制随系统区域变,跨端不一致);
                   每小时档只给分钟(小时由「每小时」本身承担) -->
                  <div v-if="schedForm.repeat === 'hourly'" class="fld">
                    <span class="fld-lab">第几分钟</span>
                    <select
                      v-model.number="schedForm.minute"
                      class="sel"
                      @change="refreshSchedulePreview()"
                    >
                      <option v-for="m in MINUTES" :key="m" :value="m">
                        {{ String(m).padStart(2, "0") }} 分
                      </option>
                    </select>
                  </div>
                  <div
                    v-else-if="schedForm.repeat !== 'every_n_min'"
                    class="fld-row"
                  >
                    <label v-if="schedForm.repeat !== 'every_n_hour'" class="fld">
                      <span class="fld-lab">几点</span>
                      <select
                        v-model.number="schedForm.hour"
                        class="sel"
                        @change="refreshSchedulePreview()"
                      >
                        <option v-for="h in HOURS" :key="h" :value="h">
                          {{ String(h).padStart(2, "0") }} 点
                        </option>
                      </select>
                    </label>
                    <label class="fld">
                      <span class="fld-lab">分</span>
                      <select
                        v-model.number="schedForm.minute"
                        class="sel"
                        @change="refreshSchedulePreview()"
                      >
                        <option v-for="m in MINUTES" :key="m" :value="m">
                          {{ String(m).padStart(2, "0") }} 分
                        </option>
                      </select>
                    </label>
                  </div>

                  <p v-if="skipHint(schedForm)" class="dim sched-note">
                    {{ skipHint(schedForm) }}
                  </p>
                  <p v-if="costHint(schedForm)" class="dim sched-note">
                    {{ costHint(schedForm) }}
                  </p>

                  <!-- 预览:不会 cron 的用户唯一的验收手段 -->
                  <div v-if="schedView" class="sched-preview">
                    <span class="sched-view">{{
                      schedView.label || "自定义排期"
                    }}</span>
                    <span
                      v-for="(l, i) in previewLines(schedView.next_runs)"
                      :key="i"
                      class="dim"
                      >{{ l }}</span
                    >
                  </div>
                  <p v-else-if="structToCron(schedForm)" class="dim sched-note">
                    正在算下次触发…
                  </p>

                  <div class="chip-row">
                    <button
                      v-for="p in PRESETS"
                      :key="p.label"
                      class="chip"
                      type="button"
                      @click="useSchedPreset(p.form)"
                    >
                      {{ p.label }}
                    </button>
                  </div>

                  <div class="fld-row">
                    <input
                      v-model="schedNL"
                      class="inp grow"
                      placeholder="或直接说:每天早上8点 / 每周一三五 9 点 / 月底最后一天 17:00"
                      @keyup.enter="applyScheduleNL()"
                    />
                    <button
                      class="ghost"
                      :disabled="schedNLBusy"
                      @click="applyScheduleNL()"
                    >
                      按这句话排
                    </button>
                  </div>
                  <p v-if="schedNLNote" class="dim sched-note">
                    {{ schedNLNote }}
                  </p>

                  <!-- 自定义表达式(高级):界面上**唯一**出现 cron 的地方。
                       它只服务一个真实用例 —— 用户从教程/AI 那里抄来一条表达式,
                       总得有地方粘贴。能反解就提示改用选择器,反解不出也允许原样保存。 -->
                  <div class="sched-adv">
                    <button
                      v-if="!schedRawOpen"
                      class="link"
                      type="button"
                      @click="useRawExpression()"
                    >
                      从别处粘贴排期表达式…
                    </button>
                    <template v-else>
                      <label class="fld">
                        <span class="fld-lab">
                          自定义表达式(分 时 日 月 周;只建议粘贴你确切知道含义的)
                        </span>
                        <input
                          v-model="schedRaw"
                          class="inp mono"
                          placeholder="0 9 * * 1-5"
                          @input="refreshSchedulePreview()"
                        />
                      </label>
                      <div class="fld-row">
                        <button
                          class="ghost"
                          type="button"
                          @click="clearRawExpression()"
                        >
                          改用选择器
                        </button>
                        <span class="dim sched-note">{{ schedRawNote }}</span>
                      </div>
                    </template>
                  </div>

                  <label class="fld">
                    <span class="fld-lab">名称(可选,留空取描述开头)</span>
                    <input v-model="sf.name" class="inp" placeholder="每日对账" />
                  </label>
                  <label class="fld">
                    <span class="fld-lab">到点做什么(作为输入发给模型)</span>
                    <textarea
                      v-model="sf.prompt"
                      class="inp"
                      rows="2"
                      placeholder="把昨天的订单导出成对账表"
                    ></textarea>
                  </label>
                  <div class="form-acts">
                    <button
                      class="ghost solid"
                      :disabled="busy"
                      @click="saveSchedule"
                    >
                      {{ schedEditing ? "保存修改" : "保存计划" }}
                    </button>
                    <button
                      v-if="schedEditing"
                      class="ghost"
                      @click="cancelSchedEdit()"
                    >
                      取消
                    </button>
                  </div>
                </div>

                <div class="plist">
                  <div
                    v-for="s in schedules"
                    :key="s.id"
                    class="prow scrow"
                    :class="{ off: !s.enabled }"
                  >
                    <div class="pmain">
                      <span class="sname">
                        {{ s.name }}
                        <span
                          class="sstate"
                          :class="'ss-' + (s.last_status || 'none')"
                          >{{ statusLabel(s.last_status) }}</span
                        >
                      </span>
                      <span class="psub">{{ schedRowLabel(s) }}</span>
                      <span class="psub"
                        >下次:{{
                          s.enabled
                            ? fmtNextRun(s.next_run)
                            : s.once
                              ? "已于 " + fmtAbs(s.last_run_at) + " 执行"
                              : "已停用"
                        }}</span
                      >
                      <span v-if="s.last_run_at" class="psub"
                        >上次:{{ fmtAbs(s.last_run_at) }}</span
                      >
                      <span
                        v-if="s.last_status === 'failed' && s.last_error"
                        class="psub err-text"
                        >{{ s.last_error }}</span
                      >
                    </div>
                    <div class="sacts">
                      <button
                        v-if="!schedDone(s)"
                        class="ghost"
                        data-tip="立即跑一次"
                        :disabled="!s.enabled"
                        @click="doScheduleRun(s.id)"
                      >
                        {{ s.once ? "现在跑" : "运行" }}
                      </button>
                      <button
                        v-if="!schedDone(s)"
                        class="ghost"
                        data-tip="修改排期与描述"
                        @click="editSchedule(s)"
                      >
                        编辑
                      </button>
                      <button
                        v-if="!schedDone(s)"
                        class="ghost"
                        data-tip="启用或停用"
                        @click="toggleSchedule(s)"
                      >
                        {{ s.enabled ? "停用" : "启用" }}
                      </button>
                      <button
                        class="ghost danger-text"
                        data-tip="删除计划(需确认)"
                        @click="deleteSchedule(s)"
                      >
                        删除
                      </button>
                    </div>
                  </div>
                  <p v-if="!schedules.length" class="dim">
                    还没有计划:点上方「＋ 新增」选一个时间,或直接说一句「每天 8
                    点整理昨日订单」。
                  </p>
                </div>
              </section>
</template>

