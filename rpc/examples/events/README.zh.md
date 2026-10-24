# 双向事件

[English](README.md) | 中文

一次往返使用两个方向的普通 typed Topic：

```text
Host Emit(Request) → Child On(Request)
                        → Publish(Notice) → Host On(Notice) → 打印
Host 等待 Request 返回 → Shutdown
```

本例替代旧 `examples/process-events`，在原 Child→Host Notice 基础上增加 Host 请求。
两端业务 Plugin 继续使用普通 Gordis events API。

## 运行

从 `rpc/` 执行：

```sh
gordis_workspace=$(mktemp -d)
export GOWORK="$gordis_workspace/go.work"
go work init .. .
go build -o /tmp/gordis-rpc-events-host ./examples/events/host/cmd
go build -o /tmp/gordis-rpc-events-plugin ./examples/events/plugin/cmd
/tmp/gordis-rpc-events-host /tmp/gordis-rpc-events-plugin
```

上述命令创建临时 workspace，映射本仓库 Core 与 RPC 源码。发行 go.mod 固定的
Core 版本尚不含新 remote events 协作接口；独立发行构建前须升级为包含它们的真实版本。
无需本地 replace 或永久 workspace。

唯一业务输出：

```text
received notice: hello
```

CLI 只接收插件程序路径，RPC 诊断写入 stderr。挂载和等待 Request 使用有界
context；deferred Shutdown 使用独立超时并传播清理错误。

## 普通业务与装配

[contract](contract/notice.go) 定义 Request、Notice 和 typed Topic。
[Child 业务](plugin/plugin.go) 订阅 Request，在 handler 内 Publish Notice。
[Host 业务](host/plugin.go) 在 Notice handler 中直接打印，并在 AfterReady 任务中
调用 Emit。这些包仅依赖 Gordis/events 和普通契约，不依赖 RPC 运行库或 bindings；
Host 不导入 Child 业务实现。

[bindings](bindings/notice.go) 在两端创建同一 Bridge。方向以 Child 为参照：
Request 允许 Subscribe，Notice 允许 Publish；每个 Topic 显式声明稳定 JSON codec ID。
事件声明手写，本例没有 rpcgen 步骤。

[Host 入口](host/cmd/main.go) 依次挂载 Bus、Bridge process adapter 和 observer。
observer 启动前，Child 的 Request 订阅已经安装；AfterReady 保证 observer 已能
发送事件和接收 Notice。一个带缓冲的 channel 报告 Emit 结果，让 CLI 等请求任务
结束后再 Shutdown；MountReady 本身不等待 AfterReady 任务完成。Request 使用
顺序 Emit，嵌套的 Notice 回调不依赖并行 Bus 的额外容量。

[Child 入口](plugin/cmd/main.go) 对一个普通业务实例调用 Bridge.Unit，再 ServeUnit。
没有普通 Service Imports/Exports，也没有程序配置；桥自动加入真实私有 endpoint 和
remote Bus，Child 不挂本地 events.Plugin。

打印表示 Notice 已收到；此后 Request 如果报错，CLI 仍失败退出。传输额度和故障
场景由专门的库测试覆盖。

## 验证

在上述已配置 workspace 的终端中，从 `rpc/` 执行：

```sh
go test -race -count=1 -timeout=180s ./examples/...
go test -race -run '^TestBridgeNestedNotificationCompletion$' -count=10 -timeout=180s ./events
go vet ./examples/...
```

测试用 `-race` 构建真实 Host/Child，检查唯一输出、成功退出和生产依赖边界。
专门的 TestBridgeNestedNotificationCompletion 库测试在收到 Notice 后阻塞 handler，
验证原始 Emit 仍等待其完成，并在释放后检查错误传播。Hook、路由、取消和实际清理
矩阵同样由[事件库测试](../../events/README.zh.md)负责。
