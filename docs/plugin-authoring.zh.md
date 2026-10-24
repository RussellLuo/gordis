# 编写插件

[English](plugin-authoring.md) | 中文

本文覆盖当前 Local-first 作者模型。对象关系见[核心概念](concepts.zh.md)，精确清理规则见
[生命周期](lifecycle.zh.md)。

## 1. 定义公共契约

provider 与 consumer 共同依赖一个轻量契约包：

```go
type Store interface {
    Put(context.Context, string, []byte) error
}

var StoreKey = gordis.NewKey[Store]("example/store")
```

`StoreKey.Spec()` 用于 `PluginSpec.Requires` / `Provides`；`scope.Provide` 提交运行时值，
`scope.Get` 读取 consumer generation 的固定 binding。`scope.HasProvided(StoreKey.Spec())`
检查本代是否声明并成功发布了该服务，不查询其他 provider；暂存值在发布提交前返回 false。

## 2. 实现 Plugin

插件结构体保存解码后的实例配置，并实现稳定的 `Spec` 元数据与本代 `Activate`：

```go
type Writer struct {
    Path string `json:"path"`
    Text string `json:"text"`
}

func (*Writer) Spec() gordis.PluginSpec {
    return gordis.PluginSpec{
        ID:       "writer",
        Requires: []gordis.ServiceSpec{StoreKey.Spec()},
        New:      func() gordis.Plugin { return new(Writer) },
    }
}

func (p *Writer) Validate() error {
    if p.Path == "" {
        return errors.New("path is required")
    }
    return nil
}

func (p *Writer) Activate(ctx context.Context, scope *gordis.Scope) error {
    store, err := scope.Get(StoreKey)
    if err != nil {
        return err
    }
    return store.Put(ctx, p.Path, []byte(p.Text))
}
```

约束：

- `Spec` 的 ID、Requires 和 Provides 不随配置变化。
- `New` 每次返回新的非 nil、实现 `Activate` 的对象，只设置确定性的默认值。
- `Validate` 可重复、无副作用。
- `Activate` 成功返回时，插件必须已可用并提交全部声明的 Service。
- 长期任务通过 `scope.Go` 使用 `scope.Context()`，不用 activation context。
- `Config` 是单个 JSON object（空值视为 `{}`）；标准结构体解码会拒绝未知字段。

Host 在预检时创建 preparation object，核对它的 `Spec`、配置、可选 `Validate` 和 `Activate`
能力后丢弃；实际激活时再创建独立的运行对象。

## 3. 用 Scope 管理资源

在 `Activate` 中取得资源、立即登记清理，最后发布：

```go
store, err := openStore()
if err != nil {
    return err
}
if err := scope.Defer("store", func(context.Context) error {
    return store.Close()
}); err != nil {
    _ = store.Close()
    return err
}
return scope.Provide[Store](StoreKey, store)
```

这些 API 分别处理依赖、工作准入和停止清理：

- `scope.Provide(key, value)` 暂存已声明的服务，待 `Activate` 成功后发布。
  `PluginSpec.Provides` 声明契约，`scope.HasProvided(key.Spec())` 检查本代已提交的发布。
- `scope.Get(key)` 是 Scope 的泛型方法，在 `Activate` 中取得本代的固定依赖。
- `scope.Lookup(key)` 在本代 View 中探测可选服务，无需声明 `Requires`，不建立依赖边，
  也不取得租约。
- `scope.Go(name, fn)` 创建并管理长期任务；非取消错误会使本代失败。任务必须等本代 Ready
  后才能开始时使用 `scope.AfterReady(name, fn)`。
- `scope.Acquire()` 为调用方正在执行的工作持有租约，但不创建 goroutine；调用方必须
  `defer release()`。只允许 Ready 后调用的入口使用 `scope.AcquireReady()`。
- `scope.OnStop(name, fn)` 在任务取消前关闭 listener、route 或 subscription 等入口。
- `scope.Defer(name, fn)` 在任务和租约排空后逆序释放最终资源。

不要让调用方的 Scope、固定依赖或未受管 goroutine 跨其自身 generation 逃逸。清理登记被
拒绝时，调用方仍拥有刚取得的资源，必须立即自行释放。

### 按需查询可选服务

