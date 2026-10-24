# gordis-rpc

[English](README.md) | 中文

让普通 [Gordis](../README.zh.md) Plugin 在可信的本地子进程中运行。消费者仍通过 `scope.Get` 使用类型化服务接口，gordis-rpc 负责进程启动、远程调用和清理。进程隔离不构成不可信代码的安全沙箱。

## 试一试

从 `rpc/` 执行：

```sh
GOWORK=off go build -o /tmp/gordis-rpc-basic-host ./examples/basic/host/cmd
GOWORK=off go build -o /tmp/gordis-rpc-basic-plugin ./examples/basic/plugin/cmd
/tmp/gordis-rpc-basic-host /tmp/gordis-rpc-basic-plugin
# Hello, Gordis!
```

Host 通过 `NewProcess` 接入子进程，子进程通过 `ServeUnit` 运行插件装配。

运行 `events` 示例或完整源码检查时，需要通过[临时 workspace](docs/development.zh.md#常用检查)映射本仓库的 Core 源码；当前固定的 Core 版本缺少 events 协作接口。

## 继续阅读

1. [basic 示例](examples/basic/README.zh.md)：了解契约、业务插件、配置与程序装配。
2. [核心概念](docs/concepts.zh.md)：理解服务边界与子进程装配。
3. [Binding](docs/bindings.zh.md)：从 Go 接口生成 RPC 适配代码，配置额度与错误处理。

按需查阅[架构与生命周期](docs/architecture.zh.md)，了解启动、回调与清理；[开发与验证](docs/development.zh.md)介绍检查命令与发行要求。

更多可运行场景见[示例导航](examples/README.zh.md)。跨进程事件通信见 [RPC events](events/README.zh.md)。
