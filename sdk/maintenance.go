// 维护类操作的**空闲闸门**(第一百六十七批)。
//
// 借鉴 Octop 的 memory slim 纪律(它把"等当前任务结束 → 备份 → 压缩 → 恢复"写成流程,
// 且 120s 内等不到空闲窗口就**失败退出,不硬闯**)。gah 的运维动作大多天然安全
// (压缩只追加摘要事件、备份恢复前自动先备份当前状态),唯一真缺口是**回合运行中**做
// 会改变模型视线的操作:压缩会在模型本轮还没看完的历史上动刀,下一步请求看到的投影
// 就和上一步不是同一份东西。
//
// 纪律三条(写在这里,后来加运维动作时照抄):
//   - 先问,不硬闯:有运行中的回合就**明说拒绝**,不排队、不静默延后(用户以为生效了);
//   - 拒绝要给**出口**:说清等什么(回合结束)或按什么(/stop);
//   - 未装配不算拒绝:没有回合控制(嵌入/单测/极简宿主)时照旧执行,不假装"忙"。
package sdk

// MaintenanceBlocked 现在是否该拒绝一次维护类操作(压缩/换库/重建索引)。
//
// 返回 (可展示的原因, 是否拒绝)。未装配 ctx.turnControl 或无运行回合 ⇒ (\"\", false)。
func MaintenanceBlocked(c Ctx) (string, bool) {
	if c == nil {
		return "", false
	}
	var tc TurnControl
	if err := c.Inject("ctx.turnControl", &tc); err != nil || tc == nil {
		return "", false
	}
	if !tc.Running() {
		return "", false
	}
	return "回合进行中:先等这一轮结束(或 /stop)。这个操作会改写模型本轮看到的上下文,不在回合中途做。", true
}
