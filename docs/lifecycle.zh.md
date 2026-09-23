# 生命周期

[English](lifecycle.md) | 中文

每次实例激活都会创建新的 Plugin 运行对象、generation 和 Scope。本代的服务绑定、任务、请求租约和清理操作都归这个 Scope 所有。

可运行 `go run ./examples/lifecycle` 观察入口关闭、租约排空和资源释放顺序。基本对象关系见[核心概念](concepts.zh.md)。

## 启动

```text
registered / pending / stopped
  → generation + 1
  → New → JSON 解码 → Spec 核对 → Validate
  → 创建 Scope → Plugin.Start
  → 复核父代和服务绑定 → 发布暂存服务 → ready
```

Host 按父级和提供者优先的拓扑顺序启动。`Start` 返回前，插件必须完成必要握手、提交声明的服务并达到可用状态；长期任务交给 `Scope.Go`。

预检与激活使用相同的配置准备流程，但预检对象会被丢弃。`New` 和 `Validate` 不得创建资源，资源只在 `Start` 中创建并登记。

启动失败时，Host 清理本次操作已经启动的实例。失败实例保留 `failed` 和原始错误，未发布的暂存服务不会被消费者看到。可选子树失败会清理该分支及受影响消费者，健康父组可以继续运行，但启动调用仍返回错误。

`Ready` 表示实例自身完成启动；`GroupReady` 还要求全部必需子实例就绪。启动 context 只控制启动调用，长期任务使用 `scope.Context()`。

## 停止

Host 先冻结整个影响闭包，再执行用户回调：

1. 所有受影响 Scope 拒绝新的任务、租约和资源登记。
2. 逆拓扑执行 `OnStop`，关闭路由、监听器或订阅入口。
3. 取消 Scope 的运行 context。
4. 等待 `Scope.Go` 任务和 `Acquire` 租约退出。
5. 逆序执行 `Defer`，最后撤销服务和依赖引用。

这保证消费者先于提供者、子实例先于父实例释放。`OnStop` 只负责关闭入口；数据库连接、进程和其他最终资源放在 `Defer`。

请求入口应使用租约：

```go
release, err := scope.Acquire()
if err != nil {
    return err // 实例正在停止
}
defer release()
```

没有租约保护的裸服务引用不受停止流程保护。清理回调应使用启动时已经取得的固定依赖，不应重新调用 `Get`。

## 超时与残留

停止 context 只限制调用方等待时间，不取消后台清理。任务、请求或回调未退出时，资源、父代和上游服务继续保留；框架不会强制终止 goroutine。

清理回调返回错误或 panic 时，其余回调仍会继续。`cleanupComplete=true` 只表示所有步骤已经返回，不表示报错资源确实释放。存在 residuals 时，Host 保守地阻止同一位置启动新一代，避免覆盖仍可能存活的资源。

重复停止等待同一个清理过程，不重复执行回调。不要在受管任务或清理回调中同步等待自己的 Stop，否则会形成自等待。

## 运行失败

受管任务返回非取消错误，或 Plugin/生命周期回调发生 panic 时，Host 会：

1. 立即冻结故障实例、所有权后代和服务消费者的传递闭包。
2. 取消仍在进行的启动。
3. 通过串行生命周期执行器清理受影响实例。

故障源保留 `failed`、`LastError` 和 `FailurePhase`。没有自身错误的连带实例清理后为 `stopped`，`StopReason` 指向故障源。允许等待的健康消费者可以回到 `pending`，依赖恢复后以新 generation 重新激活。独立实例不受影响。

未通过 `Scope.Go` 管理的 goroutine 不在故障捕获和清理范围内。

## 管理并发与诊断

生命周期操作串行执行；冲突操作返回 `ErrBusy`，相同停止请求加入已有清理。用户回调在 Host 状态锁之外运行，可以读取 Snapshot，但不应在回调内重入并等待新的生命周期操作。

重点诊断字段：

| 字段 | 含义 |
| --- | --- |
| `State` / `GroupReady` | 实例自身及必需子树是否可用 |
| `Generation` / `ParentGeneration` | 当前运行代次及所属父代 |
| `Bindings` / `ProvidedSlots` | 固定的输入提供者和输出槽位 |
| `BlockedBy` / `BlockedSlots` | 未就绪或仍占用资源的依赖 |
| `Tasks` / `Leases` | 尚未退出的任务和请求 |
| `CleanupPhase` / `CleanupComplete` / `Residuals` | 清理进度与失败项 |
| `LastError` / `FailurePhase` / `StopReason` | 故障源和连带停止原因 |

动态变更遵守同一套冻结、排空和残留规则，详见[动态管理](dynamic.zh.md)。`processbridge` 复用同一 Scope 生命周期，并通过 `gordis.process/1` 协调远端清理。
