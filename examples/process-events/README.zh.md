# 来自进程插件的事件

[English](README.md) | 中文

本示例挂载一个独立构建的进程插件，由它向 Host EventBus 发布类型化 Topic。
Local 插件通过与 Local publisher 相同的 `events.Bind(scope).On` API 接收该事件。

## 运行

在仓库根目录执行：

```sh
go build -o /tmp/gordis-process-events-host ./examples/process-events
go build -o /tmp/gordis-process-events-plugin ./examples/process-events/plugin
/tmp/gordis-process-events-host /tmp/gordis-process-events-plugin
```

预期输出：

```text
received from process: hello
plugin process reaped
```

## 实现原理

Host 首先挂载 `events.Plugin` 和一个 Local observer，然后挂载用于 `remote-publisher` 的
`processbridge.Adapter`。remote plugin 使用 `Scope.AfterReady`，只有当其 generation 和事件桥
都进入 Ready 后才发布。

两个进程端点都注册 `wire.Notice`。它使用稳定的 wire ID 将共享类型化 Topic 绑定到
JSON codec，并允许 `EventPublish`，即允许进程向 Host Bus 发布。如果事件绑定缺失或不匹配，
进程握手会在 remote plugin 激活前拒绝连接。

Host observer 不需要任何进程专用代码。bridge 会经由 Host EventBus 路由远程发布，
因此普通 Topic 路由和 Scope 生命周期规则仍然适用。Host shutdown 随后停止并完整回收子进程。

关键文件：

- [main.go](main.go)：Host 装配和 Local subscriber。
- [plugin/main.go](plugin/main.go)：remote publisher。
- [contract/contract.go](contract/contract.go)：共享类型化 Topic 和 payload。
- [contract/wire/wire.go](contract/wire/wire.go)：显式进程事件 wire。

Local 发布见 [events](../events/README.zh.md)；由进程支撑的类型化 Service 见
[process](../process/README.zh.md)；完整联邦契约见
[进程协议](../../docs/process-protocol.zh.md#event-federation)。
