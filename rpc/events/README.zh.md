# RPC events

[English](README.md) | 中文

`github.com/RussellLuo/gordis/rpc/events` 通过 Host Bus 路由跨进程类型化通知和 Hook。业务 Plugin 继续使用普通 `events.Bind(scope).On`、`Publish/Parallel` 和 `Emit`；每个 Child 独立持有注册表和 Session peer。Local→Local、Host→Child、Child→Host、Child→Child 以及 Child 自发自收都只经过 Host 路由一次。

## 装配

Topic 放在普通 contract 包；codec 和进程选择放在装配层：

```go
import (
    core "github.com/RussellLuo/gordis/events"
    "github.com/RussellLuo/gordis/rpc"
    rpcevents "github.com/RussellLuo/gordis/rpc/events"
)

var Notice = core.NewTopic[string]("example/notice")

bridge, err := rpcevents.NewBridge(
    rpcevents.BindTopic(Notice, rpcevents.JSONEventCodec[string]("example/notice-json/1"),
        rpcevents.Publish|rpcevents.Subscribe),
)
// 检查 err 后再使用 bridge。

// Host 只声明普通业务边界；Bridge 自动加入私有服务。
plugin, err := bridge.NewProcess(rpc.ProcessOptions{
    PluginID: "notice-process",
    Command: rpc.Command{Path: "/path/to/child"},
    ChildBoundary: rpc.Boundary{},
    Limits: rpc.Limits{
        Business: rpc.AdmissionLimit{Pending: 16, Handling: 16},
        Control: rpc.AdmissionLimit{Pending: 8, Handling: 8},
        Continuation: rpc.AdmissionLimit{Pending: 8, Handling: 8},
    },
})

// Child 保留原业务 Unit 的 Config、Plugin 和实例。
unit, err := bridge.Unit(businessUnit)
// 检查 err 后，将 unit 与明确额度交给 rpc.ServeUnit：
// rpc.ServeUnit(ctx, rpc.ServeUnitOptions{Identity: identity, Unit: unit, Limits: rpc.Limits{
//     Business: rpc.AdmissionLimit{Pending: 16, Handling: 16},
//     Control: rpc.AdmissionLimit{Pending: 8, Handling: 8},
//     Continuation: rpc.AdmissionLimit{Pending: 8, Handling: 8},
// }})
// 不要在这个 Unit 中安装本地 core.Plugin。
```

方向以 Child 为参照：Publish 允许向 Host Bus 派发，Subscribe 允许登记 Child handler。两端契约集合、codec ID 和方向必须完全相同；排序后的声明与 adapter 协议决定私有 Binding 版本，Describe 后、Initialize 前拒绝差异。codec ID 是兼容性声明，不能证明不同 Go 类型或实现兼容；每次操作仍检查准确本地类型及 wire kind/方向/模式。

Host 先挂载本地 Bus 和 observer，再挂载 process plugin。MountReady 成功确认初始化登记的 owner 已在 Host 完成 OwnerReady，committed 订阅已安装；loader AfterReady 发送 Enable。Child 派发同时等待自身 OwnerReady 确认和 Enable，不表示任意业务 AfterReady 任务都已完成。

RPC 并发额度字段 0 表示不限，使用 Bridge 时也一样；NewProcess 和 Unit 不暗中补上限。上述装配与可运行示例在两端直接写出本例额度：Business 16/16、Control/Continuation 8/8，并非库的容量推荐；集成夹具自行声明测试额度。请按负载设置，任意并发额度字段改回 0 即取消该项限制。Host 额度不会配置 Child，Unit 本身不携带 Limits。不限 RPC 数量仍计数并排空；下述订阅/token 上限继续独立生效。

## 所有权与限制

订阅经过 pending → committed → active → removed。Child 保存 handler 与清理责任后才 Commit；运行期订阅可能在 On 返回前收到通知。priority、注册顺序、Global 和 Topic View 由 Host 决定，Global 不绕过 Topic 隔离。Host 原子消耗 Once，并在编码或投递失败时仍确认 Child 映射回收；On/Commit 取消且回滚失败不能返回成功句柄。

取消句柄立即禁止本地投递。普通 `func()` 无法返回远端错误，撤销未确认会使 Session 失败，并在最终 Scope 清理中保留错误。owner 退役撤销其代理并取消在途调用；控制响应不等待所属 owner 自己排空。Session 停止、初始化失败和进程丢失都撤销订阅；重启使用新 Session，旧 client/ID 不转接。

每 Session 最多 1024 条 pending/committed/active 订阅；每端共用最多 128 个 next/base token，过期但仍执行的 token 继续占容量；ID 单调不复用，移除归还容量。RPC 三类预算独立，Dispatch/Deliver 使用 Business，owner/订阅/Enable/撤销/续调过期使用 Control，Continue 使用 Continuation；耗尽恢复 `rpc.ErrBackpressure`，不排队、不重试。自定义 codec 必须可并发调用，编码为合法单个 JSON 值。JSONEventCodec 拒绝未知字段、重复键、尾随内容和不可空类型的 null，指针/map/slice 的 null 合法。

Snapshot 返回普通 RPC 诊断；EventsSnapshot 报告 Host peer 的 owner/订阅、实际 Host dispatch、出站 Deliver、Tokens/Continuing、关闭状态与错误。Deliver caller 返回不代表已取消的 Child handler 实际退出；RPC 实际 handler 计数、Child 节点 lease/清理与 PID/Reaped 必须分别观察。停止仍等待实际 Host handler，强杀保留未知/forced 远端清理。

Hook 选择（Handle、Serial/Bail）和 middleware（Use、Waterfall）分别声明输入/输出 codec：

```go
rpcevents.BindHook(Transform,
    rpcevents.JSONEventCodec[Input]("example/input/1"),
    rpcevents.JSONEventCodec[Output]("example/output/1"),
    rpcevents.Publish|rpcevents.Subscribe)
```

选择保留明确 handled、无 handler sentinel 与业务错误；Waterfall 支持输入变换、短路、结果包装和多 Child 嵌套。next 只可调用一次，middleware 返回时即过期；独立有界 Control 确认先关闭 Host token 准入，再等待已接纳 next，正常过期不取消这部分工作。取消、显式退订、owner 退役和 Session 丢失会取消对应 token；实际退出前仍保留 invocation lease 和 token 容量。已知 sentinel 可跨 wire 恢复，不承诺任意 Go 错误树。

所有 *From 继续由 Host 判断 Topic/来源 View。Child 来源须由实际调用 Scope 提供，并由该 Instance 在 Unit.Exports 显式导出；Host 独立限制为普通 Boundary.Exports，恢复准确 ServiceSpec，再检查 loader.HasProvided。Child 内部未导出服务、另一 owner 的导出不能成为来源地址；Global 绕过来源过滤，但不绕过 Topic 分区。

私有协议为 `2:<registry-digest>`，两端须同步重建。可运行的 Host 与子进程装配见[双向事件示例](../examples/events/README.zh.md)。

## 源码验证

从 `rpc/` 创建临时 workspace，映射本仓库 Core 与 RPC 源码后执行：

```sh
gordis_workspace=$(mktemp -d)
export GOWORK="$gordis_workspace/go.work"
go work init .. .
go test -race -count=1 -timeout=180s ./events
go vet ./events
```

正式独立发行前仍须固定包含新 remote 协作接口的真实 Core 版本。测试以同一个 race 测试二进制运行真实 Child，需要本地 socket 权限。
