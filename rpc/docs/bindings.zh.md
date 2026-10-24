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

Unit 与 Host callback 两端的 unary/stream handler panic 均返回 gRPC `Internal` 错误；实际 handler 退出后释放准入计数与 lease，后续调用仍可继续。panic 不重建任意业务错误，也不回滚已发生的副作用。

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

## 领域 adapter 的私有回调与导出发布

`AttachOptions.LocalImports` 为每次 Activate 创建的私有回调提供实例值：用 `BindLocalImport(binding, value)` 构造，Binding 仍必须出现在完整 ChildBoundary.Imports 中，类型与版本必须一致。nil、typed nil、重复项和边界外项在获取进程前拒绝。Attach 使用边界自身的 Codec/Validate；未覆盖的 Import 继续通过 Scope.Get 解析，必须声明 Requires。LocalImport 属于当前 Scope/Session，不能缓存到共享定义中，也不能用于绕过普通外部服务的 Requires。

Accept 中调用 `session.PrepareExports()`，用 `PreparedClient(prepared, key)` 读取待接受的 endpoint，再调用 `prepared.Provide(scope)` 发布同一批对象。每个 Session 只准备一次，factory 的失败结果也缓存；读取不会重建 client。Provide 只接受 Attach 所属的 Scope/generation，一次发布尝试失败后也不能重试；中途失败必须让 Activate 返回错误，由 Gordis 回滚所有 staged 服务。Session 停止后拒绝准备或发布。普通 NewProcess 使用同一路径；低层 `ExportClient` 每次可重新构造 client，adapter 应使用 PreparedClient 保持对象身份。

Snapshot 中 `CallbacksClosed` 表示 callback 入口已关闭，`CallbackHandling` 统计实际尚未退出的 unary/stream callback，`CallbacksDrained` 只有入口关闭且实际计数归零才成立。Child CleanupReport 与 Reaped 是独立证据。固定 Import 的清理回调保留到 Dispose 完成；实际 callback 未退出时，Scope 最终清理继续等待，其固定依赖也继续保留。调用方等待超时不代表后台清理完成。

## 即时准入

每个 Session 的两端各自持有出站 Pending 与入站 Handling 额度。Business、Control、Continuation 均使用 `AdmissionLimit{Pending, Handling}`：每个字段 0 表示不限，正数为明确上限，负数校验失败。Limits 零值不设置应用层并发上限。不限额度仍计数、关闭准入后仍拒绝新调用，停止时仍等待实际工作排空；没有队列、自动重试或跨类别借额。消息大小和超时字段的默认行为独立保留。

按业务负载明确填写容量，库不提供数值预设。例如，某接收端经测量可以同时执行 32 个 handler，可以在该端配置：

```go
limits := rpc.Limits{
    Business: rpc.AdmissionLimit{
        Pending:  0,  // 不限制本端出站 Business 调用数量。
        Handling: 32, // 本端最多 32 个实际入站 Business handler。
    },
}
```

此配置中的 Control/Continuation 仍不限；32 不是对其他服务的容量推荐。默认处理不会再次为零值额度补上限。

普通生成的 unary 和 stream 方法使用 Business。手写 Codec 可声明 `MethodClasses: map[string]rpc.AdmissionClass{"Commit": rpc.Control, "Continue": rpc.Continuation}`，NewBinding 复制该表。未列出的方法默认 Business；非法类别、没有对应 handler 的类别条目及非 Business stream 校验失败。两端按各自 Binding 声明检查，请求不携带类别；更改方法类别必须升级 Binding 版本。Control handler 只做有界状态操作，不执行用户 handler 或等待业务排空；Continuation 使用独立额度执行已有 invocation 的 next/base。

Pending 在 payload 序列化前申请，直到 Call/Stream 实际返回才释放，包括 stream yield 和结果解码。Handling 直到实际 unary/stream handler 返回才释放。caller 取消后返回可以释放 Pending，远端尚未退出的 handler 仍占 Handling。已取消调用不开始序列化或进入 handler；stream 与 unary 共用 Business 额度。

耗尽立即返回可由 `errors.Is(err, rpc.ErrBackpressure)` 识别的错误，并保留 gRPC `ResourceExhausted`；消息大小超限采用同样映射。Initialize、Quiesce、Dispose、Watch 不占领域额度。Session 停止时关闭出站准入，固定 Import callback 保持到 Child 本地清理结束；随后 Child 先关闭 callback 连接，再等待剩余出站工作，避免排空与连接关闭互相等待。Host 最终清理仍等待实际 callback 和被阻塞的序列化退出。

