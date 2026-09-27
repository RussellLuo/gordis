# 所有权与就绪状态

[English](README.md) | 中文

本示例将生命周期所有权与 Service 就绪状态分离：

- `collector.Env().Mount` 使 `metrics` 成为 child，因此它会随该 collector 的精确
  generation 一起被回收。
- `metrics` 依赖 `MetricsBackend`；决定它何时可以激活的是该 Service 依赖，
  而不是它与 parent 的关系。
- backend 暂缺会使 `metrics` 保持 pending，但不会阻止 `collector` 进入 ready。

在仓库根目录执行：

```bash
go run ./examples/readiness
```

预期输出：

```text
collector ready; metrics pending
metrics ready: backend=prometheus
```

主流程从已 ready 的 collector Env 挂载 child，报告预期的 `pending` 状态，然后挂载
backend。Gordis 会自动激活 child，其 `Activate` 方法会输出收到的 backend。这个过程
不需要轮询诊断状态。完整程序见 [main.go](main.go)。
