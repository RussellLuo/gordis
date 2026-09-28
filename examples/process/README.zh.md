# 进程插件

[English](README.md) | 中文

本示例在子进程中运行一个普通的 Local-first Greeter Plugin。可选的进程边界集中在
`greeter/proc`；Plugin ID、配置、类型化 `greeter.Key` 和 consumer 均与 Local 模式相同。

## 运行

在仓库根目录执行：

```sh
go build -o /tmp/gordis-process-host ./examples/process
go build -o /tmp/gordis-process-plugin ./examples/process/cmd/greeter-plugin
/tmp/gordis-process-host /tmp/gordis-process-plugin
```

预期输出：

```text
Hello, Gordis!
```

## 实现原理

`greeter/plugin.go` 定义不依赖任何进程专用能力的普通业务 Plugin。Host 登记
`greeterproc.New` 返回的 Process prototype；通过 `MountReady` 挂载时，adapter 启动 child
executable，并经由 `greeterproc.Serve` 将 `InstanceSpec.Config` 转交给同一个 Plugin。

`greeter/proc` 中的显式 binding 将远端 Greeter 公布为类型化 `greeter.Key`，因此 consumer
仍通过普通的 `gordis.Get` API 使用 Service，无需知道 provider 运行在哪个进程。装配时改为
登记 `new(greeter.Plugin)` 即可切换到 Local 模式，`InstanceSpec`、配置和 consumer 均无需
改变。`run` 返回时，`Host.Shutdown` 会停止并回收子进程。

进程外 Plugin 同时消费和提供类型化 Service 的写法见 [duplex](../duplex/README.zh.md)；一起
交付 backend 和 UI 的示例见 [application-plugin](../application-plugin/README.zh.md)。