必需的数据库使用 `Requires + Get`，可能缺失的缓存使用 `Lookup`。在 `Activate` 中注入
查询函数，每次请求调用该函数：

```go
type ValueStore interface {
    Get(context.Context, string) ([]byte, error)
}

var DatabaseKey = gordis.NewKey[ValueStore]("example/database")
var CacheKey = gordis.NewKey[ValueStore]("example/cache")

type readerPlugin struct {
    database    ValueStore
    lookupCache func() (ValueStore, bool, error)
}

func (*readerPlugin) Spec() gordis.PluginSpec {
    return gordis.PluginSpec{
        ID:       "reader",
        Requires: []gordis.ServiceSpec{DatabaseKey.Spec()},
        New:      func() gordis.Plugin { return new(readerPlugin) },
    }
}

func (p *readerPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
    database, err := scope.Get(DatabaseKey)
    if err != nil {
        return err
    }
    p.database = database
    p.lookupCache = func() (ValueStore, bool, error) {
        return scope.Lookup(CacheKey)
    }
    return nil
}

func (p *readerPlugin) Get(ctx context.Context, id string) ([]byte, error) {
    cache, found, err := p.lookupCache()
    if err != nil {
        return nil, err
    }
    if found {
        return cache.Get(ctx, id)
    }
    return p.database.Get(ctx, id)
}
```

查询函数捕获本代 Scope，每次调用都重新执行 Lookup。应用应在初始化完成后发布请求入口，
Ready 后才接受请求。并发入口需要在 reader 清理前拒绝新请求并等待已有请求结束；请求租约
的用法见[生命周期](lifecycle.zh.md)。这不延长可选 cache provider 的生命周期。示例只在
缓存缺失时降级；缓存调用失败后是否降级，由业务根据错误类型和请求取消状态决定。

运行[可选服务示例](../examples/optional-service/README.zh.md)，可以观察同一代 reader 的
数据库 → 缓存 → 数据库请求路径。

`Lookup` 返回 `(value, found, err)`，只查询调用方固定 View 对应的标签，不会寻找其他标签
作为降级。缓存缺失时 reader 保持 Ready；缓存出现、移除或替换都不会重启它。Key 的名称与
准确 Go 类型必须匹配 provider 契约。

| 返回结果 | 含义 |
| --- | --- |
| 零值、`false`、`nil` | 该标签没有已发布且 Ready 的 provider，或 provider 已冻结/正在停止 |
| 服务值、`true`、`nil` | 查询这一刻当前 provider 可用 |
| 零值、`false`、错误 | Key 无效或契约类型不匹配；nil/关闭/冻结调用方返回 `ErrClosed`；过期 generation 返回 `ErrStale`；有效但不带本地 Host 的 detached Scope 返回 `ErrLookupUnsupported` |

调用方必须是 Host 中当前有效、未冻结的 generation。`Activate` 中可以查询其他 Ready
provider，自身 `Provide` 暂存的值要等 Ready 发布后才能查到。未声明的 `Get` 仍报错，即使
`Lookup` 能找到该服务。

```text
请求取得 cache → provider 被卸载 → 请求继续调用旧 cache
```

卸载后新查询不再返回旧 provider。已取得的对象仍是普通 Go 值，不自动跟随替换，也不会
自动失效；其方法可能继续成功，也可能因资源关闭或取消而失败。框架不保证这些调用成功，
也不等待它们结束才卸载 provider。需要 provider 存活到 consumer 清理结束时，使用
`Requires + Get`。

后台功能需要随服务出现而启动、消失而停止时，挂载一个声明该 `Requires` 的已登记 consumer
child。服务缺失时 child 保持 Pending，出现后取得独立生命周期。child 的 Pending 或失败
不改变 parent readiness，ownership 不隐含 readiness 依赖。这表达条件子功能；请求路径中的
缺失降级使用 `Lookup`。

## 4. 在应用中装配并隔离 root 实例

前面三节是 Plugin 作者的核心路径；类型登记、root 实例挂载和部署标签由应用装配层负责。
应用先登记固定 Plugin 类型目录，再挂载 desired instance：

