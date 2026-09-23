# Gordis

[English](README.md) | 中文

Gordis 是一个用于组合 Go 插件的轻量框架，通过类型化服务连接插件，并按依赖关系管理实例的
启动、停止与资源清理。

## 快速开始

插件通过 `Spec` 描述类型，并在 `Start` 中开始工作：

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

func (*greeterPlugin) Start(_ context.Context, scope *gordis.Scope) error {
	fmt.Printf("%s: Hello, Gordis!\n", scope.ID())
	return nil
}

func main() {
	ctx := context.Background()
	host, err := gordis.NewHost(
		[]gordis.Plugin{new(greeterPlugin)},
		[]gordis.InstanceSpec{{ID: "greeter", Plugin: "greeter"}},
	)
	if err != nil {
		log.Fatal(err)
	}
	if err = host.Start(ctx); err != nil {
		log.Fatal(err)
	}
	if err = host.Stop(ctx); err != nil {
		log.Fatal(err)
	}
}
```

运行该程序会输出：

```text
greeter: Hello, Gordis!
```

`InstanceSpec` 添加一个 `greeter` 实例，Host 负责校验、启动和停止。
[basic 示例](examples/basic/README.md)继续引入类型化服务、配置和多个实例。

## 核心模型

```text
Plugin 原型 ──Spec──> PluginSpec
InstanceSpec ──选择──> PluginSpec.ID

PluginSpec + InstanceSpec ──> Host
Host ──激活──> 实例 generation
               ├── Plugin 运行对象
               └── Scope

Parent = 生命周期所有权
Key[T]（Requires/Provides）+ 槽位 = 服务依赖与绑定
```

`PluginSpec` 描述可复用的插件类型；`InstanceSpec` 选择类型并配置具体实例。
Host 校验实例图。每次激活都会创建新的 Plugin 运行对象和 Scope，由 Scope
管理任务、请求和清理。

`Parent` 表达生命周期所有权；`Key[T]`、`Requires`/`Provides` 和槽位表达服务
依赖与绑定。它们共同决定启动和清理顺序。

`processbridge` 可以从子进程提供同一服务契约，而消费者无需改变。

## 示例

这些示例组成一条逐步深入的学习路径：

| 示例 | 重点 |
| --- | --- |
| [basic](examples/basic/README.md) | 类型化服务与实例配置 |
| [composition](examples/composition/README.md) | 多服务槽位与父子所有权 |
| [optional](examples/optional/README.md) | 可选子插件失败与 `GroupReady` |
| [lifecycle](examples/lifecycle/README.md) | 受管任务、请求租约与清理顺序 |
| [dynamic](examples/dynamic/README.md) | `Preview`、`Apply`、pending 与 `Restore` |
| [process](examples/process/README.md) | 由独立构建进程提供的类型化服务 |
| [duplex](examples/duplex/README.md) | Host 与插件通过 stdio 双向调用 |
| [application-plugin](examples/application-plugin/README.md) | Host 启动后交付带后端和 UI 的版本化插件 |

## 文档

- [核心概念](docs/concepts.zh.md)：对象、服务绑定、所有权与依赖。
- [插件编写](docs/plugin-authoring.zh.md)：契约、清理与进程适配。
- [生命周期](docs/lifecycle.zh.md)：启动、排空、失败传播与诊断。
- [动态实例管理](docs/dynamic.zh.md)：运行时变更、等待与恢复。
- [进程协议](docs/process-protocol.zh.md)：双向 RPC 与远端清理。

## 能力边界

Gordis 核心只管理 Go 插件图、类型化服务和生命周期。HTTP、UI、Agent、插件包分发和
持久化属于应用层职责。

进程传输面向受信任的本机 stdio 子进程，不提供远程发现、鉴权、断线重连、原地修改
配置、并行运行新旧 generation 或强制终止 goroutine。

## 致谢

Gordis 的核心设计受到 [Cordis](https://github.com/cordiverse/cordis) 启发，尤其是
服务组合、依赖感知的插件生命周期，以及资源随生命周期所有者统一清理等思想。
Gordis 在 Go 中重新表达了这些思想，是一个独立项目，并非 Cordis 的 Go 移植版或
API 兼容实现。

感谢 Shigma 和 Cordis 的所有贡献者为 Gordis 提供的思想启发。
