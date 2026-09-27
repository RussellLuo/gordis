# 双向进程调用

[English](README.md) | 中文

本示例展示单个 stdio session 上的一次嵌套调用。Host 调用插件的 `add(2)` 方法。
插件在处理该请求时又调用 Host 的 `base()` 方法，收到 `40` 后返回 `42`。

它直接使用进程协议，不涉及 UI、HTTP 或 Gordis 服务图。

## 运行

在仓库根目录执行：

```sh
go build -o /tmp/gordis-duplex-host ./examples/duplex
go build -o /tmp/gordis-duplex-plugin ./examples/duplex/plugin
/tmp/gordis-duplex-host /tmp/gordis-duplex-plugin
```

预期输出：

```text
Host → plugin: add(2)
plugin → Host: base()
result: 42; plugin process reaped
```

## 实现原理

Host 在 `process.Options` 中提供 callback handler，并通过 `client.Call` 调用插件。
插件通过 `process.Handler.Call` 处理该请求，并使用获得的 `process.Caller` 回调 Host。
双方共享 contract 包中的方法名和身份元数据。协议消息使用 stdout；插件日志应使用 stderr。

关键文件：

- [main.go](main.go)：启动进程并处理 Host callback。
- [plugin/main.go](plugin/main.go)：处理插件调用并回调 Host。
- [contract/contract.go](contract/contract.go)：共享协议标识符。

由子进程支撑的类型化 Gordis 服务见 [process](../process/README.zh.md)；协议细节见
[进程协议](../../docs/process-protocol.zh.md)。
