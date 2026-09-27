# 进程插件

[English](README.md) | 中文

本示例启动一个空 Host，挂载独立构建的 Greeter 插件，调用其类型化服务，并在 shutdown
期间回收子进程。

Host 只导入共享契约和 wire adapter，Greeter 实现不会链接进 Host 二进制文件。

## 运行

在仓库根目录执行：

```sh
go build -o /tmp/gordis-process-host ./examples/process
go build -o /tmp/gordis-process-plugin ./examples/process/plugin
/tmp/gordis-process-host /tmp/gordis-process-plugin
```

预期输出如下，每次运行的 PID 会不同：

```text
host PID <host-pid> started without a plugin
plugin PID <plugin-pid> started
Hello, Gordis!
plugin process reaped
```

## 实现原理

Host 在逻辑插件 ID `greeter` 下注册通用 `processbridge.Adapter`。root Env 使用与 Local
插件相同的 `MountReady` API 挂载 provider 和 consumer。adapter 启动子进程，并将其 RPC 绑定
公布为类型化 `Greeter` 服务。consumer 只依赖 `GreeterKey`，因此无需知道 provider 是本地运行
还是运行在另一进程中。consumer 直接在 `Activate` 方法中调用并输出 greeting。

关键文件：

- [main.go](main.go)：Host 装配和延后插件连接。
- [plugin/main.go](plugin/main.go)：Greeter 插件进程。
- [contract/contract.go](contract/contract.go)：共享类型化契约。
- [contract/wire/wire.go](contract/wire/wire.go)：RPC 绑定。

双向进程调用见 [duplex](../duplex/README.zh.md)；一起交付 backend 和 UI 的示例见
[application-plugin](../application-plugin/README.zh.md)。
