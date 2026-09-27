# 动态实例管理

[English](README.md) | 中文

本示例通过 `ChangeSet` 修改一个运行中的 Host。它会移除 ready consumer 的 provider，
观察 consumer 回到 `pending`，再使用 `Operation.Restore` 以新 generation 恢复两者。

## 运行

在仓库根目录执行：

```sh
go run ./examples/dynamic
```

预期输出：

```text
consumer generation 1 reads source generation 1
consumer pending: source removed
consumer generation 2 reads source generation 2
```

## 实现原理

`NewHost` 注册固定的插件类型目录。root Env 挂载 `source` 和 `consumer`，并等待两者
进入 ready，从而建立控制面变更要修改的运行状态。

移除 provider 时先调用 `ChangeSet.Unmount`，再调用 `ChangeSet.Apply`；存活的 consumer
会回到 pending，而不是被删除。返回的持久 `Operation` 首先用于等待收敛，随后 `Restore`
将已移除的描述重新应用为新 operation，产生新的 source 和 consumer generation。consumer
每次激活都会输出 ready generation；移除 operation 完成后，主流程会报告预期的
`pending` 转换，无需轮询诊断状态。

完整程序见 [main.go](main.go)；operation 语义见[动态实例管理](../../docs/dynamic.zh.md)。
