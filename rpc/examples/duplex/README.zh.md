# 导入依赖与嵌套回调

[English](README.md) | 中文

在 [basic](../basic/README.zh.md) 的远端服务基础上，插件程序的 Greeter 再调用 Host
提供的 UserDirectory。这里的 duplex 指双向服务调用，不是 gRPC 双向流。

```text
Host consumer → 插件程序 Greeter.Greet("user-42")
              → Host UserDirectory.DisplayName("user-42")
              ← "Gordis"
              ← "Hello, Gordis!"
```

## 运行

从 `rpc/` 执行：

```sh
GOWORK=off go build -o /tmp/gordis-rpc-duplex-host ./examples/duplex/host/cmd
GOWORK=off go build -o /tmp/gordis-rpc-duplex-plugin ./examples/duplex/plugin/cmd
/tmp/gordis-rpc-duplex-host /tmp/gordis-rpc-duplex-plugin
```

预期业务输出：

```text
callback: user-42 -> Gordis
Hello, Gordis!
```

callback 一行由 Host Directory 输出；最后一行在 consumer 收到 Greeter 的结果后输出。
CLI 只接收插件程序路径，本例没有业务配置。

## 一个 Import，一个 Export

- [契约](contract/greeter.go)与 [Directory 契约](contract/directory.go)只包含普通 Go 接口、Key、版本及生成声明。
- [Host Directory](host/directory.go) 是普通 provider；[Greeter Plugin](plugin/plugin.go)
  声明 Requires Directory，通过 `scope.Get` 固定取得依赖，并在 `Greet` 中传递调用 context。
- [Host consumer](host/plugin.go) 仍只取得 Greeter 并调用普通接口。业务包与契约均不依赖 RPC。
- [Host 入口](host/cmd/main.go) 依次挂载 Directory、`rpc.NewProcess` 和 consumer，等待各自 Ready。
  加载器将 Imports 映射为 Requires，使 Directory 在实际回调与进程清理完成前保持存活。
- [插件程序入口](plugin/cmd/main.go) 用 `ServeUnit` 装配一个 Greeter 实例，导入
  `bindings.UserDirectory`，从该实例导出 `bindings.Greeter`。

Host 的 `ChildBoundary` 对应插件程序的 `Unit.Boundary()`。
**Imports/Exports 始终以插件程序为参照**，两端声明相同方向：Directory 是 Import，
Greeter 是 Export。Unit 使用 ConfigSpec 零值，只接受缺省输入或空对象；程序入口只负责
身份、边界和生命周期，Host 不导入 Greeter 的业务实现。

本例无需 NewConfig 或 Configure；零值声明的 Schema 为
`{"type":"object","additionalProperties":false}`，非空配置在 Initialize 前校验失败。
详见[程序配置规则](../../docs/concepts.zh.md#声明程序配置)。

插件注册 ID 与服务 Key 独立声明，通过 `Spec.Provides` 关联：

| PluginID（插件实现） | Key 名称（服务契约） |
| --- | --- |
| `example/duplex/greeter-provider` | `example/duplex/greeter` |
| `example/duplex/directory-provider` | `example/duplex/directory` |

消费方在 `host` 包中声明 `PluginID = "example/duplex/greeter-consumer"`，
由其 `Spec()` 和 Host 程序入口共同引用，通过 `Requires` 依赖 Greeter。
实例 ID 仍为 `"consumer"`、`"greeter"`、`"directory"` 这样的短名称。

## 生成与验证

从 `rpc/examples/duplex/contract/` 执行：

```sh
GOWORK=off go generate
GOWORK=off go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Greeter -key GreeterKey -version Version -package bindings -out ../bindings/greeter.go -check
GOWORK=off go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type UserDirectory -key DirectoryKey -version Version -package bindings -out ../bindings/directory.go -check
```

两个生成的 Binding 随源码提交，普通构建不运行生成器。从 `rpc/` 验证：

```sh
GOWORK=off go test -race -count=1 ./examples/...
```

示例测试用 `-race` 构建真实 Host/插件程序，检查两行结果、成功退出和生产依赖边界。
并发回调、初始化/清理回调、取消与实际 handler 退出由库内测试负责，参见
[架构与生命周期](../../docs/architecture.zh.md)。
