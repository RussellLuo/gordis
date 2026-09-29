# 核心概念

[English](concepts.md) | 中文

Gordis 把应用描述为一组插件实例。插件通过类型化服务协作，Host 负责校验组合与协调启停，Scope 负责一次运行中的任务、请求和资源。

## 概念全景

```text
契约定义
Plugin 原型 ──Spec()──> PluginSpec
Key[T] ──Spec()──> ServiceSpec ──声明于──> PluginSpec.Requires / Provides

应用装配
NewHost（Plugin 类型）──> Host ──> synthetic root Env
Env View + InstanceSpec（ID / Plugin / Config）──Mount──> desired instance graph
Host ──校验──> Requires / Provides + View 标签：服务依赖边
Host ──激活──> 实例 generation
               ├─ Plugin 运行对象（由 PluginSpec.New() 创建）
               ├─ Scope ──拥有──> 任务 / 租约 / 清理操作
               └─ 固定绑定：消费者 Key + 标签 → 提供者实例 + generation → 服务值
```

## 从插件类型到一次运行

| 概念 | 含义 |
| --- | --- |
| `Plugin` 原型 | 传给 `NewHost`，只用于读取稳定的 `Spec()`，不会被启动 |
| `PluginSpec` | 插件类型 ID、`New`、`Requires` 和 `Provides` |
| `Env` | 用于挂载实例的不可变 owner 与可见性 View |
| `InstanceSpec` | owner-local ID、插件类型与 JSON 配置 |
| `Instance` | 跨 Update/Restart generation 稳定、直到 Unmount 的逻辑句柄 |
| Plugin 运行对象 | `PluginSpec.New()` 为一次准备或激活创建的新对象 |
| generation | 同一实例的激活代次；每次实际激活递增 |
| `Scope` | 本代的身份、固定依赖、任务、租约和清理操作 |
| `Host` | 校验图、分配 generation、发布服务、协调启停并提供诊断 |

同一插件类型可以创建多个配置不同的实例。实例重新启动时 ID 不变，但运行对象、Scope 和 generation 都会更新。旧代的异步结果不能修改新代状态。

Host 在预检和激活时都执行：

```text
New → JSON 解码 → Spec 契约核对 → 可选 Validate → Activate 能力核对
```

预检对象会被丢弃，因此 `New` 和 `Validate` 必须无副作用；资源只能在 `Activate` 中创建。

## 服务、Key 与标签

服务是插件之间共享的 Go 值，通常是接口。公共契约包定义 `Key[T]`：

```go
type Store interface {
    Put(context.Context, string, []byte) error
}

var StoreKey = gordis.NewKey[Store]("example.store/1")
```

`Key[T].Spec()` 暴露存放在 `PluginSpec.Requires` 和 `PluginSpec.Provides` 中的
`ServiceSpec` 元数据。

提供者在 `PluginSpec.Provides` 中声明 `StoreKey.Spec()`，并在 `Activate` 中调用 `Provide`。消费者在 `Requires` 中声明同一个契约，再通过 `Get` 取得固定绑定。

`Plugin` 不等于 Service，`Provides` 也不会自动发布 Plugin。插件可以发布自身、另一个对象或多个服务值。

默认情况下，服务名同时是可见性标签。需要多个相同契约的提供者时，应用派生隔离的 Env：

```text
tenantA := host.Env().Isolate(StoreKey, "tenant-a")
tenantB := host.Env().Isolate(StoreKey, "tenant-b")
```

插件内部仍只使用 `StoreKey`，不需要知道可见性标签。Host 要求同一 `(Key, label)` 只有一个
provider，且两端契约名称与 Go 类型完全一致。

## 所有权、服务依赖与可见性

Gordis 中有三种彼此独立的关系：

| 关系 | 表达方式 | 决定什么 |
| --- | --- | --- |
| 生命周期所有权 | 从哪个 `Env` 执行 `Mount` | generation 归属、递归停止与 child-first 清理 |
| Service 依赖 | `PluginSpec.Requires` / `Provides` | Pending、激活条件、固定 binding 与 consumer-first 清理 |
| Service 可见性 | `Env.Isolate(Key, label)` | 当前 Env 下哪些 `(Key, label)` provider 可被选择 |

