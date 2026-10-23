# Gordis

[English](README.md) | 中文

Gordis 是一个用于组合 Go 插件的轻量框架，通过类型化服务连接插件，并按依赖关系管理实例的
启动、停止与资源清理。

## 模块

- 根模块 `github.com/RussellLuo/gordis` 提供插件组合、生命周期与可选的 `events` 子包。
- [RPC](rpc/README.zh.md) 模块 `github.com/RussellLuo/gordis/rpc` 通过 go-plugin/gRPC 在本地子进程运行 Gordis 插件，拥有独立的 go.mod、依赖、示例和发行标签（`rpc/v…`）。Core 不依赖 RPC。

## 快速开始

插件通过 `Spec` 描述类型，并在 `Activate` 中启动一代运行：

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

`NewHost` 固定静态链接的 Plugin 类型目录；`Env.MountReady` 挂载一个 root 实例并等待
该次精确 desired revision。`Host.Shutdown` 在程序退出时统一清理已挂载的实例。

## 核心模型

```text
Plugin 原型 ──Spec──> PluginSpec
NewHost（Plugin 类型）──> Host ──> synthetic root Env
root Env + InstanceSpec ──Mount──> 逻辑 Instance
                                      └── generation
                                          ├── Plugin 运行对象
                                          └── Scope

Key[T]（Requires/Provides）+ Env View = 固定服务绑定
```

`PluginSpec` 描述可复用的插件类型；`InstanceSpec` 选择类型并配置一个 root 实例。
每次激活都会创建新的 Plugin 运行对象和 Scope，由 Scope 管理任务、请求和清理。
required Service 暂缺时逻辑 Instance 自动进入 `pending`；挂载 provider 后 Host 自动收敛。

`Env.Isolate` 派生不可变的可见性 View。`Key[T]` 与 `Requires`/`Provides` 决定依赖和清理
顺序，View 则选择一代运行期间固定的 provider 标签。

ownership 与 readiness 明确分离：child 随 owner generation 换代和清理；实例是否能 Ready
只由显式声明的 Service 依赖决定。

管理面可以通过 `Host.Changes()` 原子提交跨实例变更。可选的 `events` 子包与
[RPC 模块](rpc/README.zh.md)沿用相同的 Service、Scope 和生命周期模型；核心不读取配置文件或维护另一棵 Loader 图。

## 示例

从 [basic](examples/basic/README.zh.md) 开始了解类型化 Service、配置和实例挂载。完整的事件、
隔离、所有权、就绪、动态变更、进程插件与应用集成示例见[示例指南](examples/README.zh.md)。

普通 Service 的子进程运行方式见 [RPC 示例](rpc/examples/README.zh.md)，按
`basic → duplex → stream` 阅读。

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
