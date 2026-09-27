# 隔离的服务实例

[English](README.md) | 中文

本示例将两个 consumer 连接到同一类型化服务的不同 provider。

```text
store-main  ── store.main  ──> collector-main
store-audit ── store.audit ──> collector-audit
```

## 运行

在仓库根目录执行：

```sh
go run ./examples/isolation
```

预期输出：

```text
collector-main -> store-main
collector-audit -> store-audit
```

## 实现原理

`Env.Isolate` 将 `storeKey` 分别映射到 `store.main` 和 `store.audit`。通过同一 Env
挂载的 provider 和 consumer 使用同一 label，因此每个 collector 都会收到预期的 store，
而无需向任一插件添加部署 label。

四个实例都是相互独立的 root 实例。隔离改变的是服务可见性，不会创建生命周期所有权。

完整程序见 [main.go](main.go)。
