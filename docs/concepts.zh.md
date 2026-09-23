# 核心概念

[English](concepts.md) | 中文

Gordis 把应用描述为一组插件实例。插件通过类型化服务协作，Host 负责校验组合与协调启停，Scope 负责一次运行中的任务、请求和资源。

## 概念全景

```text
契约定义
Plugin 原型 ──Spec()──> PluginSpec
Key[T] ──Spec()──> ServiceSpec ──声明于──> PluginSpec.Requires / Provides

应用装配
InstanceSpec
├─ Plugin 字段 ──选择──> PluginSpec.ID
├─ ID / Config / Parent / Optional
└─ Inputs / Outputs ──把服务契约映射到槽位

PluginSpec + InstanceSpec ──> Host
Host ──校验──> 实例图
               ├─ Parent：所有权边
               └─ Requires / Provides + 槽位：服务依赖边
Host ──激活──> 实例 generation
               ├─ Plugin 运行对象（由 PluginSpec.New() 创建）
               ├─ Scope ──拥有──> 任务 / 租约 / 清理操作
               └─ 固定绑定：消费者 Key → 槽位 → 提供者实例 + generation → 服务值
```

## 从插件类型到一次运行

| 概念 | 含义 |
| --- | --- |
| `Plugin` 原型 | 传给 `NewHost`，只用于读取稳定的 `Spec()`，不会被启动 |
| `PluginSpec` | 插件类型 ID、`New`、`Requires` 和 `Provides` |
| `InstanceSpec` | 应用中的实例 ID、插件类型、JSON 配置和装配关系 |
| Plugin 运行对象 | `PluginSpec.New()` 为一次准备或激活创建的新对象 |
| generation | 同一实例的激活代次；每次实际激活递增 |
| `Scope` | 本代的身份、固定依赖、任务、租约和清理操作 |
| `Host` | 校验图、分配 generation、发布服务、协调启停并提供诊断 |

同一插件类型可以创建多个配置不同的实例。实例重新启动时 ID 不变，但运行对象、Scope 和 generation 都会更新。旧代的异步结果不能修改新代状态。

Host 在预检和激活时都执行：

```text
New → JSON 解码 → Spec 契约核对 → 可选 Validate
```

预检对象会被丢弃，因此 `New` 和 `Validate` 必须无副作用；资源只能在 `Start` 中创建。

## 服务、Key 与槽位

服务是插件之间共享的 Go 值，通常是接口。公共契约包定义 `Key[T]`：

```go
type Store interface {
    Put(context.Context, string, []byte) error
}

var StoreKey = gordis.NewKey[Store]("example.store/1")
```

`Key[T].Spec()` 暴露存放在 `PluginSpec.Requires` 和 `PluginSpec.Provides` 中的
`ServiceSpec` 元数据。

提供者在 `PluginSpec.Provides` 中声明 `StoreKey.Spec()`，并在 `Start` 中调用 `Provide`。消费者在 `Requires` 中声明同一个契约，再通过 `Get` 取得固定绑定。

`Plugin` 不等于 Service，`Provides` 也不会自动发布 Plugin。插件可以发布自身、另一个对象或多个服务值。

默认情况下，服务名同时是槽位名。需要多个相同契约的提供者时，应用用 `Outputs` 和 `Inputs` 映射槽位：

```text
store-main  ── example.store/1 → store.primary ──> collector-main
store-audit ── example.store/1 → store.audit   ──> collector-audit
```

插件内部仍只使用 `StoreKey`，不需要知道部署槽位。Host 要求同一槽位只有一个提供者，且两端的契约名称与 Go 类型完全一致。

## 所有权与服务依赖

Gordis 同时维护两种关系：

- `InstanceSpec.Parent`：生命周期所有权，回答“谁随谁一起退出”。
- `PluginSpec.Requires`：服务依赖，回答“谁必须先启动、后清理”。

两者不能互相替代。子实例不会因为设置了 Parent 就自动获得父实例的服务；没有父子关系的实例也可以通过服务依赖协作。Host 将两种关系合成一个无环图，按父级和提供者优先启动，按子级和消费者优先清理。

`Optional: true` 只适用于子实例，表示该子树不参与父实例的 `GroupReady` 判断。它不会放宽服务依赖校验，也不会保护依赖它的其他消费者。

## 状态、就绪与等待

常见实例状态：

```text
registered → starting → ready → stopping → stopped
               │                    │
               └──── failed ────────┘
pending ────────────依赖出现────────> starting
```

- `Ready` 表示实例自身启动成功并已发布服务。
- `GroupReady` 还要求全部必需子实例就绪。
- `AllowPending` 允许动态图中的消费者等待缺失服务；静态 `NewHost` 仍要求完整依赖。
- `Snapshot` 报告绑定、generation、阻塞槽位、任务、租约、停止原因和清理结果。

操作完成与实例就绪不是同一件事。动态变更可能成功提交，但实例仍处于 pending；分别使用 `WaitOperation` 和 `WaitReady`。

## Scope 与资源归属

`Scope` 只属于一个实例的一代：

- `Get` 读取本代启动时固定的服务绑定。
- `Go` 启动受管任务。
- `Acquire` 为已进入的请求持有租约。
- `OnStop` 先关闭入口。
- `Defer` 在任务和租约退出后逆序释放资源。

长期任务使用 Scope 的运行 context，不使用启动 context。服务引用、Scope 和后台 goroutine 都不应逃逸到跨代全局状态。完整顺序见[生命周期](lifecycle.zh.md)。

## 进程外执行

跨进程不会改变业务契约。原生提供者发布真实 Go 对象；`processbridge.Adapter` 在同一个 `Key[T]` 下发布类型化代理：

```text
consumer ── Get(GreeterKey) ──> 本地实现
                            └─> 类型化代理 ── RPC ──> 远端实现
```

Consumer 不知道服务来自哪个进程。进程装配仍需显式 wire binding，因为仅凭服务名无法推导参数序列化、错误映射和取消语义。详见[进程协议](process-protocol.zh.md)。

## 边界

Gordis 核心负责实例图、服务绑定、generation 和生命周期，不负责 HTTP、UI、Agent、包下载、持久化或安全沙箱。应用可以在核心之上实现包注册表、管理 API 和前端扩展系统。
