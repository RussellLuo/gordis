# 最小远端 Service

[English](README.md) | 中文

插件程序在子进程中运行一个可配置的普通 Greeter Plugin。Host 的消费方 Plugin 通过
`scope.Get(contract.Key)` 取得 Service，调用 `Greet("Gordis")`，使用与本地 provider
相同的业务接口。

## 运行

从 `rpc/` 执行：

```sh
GOWORK=off go build -o /tmp/gordis-rpc-basic-host ./examples/basic/host/cmd
GOWORK=off go build -o /tmp/gordis-rpc-basic-plugin ./examples/basic/plugin/cmd
/tmp/gordis-rpc-basic-host /tmp/gordis-rpc-basic-plugin
```

预期输出：

```text
Hello, Gordis!
```

Host 根据发现的 Schema 校验程序 JSON，无需导入或解码为子进程的配置类型：

```sh
/tmp/gordis-rpc-basic-host -config '{"prefix":"Welcome"}' /tmp/gordis-rpc-basic-plugin
# Welcome, Gordis!
/tmp/gordis-rpc-basic-host -config '{}' /tmp/gordis-rpc-basic-plugin
# Hello, Gordis!（Config.New 的默认值）
/tmp/gordis-rpc-basic-host -config '{"prefix":""}' /tmp/gordis-rpc-basic-plugin
# Host Schema 校验失败，以状态码 1 退出，不打印 greeting。
/tmp/gordis-rpc-basic-host -config '{"unexpected":true}' /tmp/gordis-rpc-basic-plugin
# 同样在 Initialize 前被 Host 拒绝，状态码 1，不打印 greeting。
```

## 普通代码与程序装配

两端采用相同布局：`plugin.go` 定义普通业务 Plugin，`cmd/main.go` 负责程序装配。

```text
basic/
  host/plugin.go       # 消费方 Plugin
  host/cmd/main.go     # Host 程序入口
  plugin/plugin.go     # 提供方 Plugin
  plugin/cmd/main.go   # 插件程序入口
  contract/service.go  # 普通接口、Key、版本
  bindings/greeter.go   # 生成的 RPC Binding
```

[契约](contract/service.go) 声明 Greeter 接口、类型化 Key、语义版本和逻辑 Plugin ID。
[业务 Plugin](plugin/plugin.go) 以默认前缀 `Hello` 提供该 Key，由 Gordis
解码和验证配置。[消费方 Plugin](host/plugin.go) 取得 Key 后调用普通接口。契约和业务代码均不依赖 RPC。

`contract.PluginID` 为 `example/basic/greeter-provider`，服务 Key 的名称为
`example/basic/greeter`。两者独立声明：前者选择插件实现，后者标识服务契约，
通过 `Spec.Provides` 建立关系。

消费方在 `host` 包中声明 `PluginID = "example/basic/greeter-consumer"`，
由其 `Spec()` 和 Host 程序入口共同引用，通过 `Requires` 依赖 Greeter。
实例 ID 仍使用 `"consumer"`、`"greeter"` 这样的短名称。

Host 在[程序入口](host/cmd/main.go)中直接声明可执行路径、预期程序身份和 ChildBoundary，创建
`rpc.NewProcess`。插件程序在自己的入口声明相同的程序身份，无需额外的共享声明包。
库负责转交实例配置，在远端 Ready 后发布生成的 Greeter proxy。
程序启动成功尚不代表业务 Ready：插件程序的 Unit 必须成功激活并解析导出。

[插件程序入口](plugin/cmd/main.go) 装配一个普通 Greeter 实例，指定
由 `rpc.NewConfig[Config]()` 构建的 `Config: config`，无 Imports，唯一生成的 Export 明确来自该实例，然后调用
`ServeUnit`。Config.New 提供 Hello，Configure 将程序配置映射到内部 greeter 实例。
Host CLI 缺省传 `{}`；Host 按本次 Describe 的 Schema 在 Initialize 前拒绝空前缀和未知字段，
child 仍独立校验。不需要 Host 回调服务。Host 收到 greeting 后完成 Shutdown，合并业务与清理错误。

程序配置是具名非指针结构体，New/Configure 使用值接收者；json tag 使 prefix 可省略，
jsonschema 提供描述，jsonschemax 声明 minLength 约束。required 在默认值之前校验；
Configure 按 child 内部实例 ID 映射，Host 无需知道该 ID。
映射、零值与 Schema 限制见[程序配置](../../docs/concepts.zh.md#声明程序配置)。

## 生成 Binding

契约维护一处 `go:generate` 声明。修改接口后，从
`rpc/examples/basic/contract/` 执行：

```sh
GOWORK=off go generate
GOWORK=off go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Greeter -key Key -version Version -package bindings -out ../bindings/greeter.go -check
```

将 [bindings/greeter.go](bindings/greeter.go) 与契约一同提交；正常构建
直接编译该文件，不触发生成。伴随包包含生成的 client、handler 和 Binding，统一以 `contract` 引用普通契约、
`rpc` 引用运行库，内部实现名为 `greeterClient`、`newGreeterHandler`。作者只维护普通代码
与程序装配，无需手写 RPC adapter。

Binding 版本为 `rpcgen.json/1:1`，使用严格的按位置 `args` 请求和 `value` 响应，
替换 EX01 的手写 wire。本次早期重构保持程序与契约版本均为 `1`，Host 与插件程序
必须一起重建。

## 验证

从 `rpc/` 执行：

```sh
GOWORK=off go test -race -count=1 ./examples/...
```

测试用 `-race` 构建真实 Host 和插件程序，以同一个普通测试消费方比较 Local/process
结果，并观察成功和非法配置路径的清理及进程回收。依赖审计确认 Host 不包含提供方
业务实现，两端业务包均不依赖 RPC 运行库或生成工具。通用加载器和 wire 故障测试归库与生成器。
