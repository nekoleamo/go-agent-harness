// 整体备份服务(ctx.backup):GAH_HOME 单根数据整体打包/列出/恢复(M18)。
// 备份内容:config(含密钥)/plugins/sessions/env.sh/偏好/记忆等全部子目录,
// 排除 backups/ 自身(防递归膨胀);tar.gz 确定性归档(重跑无 diff),时间戳命名。
package sdk

// BackupInfo 一份备份的视图(列表/恢复选择用)。
type BackupInfo struct {
	Name string // 归档名(如 gah-backup-20060102-150405.tar.gz),相对备份目录
	Size int64  // 归档字节数
	Time int64  // 归档时间戳(Unix;0 = 未知)
}

// BackupService 整体备份服务:备份/列出/恢复。
// 实现(host-backup 插件)负责:默认备份目录 = $GAH_HOME/backups/(排除自身),
// 支持外部 dest 路径;恢复前自动先备份当前状态(安全默认),覆盖即执行(调用方负责二次确认)。
type BackupService interface {
	// Backup 打包当前数据为时间戳命名归档。dest 为空 = 默认备份目录($GAH_HOME/backups/)。
	// 返回归档相对名(默认目录)或绝对路径(dest 外部路径)。
	Backup(dest string) (string, error)
	// List 列出默认备份目录中的归档(按时间倒序,最新在前)。
	List() []BackupInfo
	// Restore 从默认备份目录恢复指定归档(覆盖当前数据)。
	// 调用方必须先确认;实现内部先自动备份当前状态(安全默认)。
	Restore(name string) error
}