`host.Env()` 挂载的实例由 synthetic root 拥有；`scope.Env()` 挂载的 child 属于当前 parent
generation。ownership 不蕴含 readiness 依赖：Pending 或失败的 child 不会改变 owner 的
Phase 或 `WaitReady` 结果，除非 owner 显式 Requires child 提供的 Service。Isolate 只派生
不可变的可见性 View，不创建新的生命周期所有者。

## 状态、就绪与等待

generation 的主生命周期使用以下 `Phase`：

```text
pending ──依赖出现──> activating ──> ready ──> stopping ──> stopped
                         │                         ▲
                         └──────── failure ────────┘
```

Failure 不属于 `Phase`；它与最终 `Outcome`、cleanup 诊断分开记录。

- `ready` 表示实例自身激活成功并已发布服务。
- required binding 暂缺时，desired Instance 自动保持 `pending`，不需要实例级开关。
- `Snapshot` 报告 Phase、Failure/Outcome、绑定、generation、阻塞标签、任务、租约、停止原因和清理结果。

提交与就绪不是同一件事。`Env.Mount` 可以成功提交而实例仍处于 pending；只有下一步确实依赖
该次精确 desired revision 时，才调用 `Instance.WaitReady`。`Operation.WaitReady` 检查本次
operation 固定的每个 exact target，但不会递归加入它们的 children。

## 应用级 readiness gate

应用需要判断一组最低能力是否可用时，可以挂载一个普通 Plugin，让它 Requires 这些
Service，再对该 gate 调用 `MountReady`。任何 required Service 不可用时 gate 都会保持
Pending；启动工具可结合 provider Failure 及 Snapshot 的 `BlockedBy`/`BlockedLabels`
诊断原因。Core 不维护 ownership subtree 的聚合健康状态。

## Scope 与资源归属

`Scope` 只属于一个实例的一代。围绕它的 API 形成一条从依赖访问、工作准入到停止清理的链路：

| 作用 | API | 语义 |
| --- | --- | --- |
| 读取依赖 | `gordis.Get(scope, key)` | 读取本代启动时固定的 Service binding |
| 管理长期任务 | `scope.Go` / `scope.AfterReady` | 启动由 Scope 监督的任务；后者等待本代 Ready 后再启动 |
| 保护已准入工作 | `scope.Acquire` / `scope.AcquireReady` | 持有手动释放的请求租约；后者还会拒绝 Ready 前的调用 |
| 撤回入口 | `scope.OnStop` | 在取消任务前关闭 listener、route 或 subscription 等入口 |
| 最终释放 | `scope.Defer` | 在任务和租约退出后逆序释放资源 |

`Get` 是接收 Scope 参数的包级泛型函数，不是 Scope 方法；Scope 为它限定固定 binding 和有效
生命周期。`Go` 创建并监督 goroutine，`Acquire` 不创建 goroutine，只让调用方正在执行的工作
进入同一个停止排空屏障。长期任务使用 Scope 的运行 context，不使用启动 context；服务引用、
Scope 和后台 goroutine 都不应逃逸到跨代全局状态。精确停止顺序见[生命周期](lifecycle.zh.md)。

## 进程外执行

跨进程不会改变业务契约。原生提供者发布真实 Go 对象；`processbridge.Adapter` 在同一个 `Key[T]` 下发布类型化代理：

```text
consumer ── Get(GreeterKey) ──> 本地实现
                            └─> 类型化代理 ── RPC ──> 远端实现
```

Consumer 不知道服务来自哪个进程。进程装配仍需显式 wire binding，因为仅凭服务名无法推导参数序列化、错误映射和取消语义。详见[进程协议](process-protocol.zh.md)。

## 边界

Gordis 核心负责实例图、服务绑定、generation 和生命周期，不负责 HTTP、UI、Agent、包下载、持久化或安全沙箱。应用可以在核心之上实现包注册表、管理 API 和前端扩展系统。
