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

var StoreKey = gordis.NewKey[Store]("example.store/1")
```

`StoreKey.Spec()` 用于 `PluginSpec.Requires` / `Provides`；`Provide` 提交运行时值，`Get`
读取 consumer generation 的固定 binding。

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
    store, err := gordis.Get(scope, StoreKey)
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
return gordis.Provide[Store](scope, StoreKey, store)
```

这些 API 分别处理依赖、工作准入和停止清理：

- `gordis.Get(scope, key)` 是包级泛型函数，在 `Activate` 中取得本代的固定依赖；它不是
  Scope 方法。
- `scope.Go(name, fn)` 创建并管理长期任务；非取消错误会使本代失败。任务必须等本代 Ready
  后才能开始时使用 `scope.AfterReady(name, fn)`。
- `scope.Acquire()` 为调用方正在执行的工作持有租约，但不创建 goroutine；调用方必须
  `defer release()`。只允许 Ready 后调用的入口使用 `scope.AcquireReady()`。
- `scope.OnStop(name, fn)` 在任务取消前关闭 listener、route 或 subscription 等入口。
- `scope.Defer(name, fn)` 在任务和租约排空后逆序释放最终资源。

不要让 Scope、Service 或未受管 goroutine 跨 generation 逃逸。清理登记被拒绝时，调用方仍
拥有刚取得的资源，必须立即自行释放。

## 4. 在应用中装配并隔离 root 实例

前面三节是 Plugin 作者的核心路径；类型登记、root 实例挂载和部署标签由应用装配层负责。
应用先登记固定 Plugin 类型目录，再挂载 desired instance：

```go
host, err := gordis.NewHost([]gordis.Plugin{storePlugin, new(Writer)})
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
var Changed = events.NewTopic[string]("example.changed/1")

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

业务 Plugin 仍使用相同的 `Spec`、`Activate`、`Get`、`Provide` 和 Scope API。装配层使用
`processbridge.Adapter`，子进程使用 `processbridge.Serve`，显式 `Binding` 定义 JSON DTO
与方法 wire 行为。

Adapter 使用业务 Plugin 的逻辑 ID 注册；consumer 继续依赖同一个 `Key[T]`，Adapter 在
`Activate` 中发布类型化代理。只有请求/响应、取消和错误语义清晰的契约适合跨进程。详见
[进程协议](process-protocol.zh.md)。

跨进程事件同样不改变业务 Plugin：它仍使用 `events.Bind(scope)`。装配层在 Adapter 与
Serve 两侧用相同的 `BindTopic/BindHook` 登记显式 `EventCodec` 和 Publish/Subscribe 方向；
纯本地 Event 不需要 wire。远端不能用 `Scope.Env().Mount` 建立 Host 不可见的 child。
