// 会话流 markdown 渲染的**模块级单例**取件器(批二)。
//
// 为何单列一个文件而不是直接在 DocText.vue 里 new:`<script setup>` 的顶层代码
// **每个组件实例都跑一次** —— 在那里建 client 就等于「每个 DocText 一个缓存一个闸」,
// 共享彻底失效(这正是批二要解决的东西本身)。
//
// 缓存的**边界**(实测,别误以为它能跨刷新):内存级,同一页面上下文内有效。
//   ✓ 命中:视图往返(stream ↔ 轨迹/看板/变更,StreamView 卸载重挂,几百个 DocText 全重建)
//            —— 实测往返一次额外请求 0(无缓存时应约 200);会话页签来回;上滚分页再滚回。
//   ✗ 不命中:整页刷新(F5)后 JS 上下文重建,单例连同缓存一起没了。
//     「刷新后秒开」要靠这个得走服务端缓存,那是另一件事,本批不做。
import { api } from './api'
import { DocRenderClient } from './docfetch'

// 容量 256 条:会话流窗口上限 400 条消息(约一半是 assistant 文本 ⇒ ~200 条),
// 留一点余量让「切会话再切回来」也命中。并发闸 4:够压满 CPU,又不至于把首屏拖成串行。
export const docRenderClient = new DocRenderClient(async (t) => {
  const v = await api.docRender(t)
  return { blocks: v.blocks ?? [] }
})