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

## 项目组成

| 组成 | 用途 |
| --- | --- |
| Core | `github.com/RussellLuo/gordis` 模块，提供插件装配、类型化服务与生命周期管理。 |
| [Events](examples/events/README.zh.md) | Core 模块中的 `events` 子包，提供插件间的事件通信。 |
| [RPC](rpc/README.zh.md) | 独立的 `github.com/RussellLuo/gordis/rpc` 模块，让插件在受信任的本地子进程中运行，支持类型化服务和事件通信。 |

HTTP、UI、Agent、插件包分发和持久化属于应用层职责。

## 继续学习

1. [basic 示例](examples/basic/README.zh.md)：在插件之间共享类型化服务，并为各个实例配置参数。
2. [核心概念](docs/concepts.zh.md)：理解对象、所有权、服务依赖与可见性。
3. [插件编写](docs/plugin-authoring.zh.md)：定义契约、管理资源与装配应用。

按需查阅[生命周期](docs/lifecycle.zh.md)和[动态实例管理](docs/dynamic.zh.md)。
更多隔离、所有权、就绪、动态变更及可选扩展示例见[示例指南](examples/README.zh.md)。

## 致谢

Gordis 的核心设计受到 [Cordis](https://github.com/cordiverse/cordis) 启发，尤其是
服务组合、依赖感知的插件生命周期，以及资源随生命周期所有者统一清理等思想。
Gordis 在 Go 中重新表达了这些思想，是一个独立项目，并非 Cordis 的 Go 移植版或
API 兼容实现。

感谢 Shigma 和 Cordis 的所有贡献者为 Gordis 提供的思想启发。
