# 动态实例管理

[English](dynamic.md) | 中文

`NewHost` 固定静态链接的 Plugin 类型目录，并创建一个空的 synthetic root。应用通过
`host.Env()` 挂载全部实例；`NewHost` 只接收类型目录。

## 挂载与等待

```go
host, err := gordis.NewHost(plugins...)
if err != nil {
    return err
}
defer host.Shutdown(context.Background())

collector, err := host.Env().Mount(ctx, gordis.InstanceSpec{
    ID: "collector", Plugin: "collector", Config: collectorConfig,
})
if err != nil {
    return err // 预检失败，没有提交
}
if err := collector.WaitReady(ctx); err != nil {
    return err
}
```

`Mount` 在 desired instance 提交后返回，不等待 activation。`MountReady` 是
`Mount + WaitReady` 的便利入口；若等待超时或 activation 失败，它会返回非 nil 的已提交
Instance 和对应错误，不会暗中 Unmount。

| API | 语义 |
| --- | --- |
| `Env.Mount` | 校验并提交 root 实例，随后异步收敛 |
| `Env.MountReady` | 挂载并等待该次精确 desired revision 自身 Ready |
| `Instance.Update` | 提交新 JSON 配置并替换 generation |
| `Instance.Restart` | 保持配置不变并替换 generation |
| `Instance.WaitReady` | 等待调用开始时固定的 revision |
| `Instance.Unmount` | 删除 desired state，并在调用方 context 内等待清理完成 |
| `Host.Shutdown` | 永久关闭 root Env 并排空全部实例 |

## Pending 与依赖恢复

required Service 暂缺是正常的动态状态。consumer 提交后无需实例级开关，自动进入
`pending`。匹配的 provider 挂载后，Host 自动激活 consumer。provider 运行失败时，
健康 consumer 会先冻结、排空并回到 `pending`；应用显式 Update 或 Restart provider 后，
consumer 以新 generation 恢复。

Plugin 自身 activation failure 不同：Gordis 保存原始错误并清理该 generation，但不会自动
重试同一个逻辑实例；应用必须显式调用 `Update` 或 `Restart`。

## 隔离的 Service View

零值 View 使用 Service Key 名称作为默认标签。`Env.Isolate` 不修改原 Env，而是为一个
target 派生新标签：

```go
tenantA := host.Env().Isolate(StoreKey, "tenant-a")

reader, err := tenantA.Mount(ctx, gordis.InstanceSpec{
    ID: "reader", Plugin: "reader",
})
_, err = tenantA.Mount(ctx, gordis.InstanceSpec{
    ID: "store", Plugin: "store",
})
```

两个实例都在 `(StoreKey.Name(), "tenant-a")` 解析 `StoreKey`。默认 provider 不会满足隔离的
reader，不同标签下的 provider 也不冲突；consumer 一代运行期间的 provider 与 generation
绑定保持固定。

## 提交与句柄规则

配置会在操作接受前完成解码和校验。context 在接受前取消时不提交任何内容；操作一旦接受，
root 入口会等到提交结果明确，不会返回“可能已提交”的模糊超时。

`WaitReady` 固定逻辑身份与调用时的精确 desired revision，只观察该实例自身，不观察其
ownership descendants。后续 Update 或 Restart 使旧等待
返回 `ErrStale`；Unmount 返回 `ErrClosed`。即使以后复用相同 local ID，旧句柄也永远不会
指向新实例。

`Instance.Unmount` 的 context 只限制调用方等待；操作一旦接受，后台清理仍会完成。需要在
超时后继续观察同一次清理时，应使用 `ChangeSet` 并保留返回的 `Operation`。

Unmount provider 不会删除仍然存活的 consumer。required binding 因此无法解析时，这些
consumer 会先排空再进入 `Pending`，与 provider 运行时失败后的行为一致；重新挂载匹配的
provider 后，Host 会再次自动收敛它们。

## 批量变更与恢复（进阶）

普通应用优先使用前述 `Env` 和 `Instance` 操作。Loader 和其他需要原子修改多个实例的管理面，
再通过 `ChangeSet.Apply → Operation` 提交跨实例变更：

```go
changes := host.Changes()
changes.Mount(tenantEnv, gordis.InstanceSpec{
    ID: "store", Plugin: "store", Config: storeConfig,
})
changes.Update(reader, readerConfig)

operation, err := changes.Apply(ctx)
if err != nil {
    return err
}
mounted := operation.Mounted() // 按 ChangeSet.Mount 的登记顺序
if err := operation.WaitReady(ctx); err != nil {
    return err
}
```

`ChangeSet.Apply` 会在冻结任何现有 scope 前校验完整候选图，并以当前 Host revision、
Instance logical identity 和 owner generation 守护提交。这些检查是框架不变量，不是要求
人工授权的步骤。nil error 只表示候选图已提交；activation 可能仍在进行，也可能因缺少
required binding 正常停在 Pending。`Operation.Wait` 观察本次收敛是否结束，
`Operation.WaitReady` 则等待本次 operation 显式固定的全部 exact revisions Ready，不递归
等待它们的 children。

如果调用方 context 在操作接受后到期，`Apply` 会同时返回非 nil `Operation` 和 context
错误。需要在超时后继续观察的管理面应保留该 Operation，并使用新的 context 调用 `Wait`；
普通请求可以直接返回错误。

影响闭包仍可通过 `Operation.Snapshot` 用于诊断和应用级审计。若应用确实需要审批或 dry-run
策略，可在 Gordis 之上封装该工作流，而无需让每个本地变更都承担这套交互。

同一 ChangeSet 中对旧 Instance 执行 `Unmount`，再从相同 owner Env `Mount` 相同 local ID，
表示原子替换；新实例获得新的 logical identity，旧句柄永久关闭。`Operation.Restore` 是一次
新的显式 operation：只有 Host revision 和 owner generation 仍匹配时才恢复本次操作直接
变更的旧描述，不会覆盖更晚的 Loader 或用户变更。因 owner 停止而被递归移除的 descendant
属于旧 generation，不会被 Restore 直接复活；Plugin 应从新 `Scope.Env()` 重新声明自己的
child，外部 Loader 则在 restored parent Ready 后用新的 `parent.Env()` 重新 reconcile。

Loader 自己保存 entry、owner Env 和 Instance；group/include/overlay/package/disabled 都是
上层配置策略。disabled entry 翻译为 `Unmount`，重新启用时重新 `Mount`。核心不读取配置
文件，也不维护第二棵 Loader runtime graph。

Snapshot 提供 local/qualified identity、phase、generation、固定 binding、阻塞标签、
failure/outcome 与 cleanup 状态。资源顺序和 residual 规则见[生命周期](lifecycle.zh.md)。
