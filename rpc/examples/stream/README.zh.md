# 流式结果与提前停止

[English](README.md) | 中文

在 [basic](../basic/README.zh.md) 和 [duplex](../duplex/README.zh.md) 之后，本例增加
server-stream：一次请求逐条返回有限的 typed chunk。使用确定性的 Generator，
不依赖模型 SDK，没有业务配置。

## 运行

从 `rpc/` 执行：

```sh
GOWORK=off go build -o /tmp/gordis-rpc-stream-host ./examples/stream/host/cmd
GOWORK=off go build -o /tmp/gordis-rpc-stream-plugin ./examples/stream/plugin/cmd
/tmp/gordis-rpc-stream-host /tmp/gordis-rpc-stream-plugin
```

预期业务输出：

```text
complete: 1
complete: 2
complete: 3
partial: 1
partial: 2
consumer stopped
```

CLI 只接收插件程序路径。Host 调用两次：完整消费 `count=3`，再请求 `count=5`，
消费第二条后提前停止。只把预期的本地 sentinel 当作成功的提前停止；其他错误
使程序失败，Shutdown 错误也正常传播。

## 普通流式接口

[契约](contract/generator.go)声明：

```go
type Generator interface {
    Generate(context.Context, int, func(Chunk) error) error
}
```

count 为正数时产生 1 到 count 的序号，0 为空流，负数返回业务错误。
[provider](plugin/plugin.go) 检查 context、同步调用 yield，遇到 yield 错误立即返回，
不创建后台生产 goroutine 或应用队列。[consumer](host/plugin.go) 通过 `scope.Get`
取得 Generator；两端业务包都只使用普通契约。

**yield 是各自进程内的本地函数，不被序列化。** 生成的 [Binding](bindings/generator.go)
在服务端编码 chunk，在 Host 解码后交给本地 yield。Host 的 yield 返回错误时，
保留该本地错误并取消远端流；sentinel 无须由远端 Go error 对象重建。

[Host 入口](host/cmd/main.go) 复用 basic 的 `rpc.NewProcess` 加载器。
[插件程序入口](plugin/cmd/main.go) 通过 `ServeUnit` 装配一个 Generator 实例：
无 Imports，一个 Export。Unit 的 `ConfigSpec` 零值只接受缺省配置或空对象。
程序入口负责身份、边界和生命周期；Host 不导入 provider 业务实现。

客户端提前返回不代表已证明实际远端 handler 退出。传输层允许缓冲，取消到达前
服务端可能已经生成超过两条。本例不测量背压，也不建立字节级内存上限。
库的职责边界见[架构与生命周期](../../docs/architecture.zh.md)和
[Binding](../../docs/bindings.zh.md)。

## 生成与验证

从 `rpc/examples/stream/contract/` 执行：

```sh
GOWORK=off go generate
GOWORK=off go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Generator -key GeneratorKey -version Version -package bindings -out ../bindings/generator.go -check
```

生成的 Binding 随源码提交，普通构建不运行生成器。从 `rpc/` 执行：

```sh
GOWORK=off go test -race -count=1 ./examples/...
GOWORK=off go vet ./examples/...
```

示例测试用 `-race` 构建真实 Host/插件程序，检查完整和部分输出、成功退出及生产依赖边界。
取消、deadline、中途失败、并发隔离、实际 handler 退出和清理由库内测试负责，
通过专用 fixture 验证，不给示例增加故障开关。
