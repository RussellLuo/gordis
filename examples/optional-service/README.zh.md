# 可选服务查询与降级

[English](README.md) | 中文

同一个 reader 使用必需的数据库，并在每次请求时探测可选缓存。程序挂载、卸载缓存的过程
中，reader 始终保持 Ready。两个存储都使用内存中的值，无需外部服务。

## 运行

在仓库根目录执行：

```sh
go run ./examples/optional-service
```

预期输出：

```text
reader generation 1: database:item
reader generation 1: cache:item
reader generation 1: database:item
```

## 实现原理

reader 在 `Requires` 中声明 `databaseKey`，激活时通过 `Get` 取得数据库。
`Activate` 还注入 `lookupCache` 函数，每次请求通过它执行 `scope.Lookup(cacheKey)`。

`readerCh` 将初始化完成的 reader 交给主程序。缓冲让 `Activate` 可以在主程序等待
`MountReady` 时发送对象；`MountReady` 保证请求开始前 reader 已就绪。generation 数字
仅用于输出。

每次请求查到缓存就使用缓存，缺失则查询数据库。挂载缓存后，下次请求使用缓存；卸载后，
请求恢复使用数据库。Lookup 不建立依赖边，因此 reader 始终保持同一 generation。

本例同步执行请求，最后一次请求结束后才 Shutdown。Lookup 不保证缓存存活到后续调用
结束。示例只在缺失时降级；缓存调用失败后是否降级由业务决定。并发请求的停止管理见
[lifecycle](../lifecycle/README.zh.md)。

完整程序见 [main.go](main.go)，完整契约见[插件编写](../../docs/plugin-authoring.zh.md#按需查询可选服务)。
[dynamic](../dynamic/README.zh.md) 展示必需依赖的 provider 变化，
[readiness](../readiness/README.zh.md) 展示条件 child。
