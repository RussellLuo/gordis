# 来自进程插件的事件

[English](README.md) | 中文

本示例在子进程中运行一个普通的 Local-first Notice publisher Plugin。可选的进程边界集中在
`notice/proc`；Plugin ID、类型化 Topic、publisher 和 Local observer 均与 Local 模式相同。

## 运行

在仓库根目录执行：

```sh
go build -o /tmp/gordis-process-events-host ./examples/process-events
go build -o /tmp/gordis-process-events-plugin ./examples/process-events/cmd/notice-publisher
/tmp/gordis-process-events-host /tmp/gordis-process-events-plugin
```

预期输出：

```text
received notice: hello
```

## 实现原理

`notice/plugin.go` 定义普通的 publisher Plugin 及其类型化 Topic。Host 挂载
`events.Plugin`、Local observer 和 `noticeproc.New` 返回的 Process prototype。挂载 publisher
时，adapter 启动 child executable，`noticeproc.Serve` 在子进程中运行同一个 Plugin；其
`Scope.AfterReady` callback 仅在 generation 和事件桥均进入 Ready 后发布。

两个进程端点使用 `notice/proc` 中的同一个 binding。它用稳定的 wire ID 将共享类型化 Topic
绑定到 JSON codec，并允许 `EventPublish`，即允许子进程向 Host EventBus 发布。如果事件绑定
缺失或不匹配，进程握手会在 remote Plugin 激活前拒绝连接。

Host observer 不需要任何进程专用代码，而是通过普通的 `events.Bind(scope).On` API 订阅。
装配时改为登记 `new(notice.Plugin)` 即可切换到 Local 模式，`InstanceSpec`、Topic、publisher
和 observer 均无需改变。`run` 返回时，`Host.Shutdown` 会停止并回收子进程。

Local 发布见 [events](../events/README.zh.md)；由进程支撑的类型化 Service 见
[process](../process/README.zh.md)；完整联邦契约见
[进程协议](../../docs/process-protocol.zh.md#event-federation)。
