# 类型化事件发布

[English](README.md) | 中文

本示例挂载可选 EventBus，并向一个 subscriber 发布类型化订单事件。

## 运行

在仓库根目录执行：

```sh
go run ./examples/events
```

预期输出：

```text
received order: order-42
```

## 实现原理

`orderCreatedTopic` 是类型化的 `events.Topic[orderCreated]`。两个应用插件都在 `Requires`
中声明 `events.BusKey.Spec()`，因此在 `events.Plugin` 实例提供 EventBus 前，它们会保持
Pending。

observer 通过 `events.Bind(scope).On` 注册 handler。publisher 使用 `Scope.AfterReady`，
因为事件分发只会在其 generation 进入 Ready 后被接纳；随后它调用 `Publish`，将事件以
有界并行方式投递给所有匹配的 handler。主流程会等到 observer 收到事件，使示例确定性退出。

subscription 归注册它的 Scope 所有，并会在该 Scope 停止时自动移除。

完整程序见 [main.go](main.go)；Topic、Hook、顺序和分发语义见
[编写插件](../../docs/plugin-authoring.zh.md#5-可选-eventbus)。
