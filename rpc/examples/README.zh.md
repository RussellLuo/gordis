# Gordis RPC 示例

[English](README.md) | 中文

每个示例专注于普通 Gordis Plugin 在子进程中运行的一个方面。建议从 `basic` 开始，
再阅读 `duplex` 和 `stream`。每个目录中链接的 README 都包含运行方式、预期输出和实现说明。

## 进程通信

| 示例 | 重点 |
| --- | --- |
| [basic](basic/README.zh.md) | 类型化远端 Service、程序配置和 Local/process 对照 |
| [duplex](duplex/README.zh.md) | 导入 Host 服务与嵌套回调 |
| [stream](stream/README.zh.md) | 类型化流式结果、完整消费与提前停止 |

## 验证

从 `rpc/` 执行：

```sh
GOWORK=off go test -race -count=1 ./examples/...
```

测试构建并运行真实 Host 和插件程序，检查配置、Local/process 行为、流式结果和依赖边界。
