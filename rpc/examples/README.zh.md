# Gordis RPC 示例

[English](README.md) | 中文

每个示例专注于普通 Gordis Plugin 在子进程中运行的一个方面。建议从 `basic` 开始，
再阅读 `duplex`、`stream` 和 `events`。每个目录中链接的 README 都包含运行方式、预期输出和实现说明。

可运行示例只展示核心业务流程。并发、错误传播和清理保证放在专门的库测试中；
示例保留正常运行所需的超时与 Shutdown。

## 进程通信

| 示例 | 重点 |
| --- | --- |
| [basic](basic/README.zh.md) | 类型化远端 Service、程序配置和 Local/process 对照 |
| [duplex](duplex/README.zh.md) | 导入 Host 服务与嵌套回调 |
| [stream](stream/README.zh.md) | 类型化流式结果、完整消费与提前停止 |
| [events](events/README.zh.md) | 双向事件桥上的 Host Request 与 Child Notice |

## 验证

从 `rpc/` 执行：

```sh
gordis_workspace=$(mktemp -d)
export GOWORK="$gordis_workspace/go.work"
go work init .. .
go test -race -count=1 -timeout=180s ./examples/...
```

测试构建并运行真实 Host 和插件程序，检查配置、Local/process 行为、流式结果、事件往返完成
和依赖边界。临时 workspace 映射当前 Core/RPC 源码；独立发行前仍须升级
固定的 Core 依赖，使其包含新 events 协作接口。
