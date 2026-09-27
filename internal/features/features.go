package features

// Memory 暂停整个记忆功能；保留实现和数据，恢复前需要重新验证。
// 不读取数据库开关，避免旧安装的 enabled=true 继续启动后台任务。
const Memory = false
