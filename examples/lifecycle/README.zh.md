# 由生命周期管理的资源

[English](README.md) | 中文

本示例为一个 Scope 添加受管的后台任务、停止回调、执行中的请求租约和最终资源清理，
展示 shutdown 如何在等待已接纳工作前先关闭新请求入口。

## 运行

在仓库根目录执行：

```sh
go run ./examples/lifecycle
```

预期输出：

```text
entrance closed
task stopped
new request rejected
stop waits for the request
request finished
resource released
stop complete
```

## 实现原理

- `OnStop` 关闭公开入口。
- `Go` 管理一个在 Scope context 取消时退出的任务。
- `Acquire` 为一个已接纳请求持有租约，并在 shutdown 开始后拒绝新请求。
- `Defer` 在任务和租约排空后释放最终资源。

示例在调用 `Instance.Unmount` 时保持一个租约，验证新工作会被拒绝，随后释放请求，
使实例清理得以完成。`Host.Shutdown` 仍负责 Host 级的最终拆除。

完整程序见 [main.go](main.go)；完整 shutdown 顺序见[生命周期](../../docs/lifecycle.zh.md)。
