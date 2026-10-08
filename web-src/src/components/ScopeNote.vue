<script setup lang="ts">
// 作用域声明条(第一百三十四批 · 设置作用域收敛)。
//
// 为什么需要它:设置面板的模型段与推理段里那三个控件,原先**改的是当前页签**
// (api.control 不传 session 就默认绑本窗口会话),而段标题只有「模型」「推理」两字
// —— 零作用域提示。用户点一下改模型,合理理解是「改全局默认」,实际只动了这个页签。
//
// 现在把两侧都说出口:控件改**全局默认**,下方「本会话在用 X」显示这个会话的实际值。
//
// 为什么不做成「当前作用域切换器」:物理上混在一起时,用户第一次进来仍不知道默认是哪边,
// 只是把「看不出来」换成「看错」。作用域与对象一一对应才彻底 —— 改全局来设置,
// 改这个页签去右上角的「当前会话设置」。
// 不接返回值:模板里直接用 prop 名(setup 的 props 自动解包),接一个没人引用的变量
// 会被 vue-tsc 报 TS6133。
defineProps<{ what: string }>()
</script>

<template>
  <p class="scope-note" role="note">
    <span class="scope-k">作用域</span>
    下面的<b>{{ what }}</b>控件改的是<b>全局默认</b>（新会话与跟随全局的会话用它）；本会话实际在用的值见下方标注。
  </p>
</template>

<style scoped>
.scope-note {
  margin: 0 0 8px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--fg-faint);
}
.scope-k {
  display: inline-block;
  margin-right: 6px;
  padding: 0 5px;
  border: 1px solid var(--line);
  border-radius: 4px;
  font-size: 11px;
  color: var(--fg-dim);
}
.scope-note b {
  color: var(--fg-dim);
  font-weight: 600;
}
</style>