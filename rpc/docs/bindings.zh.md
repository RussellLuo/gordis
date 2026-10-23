# Binding：连接普通接口与 RPC

[English](bindings.md) | 中文 · [返回 README](../README.zh.md)

Binding 将一个类型化 Key、兼容版本和 `Codec[T]` 组合起来。Codec 提供 `Client(Invoker) T`、`Server(T) Handler`，以及可选的 `Validate(context.Context, T) error`。业务代码只使用普通接口；装配代码引用 Binding。

## 优先从普通接口生成

契约包声明 interface、`gordis.Key[Interface]` 变量和非空字符串版本常量。生成代码放在同 module 的独立伴随包，单向导入契约包；示例使用 `contract`、`bindings` 和运行库别名 `rpc`。

伴随包命名为 `bindings`，生成文件按契约命名，例如 `Greeter` 对应 `bindings/greeter.go`，`Provider` 对应 `bindings/provider.go`。文件头使用完整工具路径 `github.com/RussellLuo/gordis/rpc/cmd/rpcgen` 标识生成来源；生成器仍要求显式指定 `-out`。

从 `rpc/examples/basic/contract/` 执行：

```sh
GOWORK=off go generate
GOWORK=off go run github.com/RussellLuo/gordis/rpc/cmd/rpcgen -type Greeter -key Key -version Version -package bindings -out ../bindings/greeter.go -check
```

`-type`、`-key`、`-version` 引用导出的 Go 符号，无需重复填写 wire ID 和版本。自己的契约可采用相同的 [go:generate 声明](../examples/basic/contract/service.go)，工具版本由项目固定的 module 依赖决定；当前 module 尚未发布。

生成文件提交源码，正常构建不运行生成器。`-check` 只读检查输出是否最新；分析失败保留旧文件，成功后原子替换。输出须位于契约 module 内的独立包，不能形成导入环。

## 支持的方法

```go
// 一次请求，返回一个结果；也支持只返回 error。
Greet(context.Context, string) (string, error)

// 一次请求，多次同步调用 yield，最后返回 error。
Generate(context.Context, Request, func(Chunk) error) error
```

支持多个或零个业务参数、嵌入接口的方法，以及导出的 yield 类型/别名。yield 必须是末尾的 `func(Chunk) error`，不进入请求 payload。nil yield 在本地报错；本地 yield 返回的错误原样返回，并取消远端流。坏 chunk、提前停止、业务失败和 context 取消/deadline 均有对应的失败路径。

## DTO 与兼容规则

| 项目 | 规则 |
| --- | --- |
| 值类型 | 基本 JSON 值、导出 DTO、结构体、指针、切片、数组、字符串键 map、递归 DTO |
| null | 指针、切片、map 接受；其他类型及其嵌套字段拒绝 |
| 字段 | 名称大小写精确匹配，拒绝未知/重复字段；缺省字段保留 Go 默认值 |
| 数组与字节 | 数组长度必须精确；字节切片用 JSON base64 字符串 |
| 原始 JSON | `json.RawMessage` / `jsontext.Value` 及导出别名内嵌一个合法 JSON 值，接受 null；内部字段由应用校验 |
| 信封 | 请求为精确位置与个数的 `args` 数组；结果/chunk 为 `value`；仅 error 的成功响应为 `{}` |
| 版本 | `rpcgen.json/1:<Version>`；格式前缀与业务契约版本共同参与握手 |

原始 JSON 可用于参数、返回值、嵌套 DTO 和流式 chunk，不使用 base64，也不经过 `float64` 转换。生成器只检查其 JSON 合法性，内部结构由应用的 Schema 和策略校验；信封与外围 DTO 字段继续执行严格检查。nil RawMessage 编码为 null；指针遇到 null 时按普通 Go JSON 语义解码为 nil 指针。任意自定义 JSON/text codec 仍需显式 Codec。

每个生成的 Binding 同时提供 Codec 工厂，例如 `bindings.GreeterCodec()`。需要在 Ready 前执行领域检查时，取得新 Codec、设置 `codec.Validate`，再用 `rpc.NewBinding(contract.Key, bindings.Greeter.Version(), codec)` 装配；不会修改默认 Binding 或改变 wire 格式。

契约作者负责语义兼容；格式前缀只能区分编码格式。当前未发布阶段发生 API/wire 变化时，Host 和 child 应一起重建。

## 何时手写 Codec

泛型接口/DTO、变参、任意 interface、自定义 JSON/text codec、嵌入或未导出的 DTO 字段不在当前生成范围内（可用 `json:"-"` 忽略字段）。其他回调、客户端流、双向流或进程资源也需要显式设计适配。

手写时用 `NewBinding(key, version, Codec[T]{...})`，通过 Handler 显式注册 unary/stream 方法。Invoker 只允许访问该 Binding 的契约；Validate 必须响应初始化取消。回调需建模成另一端提供的契约，资源需转换为 DTO/句柄并定义生命周期；底座提供 unary/server-stream，不能直接传输 Go 函数、Scope 或运行时对象。

可读示例：[生成的 Greeter](../examples/basic/bindings/greeter.go)、[手写 Binding](../internal/testfixture/service.go)、[生成的流与回调契约](../internal/streamfixture/contract/service.go)。检查命令见[开发与验证](development.zh.md)。
