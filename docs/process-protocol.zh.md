# 进程协议 v1

[English](process-protocol.md) | 中文

`process` 监督受信任的本机子进程，并通过 stdio 提供双向 JSON RPC。Host 和插件都能发起调用；业务方法、DTO 和授权由应用定义，Go 对象、Scope 和服务 Key 不跨进程传递。

当前协议标识为 `gordis.process/1`，双方必须精确匹配。发生不兼容变化时，应升级协议并重新构建 Host、插件和 manifest。

## 使用方式

Host 启动子进程，并显式提供插件可以调用的方法：

```go
client, err := process.Start(ctx, process.Options{
    Path: executable,
    Identity: identity,
    Instance: "sampler-alpha",
    Generation: 1,
    Handler: hostHandler,
})
```

插件用同一会话接收调用并回调 Host：

```go
err := process.Serve(ctx, os.Stdin, os.Stdout, identity, process.Limits{}, process.Handler{
    Initialize: initialize,
    Call:       pluginHandler,
})
```

`process.Caller.Call(ctx, method, params, result)` 可并发使用。初始化期间也可以回调 Host；处理器还可以嵌套调用对端，但不应形成递归调用或等待环。

可运行示例：

- [process](../examples/process/README.md)：进程插件向 Gordis Consumer 提供类型化服务。
- [duplex](../examples/duplex/README.md)：一次 Host → 插件 → Host 的嵌套调用。
- [application-plugin](../examples/application-plugin/README.md)：远端 Plugin 自带数据面，并把 UI 加载进 Host。

## 线协议

- stdio 使用 UTF-8 JSON Lines；stdout 只写协议，stderr 用于日志。
- 每条消息携带 `session`、`instance`、`generation` 和请求 ID。
- 两端独立分配单调递增的请求 ID；`cancel` 使用原请求 ID。
- deadline 以 Unix 毫秒传递；取消不保证撤销已经发生的业务副作用，也不会自动重试。
- 不匹配会话、实例或 generation 的迟到消息被丢弃；重复或倒退的请求 ID 终止连接。

启动顺序：

```text
hello(gordis.process/1)
  → 校验 protocol / package / version / contracts
  → initialize(instance, generation, config)
  → ready
```

`hello`、`initialize`、`quiesce`、`dispose`、`shutdown` 和 `failed` 是保留方法。业务调用只能使用应用定义的方法名。

默认限制为单条消息 64 KiB、出站等待调用 16 个、入站处理器 16 个、写队列 16 条。队列满返回 `ErrBackpressure`；损坏、超长消息或 I/O 错误会终止会话。应用应使用有界 DTO，并让处理器响应 context 取消。

## `processbridge`

`processbridge` 把 Gordis 服务契约映射到进程调用：

- `Bind` 为一个 `Key[T]` 注册显式 client、handler 和 DTO 编解码。
- Host 中的 `Adapter` 获取声明的依赖、启动进程，并在同一个 Key 下发布类型化代理。
- 子进程中的 `Serve` 创建本代 Scope、注入依赖代理，并导出业务 Plugin 提供的服务。

业务 Plugin 仍使用普通的 `Spec`、`Start`、`Get` 和 `Provide`。Consumer 不知道得到的是本地对象还是进程代理。仅凭服务名无法安全推导 RPC，因此 wire binding 必须显式存在。

Adapter 的 ID 是逻辑插件类型 ID，不应编码 `-process` 等执行方式。实例 ID 和 generation 由 Host 分配，子进程不得创建自己的 Host 或重新编号。

## 生命周期与清理

声明 `gordis.lifecycle/1` 后，关闭过程为：

```text
quiesce → dispose → shutdown
```

| 阶段 | 含义 |
| --- | --- |
| `quiesce` | 拒绝新业务调用，冻结远端 Scope 并执行 OnStop |
| `dispose` | 取消任务、等待租约、逆序执行 Defer，返回清理结果 |
| `shutdown` | 清理完成后停止 transport |
| `failed` | 插件向 Host 报告当前会话的运行失败 |

Host 侧 Adapter 先关闭代理入口并排空已经准入的调用，再关闭 Client。远端清理完成前，插件仍可能回调 Host，因此不能在 OnStop 提前关闭固定依赖。

进程退出只证明 OS 资源被回收，不等于业务清理成功：

- `CleanupKnown` 表示是否收到远端 dispose 结果。
- `Cleanup` 保存业务清理结果。
- `Reaped`、`IOComplete` 和 `HandlersComplete` 分别表示进程、管道和 Host handler 的回收状态。

断连、崩溃或清理响应丢失时，Host 将远端清理视为未知并保留 residual，避免在可能仍有副作用时启动新代。Kill 只能终止子进程，不能终止不响应取消的 Host goroutine。

## 本地包与边界

manifest 包含 `id`、`version`、相对 `entry`、`sha256`、`protocol` 和 `contracts`。加载器校验路径没有逃出包目录、文件可执行、摘要和契约匹配。摘要用于完整性检查，不代表签名或信任来源。

当前实现不包含远程发现、网络传输、鉴权、断线重连、自动代理生成或进程树清理。只执行应用明确提供并信任的本地程序。
