# Gordis

[English](README.md) | 中文

Gordis 是一个用于组合 Go 插件的轻量框架，通过类型化服务连接插件，并按依赖关系管理实例的
启动、停止与资源清理。

## 快速开始

插件通过 `Spec` 描述类型，并在 `Activate` 中执行启动逻辑：

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/RussellLuo/gordis"
)

type greeterPlugin struct{}

func (*greeterPlugin) Spec() gordis.PluginSpec {
	return gordis.PluginSpec{
		ID:  "greeter",
		New: func() gordis.Plugin { return new(greeterPlugin) },
	}
}

func (*greeterPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
	fmt.Printf("%s: Hello, Gordis!\n", scope.ID())
	return nil
}

func main() {
	host, err := gordis.NewHost(new(greeterPlugin))
	if err != nil {
		log.Fatal(err)
	}
	defer host.Shutdown(context.Background())

	if _, err := host.Env().MountReady(context.Background(), gordis.InstanceSpec{
		ID: "greeter", Plugin: "greeter",
	}); err != nil {
		log.Fatal(err)
	}
}
```

运行该程序会输出：

```text
greeter: Hello, Gordis!
```

`NewHost` 注册可用的插件类型；`Env.MountReady` 挂载一个实例并等待它就绪。
`Host.Shutdown` 在程序退出时统一清理已挂载的实例。

## 核心模型

- **Plugin（插件类型）**：可复用的类型，通过 `PluginSpec` 声明标识、构造方式和服务依赖。
- **Instance（插件实例）**：插件类型的一次使用，由 `InstanceSpec` 指定独立的 ID 和配置。
  Host 按依赖关系协调实例的启动和停止。
- **Service（服务）**：通过 `Key[T]` 共享的类型化 Go 值。提供者在 `Provides` 中声明服务，
  用 `scope.Provide` 发布值；消费者在 `Requires` 中声明依赖，用 `scope.Get` 获取服务。
- **Scope（运行作用域）**：管理一次运行中的任务、请求和清理。每次激活都会创建新的插件对象和 Scope。

## 示例

从 [basic](examples/basic/README.zh.md) 开始了解类型化 Service、配置和实例挂载。完整的事件、
隔离、所有权、就绪、动态变更、进程插件与应用集成示例见[示例指南](examples/README.zh.md)。

## 可选扩展

- **事件**：`github.com/RussellLuo/gordis/events` 子包提供插件之间的事件通信，
  见[事件示例](examples/events/README.zh.md)。
- **RPC**：[`github.com/RussellLuo/gordis/rpc` 模块](rpc/README.zh.md)让插件在受信任的
  本地子进程中运行，需要进程执行时再引入；消费者仍通过类型化服务调用。
  [RPC 示例](rpc/examples/README.zh.md)按 `basic → duplex → stream` 阅读。

## 文档

- [核心概念](docs/concepts.zh.md)：建议首先阅读，对象、所有权、Service 依赖与可见性的整体关系。
- [插件编写](docs/plugin-authoring.zh.md)：契约、资源管理与应用装配。
- [生命周期](docs/lifecycle.zh.md)：启动、排空、失败传播与诊断参考。
- [动态实例管理](docs/dynamic.zh.md)：单实例操作、批量变更、等待与恢复。
- [RPC 架构与生命周期](rpc/docs/architecture.zh.md)：进程启动、服务调用、回调与清理。
- [旧 stdio 进程协议](docs/process-protocol.zh.md)：进程事件与 application-plugin 示例仍使用的
  `process`/`processbridge` 实现。

## 能力边界

Gordis 核心只管理 Go 插件图、类型化服务和生命周期。HTTP、UI、Agent、插件包分发和
持久化属于应用层职责。

普通 Service 在受信任的本地子进程中运行时使用 RPC 模块。旧 `process`/`processbridge`
stdio 传输仍用于跨进程事件与 application-plugin 示例。两套协议不兼容，迁移时需要重新构建
Host 和插件程序。

进程执行不构成不可信代码沙箱。两套传输均不提供远程发现、鉴权、断线重连或强制终止 goroutine。

## 致谢

Gordis 的核心设计受到 [Cordis](https://github.com/cordiverse/cordis) 启发，尤其是
服务组合、依赖感知的插件生命周期，以及资源随生命周期所有者统一清理等思想。
Gordis 在 Go 中重新表达了这些思想，是一个独立项目，并非 Cordis 的 Go 移植版或
API 兼容实现。

感谢 Shigma 和 Cordis 的所有贡献者为 Gordis 提供的思想启发。
