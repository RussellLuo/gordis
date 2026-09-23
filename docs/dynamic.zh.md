# 动态实例管理

[English](dynamic.md) | 中文

`Preview` / `Apply` 可以在同一个 Host 中新增、更新、停用或删除实例。插件类型目录仍在 `NewHost` 时固定；动态变化的是实例图和配置。

运行 `go run ./examples/dynamic` 可以观察消费者 `pending → ready → pending → ready`，以及恢复后的新 generation。

## 提交变更

```go
plan, err := host.Preview(gordis.Change{
    Upsert: []gordis.InstanceSpec{{
        ID: "collector-alpha", Plugin: "collector",
        AllowPending: true,
        Inputs: map[string]string{SamplerKey.Name(): "sampler.alpha"},
    }},
})
if err != nil {
    return err
}
operationID, err := host.Apply(ctx, plan)
```

`Upsert` 是完整实例描述，不做字段合并。同一描述再次提交表示显式重建或重试。`Disabled` 表示期望状态；依赖恢复不会自动启用被停用的实例。

| API | 作用 |
| --- | --- |
| `Preview(Change)` | 复制配置、校验候选图并计算影响范围，不改变运行状态 |
| `Apply(ctx, plan)` | 拒绝过期计划，排空旧代并提交候选图 |
| `Operation(id)` | 查看阶段、影响范围、目标版本、错误和恢复关联 |
| `WaitOperation(ctx, id)` | 等待操作结束；超时不终止后台清理 |
| `WaitReady(ctx, id)` | 等待实例 ready 或自身 failed |
| `Restore(ctx, id)` | 用完整旧描述提交一次新的恢复操作 |

计划绑定 Host 和图修订号；期间发生其他管理变更会返回 `ErrStale`。管理操作串行执行，重叠请求返回 `ErrBusy`。

## Pending 与删除提供者

静态 `NewHost` 要求完整依赖。动态图中，消费者只有显式设置 `AllowPending` 才能等待缺失服务。

删除仍有消费者的提供者时，还必须允许这些消费者等待：

```go
plan, err := host.Preview(gordis.Change{
    Remove: []string{"sampler-alpha"},
    AllowWaitingConsumers: true,
})
```

Host 先冻结并排空消费者，再撤销提供者。健康消费者进入 `pending`，`BlockedSlots` 指明缺失槽位；新提供者出现后，Host 重新校验图并以新 generation 激活消费者。

`Optional` 只影响父组就绪，不等于允许缺失依赖。缺少父级、契约冲突、重复提供槽位或所有权/依赖成环都会在 Preview 阶段拒绝。

## 提交、就绪与恢复

操作按以下阶段推进：

```text
draining → starting → completed / failed
```

`Committed` 表示候选描述是否已经成为当前图；`completed` 可以包含显式 pending，不代表所有实例 ready。因此管理层应先等待操作，再按需要等待具体实例就绪。

变更使用旧图和新图共同计算影响闭包。整个闭包在第一个停止回调前冻结，未受影响实例保留原 Scope 和 generation。旧任务、租约或 residuals 尚未清理时，不能覆盖服务槽位或启动新代。

`Restore` 不是事务回滚，而是使用旧的完整描述发起一次新变更。它可能再次失败，成功后也会获得新的 generation。后续管理变更会使旧恢复记录过期。

## 边界

当前实例图、操作记录和 generation 历史只保存在内存中。没有持久化、无限重试、原地热配置、并行新旧两代、业务副作用回滚或强制释放 Go goroutine。

实现与行为测试见 [dynamic.go](../dynamic.go) 和 [dynamic_test.go](../dynamic_test.go)；资源清理规则见[生命周期](lifecycle.zh.md)。
