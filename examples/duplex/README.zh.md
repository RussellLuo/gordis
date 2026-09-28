# 双向进程 Service

[English](README.md) | 中文

本示例展示进程外 Plugin 同时消费和提供类型化 Service。Host 中的 consumer 调用进程外
Greeter 的 `Greet("user-42")`；Greeter 在处理该请求时通过 `Requires` 调用 Host 中的
`UserDirectory.DisplayName("user-42")`，得到 `Gordis` 后返回 `Hello, Gordis!`。

业务 Plugin 仍使用普通的 `Spec`、`Activate`、`Get` 和 `Provide`；双向进程通信由
`processbridge` 的显式 binding 完成。

## 运行

在仓库根目录执行：

```sh
go build -o /tmp/gordis-duplex-host ./examples/duplex
go build -o /tmp/gordis-duplex-plugin ./examples/duplex/cmd/greeter-plugin
/tmp/gordis-duplex-host /tmp/gordis-duplex-plugin
```

预期输出：

```text
Host consumer → process Greeter: greet("user-42")
process Greeter → Host UserDirectory: displayName("user-42")
result: Hello, Gordis!
```

## 实现原理

`greeter.Plugin` 声明 `Requires: UserDirectoryKey` 和 `Provides: GreeterKey`。Host Adapter
为 `UserDirectoryKey` 建立 Host handler，并把远端 `Greeter` 作为同一个 `GreeterKey` 下的
类型化代理发布。子进程中的 `Serve` 为 `UserDirectoryKey` 注入 client proxy，并为真实
`Greeter` 建立 handler。

因此调用链保持在普通 Service 模型中：

```text
Host consumer → remote Greeter → Host UserDirectory → remote Greeter → Host consumer
```

将 Host 登记的 process prototype 换成 `new(greeter.Plugin)`，即可切换到 Local 模式；
Greeter、UserDirectory provider 和 consumer 的业务代码都无需改变。

关键文件：

- [main.go](main.go)：装配 Host UserDirectory、进程外 Greeter 和本地 consumer。
- [greeter/plugin.go](greeter/plugin.go)：不依赖进程能力的普通业务 Plugin。
- [greeter/proc/bridge.go](greeter/proc/bridge.go)：`UserDirectory` 与 `Greeter` 的双向 binding。
- [cmd/greeter-plugin/main.go](cmd/greeter-plugin/main.go)：子进程入口。

更基础的单向 Process Service 见 [process](../process/README.zh.md)；底层 stdio RPC 及嵌套
调用约束见[进程协议](../../docs/process-protocol.zh.md)。