Host 与 Child 分别配置自己的 Limits；修改 Host 额度不会改变 Child 的额度。`Snapshot().Admission` 显示当前 Host 端三类 Pending/Handling。`CallbackHandling == 0` 只表示当前没有实际执行的 callback；`CallbacksDrained` 还要求 callback 入口已关闭。`CallbackTransportClosed` 表示 callback transport 关闭完成。这些状态与 Child CleanupReport、Reaped、被阻塞的出站序列化分别判断。序列化或 handler 卡住时，计数和 Scope 清理责任保持到实际返回。

可选 adapter 可用 `prepared.ProvideServices(scope, services...)` 发布显式声明的子集，其他已准备 client 保持 Session 私有。它与 Provide 共用一次发布机会，失败也消耗；重复或未声明项明确拒绝。`Unit.Validate()` 公开 ServeUnit 使用的闭合装配校验，不执行配置或激活。

### 如何设置容量

额度统计并发工作数量，不是每秒请求量，也不是工作线程池大小。每端、每类、每个方向的计数由该 Session 全部方法/契约共享，不跨 Session 共享，也不保证方法间公平。先在实际负载下观察同时调用数、handler 耗时、stream 寿命、内存/CPU 和下游容量，再选择上限。Pending 与 Handling 保护的是不同工作，两者不必相等。

| 字段 | 设置时需要考虑 |
| --- | --- |
| Business | 普通调用和反向回调；慢服务、长 stream 与同 Session 的短调用竞争额度。stream 整个调用期间占一个额度，不按消息逐条计数。 |
| Control | 只承载短小的状态管理操作，避免执行用户业务；不能为了绕过饱和而把慢业务方法标为 Control。 |
| Continuation | adapter 已验证的现有 invocation 的 next/base 执行；需要考虑并行和嵌套续调，有限额度不保证任意嵌套深度。普通回调不会自动进入此类。 |
| Pending | 包括序列化、传输等待、响应解码和 stream yield，直到调用方实际返回；取消不能提前释放仍被阻塞的序列化/yield 所占额度。 |
| Handling | 实际 handler 从准入到返回的占用；caller 已取消返回时 handler 可能仍执行，因此后续调用仍可能被拒绝。 |

Host 的 Pending 统计调用 Child 导出，Handling 统计 Child 对 Host 导入的回调；Child 两者方向相反。提高 Host Pending 不会提高 Child Handling。Initialize/Quiesce/Dispose/Watch 绕过三类预算，但普通业务清理回调仍按声明类别计数，没有保留容量。同步回调重入时，外层调用仍持有额度，内层需要额外额度；较小的有限 Business 上限可能拒绝嵌套业务。Continuation 只保护显式分类且属于现有 invocation 的续调。

`errors.Is(err, rpc.ErrBackpressure)` 同时覆盖消息大小超限。嵌套调用失败或响应过大之前，业务可能已经产生部分效果，因此该 sentinel 不表示整个操作从未执行，也不能单凭它判断可以安全重试。RPC 不自动重试。

其余 Limits 字段使用不同的零值规则：

| 字段 | 范围与默认行为 |
| --- | --- |
| MaxMessage | 本端 RPC server 和 Host 出站连接的收发限制，按每条 gRPC 消息的字节数计算，包括编码/envelope 开销；<= 0 使用 4 MiB。不是整个 stream/Session 的内存上限。此字段不应用于 Child broker 回调客户端；该连接使用 go-plugin 的客户端默认值，同时仍受 Host callback server 的上限约束。 |
| StartupTimeout | Host 发现/Attach 启动，包括 Import 验证、Initialize 和 Accept；<= 0 使用 5s。较早的 context deadline 生效，自定义验证/Accept 须响应取消；不为普通业务调用设置 deadline。 |
| StopTimeout | Host 停止控制等待；<= 0 使用 4s。OnStop Quiesce 和最终 Quiesce/Dispose 分别取得超时窗口；不限制实际清理总耗时，阻塞的 callback/序列化可能在 Child 回收后仍未退出。调用方的 Shutdown 等待需单独设置 context。 |
| OrphanGrace | Child 在父进程/主连接丢失或 ServeUnit context 取消后的清理宽限；<= 0 使用 2s，到期停止服务，即使清理尚未完成。它不限制正常 Host Dispose，也不能证明清理成功。 |

`Snapshot().Admission` 返回 Host 端已观察到的计数，不是配置上限；计数为零表示空闲。它与另一端的实际工作、Child 清理报告和 Reaped 分别判断。
