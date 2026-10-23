# gordis-rpc

[English](README.md) | 中文

让普通 [Gordis](https://github.com/RussellLuo/gordis) Plugin 在可信的本地子进程中运行。消费者仍通过 `scope.Get` 使用类型化服务接口，gordis-rpc 负责进程启动、远程调用和清理。

独立 Go module：`github.com/RussellLuo/gordis/rpc`，包名 `rpc`。依赖 HashiCorp go-plugin/gRPC，不依赖 Qi。位于 Gordis 仓库的 `rpc/` 目录，尚未发布版本，API 和 wire 协议仍可能变化。Core 模块不依赖 RPC。

## 试一试

从 `rpc/` 执行：

```sh
GOWORK=off go build -o /tmp/gordis-rpc-basic-host ./examples/basic/host/cmd
GOWORK=off go build -o /tmp/gordis-rpc-basic-plugin ./examples/basic/plugin/cmd
/tmp/gordis-rpc-basic-host /tmp/gordis-rpc-basic-plugin
# Hello, Gordis!
```

[basic 示例](examples/basic/README.zh.md) 介绍配置、普通 Plugin、生成的 Binding 和插件程序 Unit。Host 程序用 `NewProcess` 注册远端 provider，插件程序用 `ServeUnit` 运行自己的装配。

## 继续阅读

- [核心概念](docs/concepts.zh.md)：契约、Binding、Boundary、Unit、Session 和程序配置。
- [架构与生命周期](docs/architecture.zh.md)：职责划分、启动、回调和清理。
- [Binding](docs/bindings.zh.md)：从 Go 接口生成适配代码，或手写 Codec。
- [开发与验证](docs/development.zh.md)：运行命令、验收记录和发行边界。
- [示例导航](examples/README.zh.md)：已有的可运行示例。
