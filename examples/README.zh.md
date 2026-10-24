# Gordis 示例

[English](README.md) | 中文

每个示例专注于插件运行时的一个方面。建议从 `basic` 开始，再随着相关概念的引入
按下列分类继续阅读。每个目录中链接的 README 都包含运行方式、预期输出和实现说明。

## 核心概念

| 示例 | 重点 |
| --- | --- |
| [basic](basic/README.zh.md) | 类型化 Service、JSON 配置和多实例 |
| [isolation](isolation/README.zh.md) | View 隔离和多 provider |
| [ownership](ownership/README.zh.md) | 嵌套 Mount、generation 所有权和递归清理 |
| [readiness](readiness/README.zh.md) | 所有权与 Service 就绪状态的分离 |
| [lifecycle](lifecycle/README.zh.md) | 任务、请求租约、入口撤回和清理 |
| [dynamic](dynamic/README.zh.md) | provider 移除、Pending 转换和 Operation Restore |
| [optional service](optional-service/README.zh.md) | 请求时 Lookup、数据库降级与调用方保持同一 generation |

## 可选扩展

| 示例 | 重点 |
| --- | --- |
| [events](events/README.zh.md) | 类型化 Topic 的订阅与发布 |
| [RPC](../rpc/examples/README.zh.md) | 类型化远端 Service、嵌套回调和流式结果 |
| [RPC events](../rpc/examples/events/README.zh.md) | Host Request、Child Notice 与完整往返等待 |