```go
host, err := gordis.NewHost(storePlugin, new(Writer))
if err != nil {
    return err
}
defer host.Shutdown(context.Background())

tenant := host.Env().Isolate(StoreKey, "tenant-a")
writer, err := tenant.Mount(ctx, gordis.InstanceSpec{
    ID: "writer", Plugin: "writer",
    Config: json.RawMessage(`{"path":"hello","text":"world"}`),
})
if err != nil {
    return err
}
_, err = tenant.Mount(ctx, gordis.InstanceSpec{ID: "store", Plugin: "store"})
if err != nil {
    return err
}
return writer.WaitReady(ctx)
```

consumer 可以先挂载，并在匹配 provider 出现前自动保持 Pending。`Env.Isolate` 同时调整该 Key
的消费与提供路由，plugin 代码无需知道部署标签。

后续控制使用 `Instance.Update`、`Restart`、`WaitReady` 与 `Unmount`。Plugin 可通过
`Scope.Env()` 挂载由当前 generation 拥有的 child；parent 停止或换代时会递归回收旧 child。
详见[动态管理](dynamic.zh.md)。

## 5. 可选 EventBus

应用把 `&events.Plugin{}` 登记到 Host 类型目录，并挂载一个 `events.PluginID` 实例。事件
使用者在 `Requires` 中声明 `events.BusKey.Spec()`，再绑定当前 generation 的 Scope：

```go
var Changed = events.NewTopic[string]("example/changed")

func (*observerPlugin) Spec() gordis.PluginSpec {
    return gordis.PluginSpec{
        ID:       "observer",
        Requires: []gordis.ServiceSpec{events.BusKey.Spec()},
        New:      func() gordis.Plugin { return new(observerPlugin) },
    }
}

func (*observerPlugin) Activate(_ context.Context, scope *gordis.Scope) error {
    _, err := events.Bind(scope).On(Changed, observe,
        events.Priority(10), events.Once())
    return err
}
```

订阅归 subscriber Scope 所有，并在清理时自动撤销。`Publish` 使用有界并行投递；`Emit` 按
稳定顺序串行调用；`Serial/Bail` 选择第一个显式 handled 的结果；`Waterfall` 用只能在当前
middleware 返回前调用一次的 `Next` 组合 middleware。已经在返回前准入的 `Next` 会被纳入
本次分发并排空；返回后保存并调用它会得到 `events.ErrNextExpired`。`Env.Isolate` 可以独立
分区 Topic，不会连带改变 Service 标签。
调用方必须得到某个确定响应者时，应使用普通 Service。

## 6. 进程外插件

业务 Plugin 仍使用相同的 `Spec`、`Activate`、`Get`、`Provide` 和 Scope API。普通 Service
使用 [RPC 模块](../rpc/README.zh.md)：

- 普通接口、类型化 Key 和 DTO 放在 contract 包；Binding 生成到独立包，普通业务包不依赖 RPC。
- Host 通过 `rpc.NewProcess` 登记命令、程序身份和预期 ChildBoundary。Imports 对应
  Requires，Exports 对应 Provides。
- 子进程通过 `rpc.ServeUnit` 声明普通 Plugin、实例、程序配置、Imports 和 Exports。
  Unit 在子进程中运行自己的本地 Host。

consumer 继续依赖同一个 `Key[T]`；process Plugin 在远端 Ready 后发布全部导出代理。
只有请求/响应、取消和错误语义清晰的契约适合跨进程。从
[basic](../rpc/examples/basic/README.zh.md) 开始，再阅读
[duplex](../rpc/examples/duplex/README.zh.md) 和
[stream](../rpc/examples/stream/README.zh.md)。

`Lookup` 只查询调用方本地 Host 的 View。子进程通过显式 Imports 和固定 `Get` 绑定访问
外部 Host 服务；RPC 不提供跨进程动态查询或外部 Host 图的管理。Unit 装配当前是闭合静态图，
详见 [RPC 核心概念](../rpc/docs/concepts.zh.md)。

跨进程事件使用 [rpc/events](../rpc/events/README.zh.md)：普通 Plugin 保持 On/Publish/Emit
和 Hook API，装配层声明 codec 与方向。参见[双向事件示例](../rpc/examples/events/README.zh.md)，
停止前等待 Request 派发及其嵌套 Notice 投递完成。
旧 stdio adapter 与 application-plugin 示例已删除。包安装、版本升级、UI 激活与
manifest 校验随应用退役；RPC 不提供替代包管理 API。
