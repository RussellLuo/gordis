# 编写插件

[English](plugin-authoring.md) | 中文

本文给出插件作者需要的最短路径。对象关系见[核心概念](concepts.zh.md)，精确启停规则见[生命周期](lifecycle.zh.md)。

## 1. 定义公共契约

提供者和消费者共同依赖一个轻量契约包：

```go
type Store interface {
    Put(context.Context, string, []byte) error
}

var StoreKey = gordis.NewKey[Store]("example.store/1")
```

`StoreKey.Spec()` 用于 `Requires` / `Provides` 声明，`StoreKey.Name()` 用于 `Inputs` / `Outputs` 槽位映射。实际服务值仍由 `Provide` 提交。

## 2. 实现 Plugin

插件结构体保存实例配置，并实现稳定的 `Spec()` 与本代的 `Start()`：

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

func (p *Writer) Start(ctx context.Context, scope *gordis.Scope) error {
    store, err := gordis.Get(scope, StoreKey)
    if err != nil {
        return err
    }
    return store.Put(ctx, p.Path, []byte(p.Text))
}
```

应用注册一个原型，并用 `InstanceSpec` 创建实例：

```go
host, err := gordis.NewHost(
    []gordis.Plugin{new(Writer), storePlugin},
    []gordis.InstanceSpec{{
        ID: "writer-main", Plugin: "writer",
        Config: json.RawMessage(`{"path":"hello","text":"world"}`),
    }},
)
```

遵守以下约束：

- `Spec()` 的 ID、Requires 和 Provides 不随配置变化。
- `New()` 每次返回全新的非 nil 对象，只设置确定性的默认值。
- `Validate()` 可重复、无副作用，不打开资源或启动任务。
- `Start()` 返回成功时必须已经可用，并提交全部声明的服务。
- Plugin 与 Service 是两个概念；插件可发布自身，也可发布独立对象。

Host 会在预检与实际激活时分别创建、解码和校验对象。预检对象总会被丢弃。

## 3. 提供服务并管理资源

提供者在 `Start` 中创建资源、立即登记清理，最后发布服务：

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

常用 Scope API：

- `scope.Go(name, fn)`：启动受管任务；非取消错误会使实例失败。
- `scope.OnStop(name, fn)`：停止入口、监听器或订阅。
- `scope.Acquire()`：保护已经进入的请求，停止会等待租约释放。
- `scope.Defer(name, fn)`：最终逆序释放资源。
- `scope.Context()`：长期任务的运行 context。

不要使用启动 context 管理长期任务，也不要把服务、Scope 或未受管 goroutine 保存到跨代全局状态。资源创建后若登记失败，调用方仍拥有资源，必须立即自行回收。

## 4. 组合多个实例

同一服务有多个提供者时，由应用映射槽位：

```go
specs := []gordis.InstanceSpec{
    {ID: "store-main", Plugin: "store", Outputs: map[string]string{StoreKey.Name(): "store.primary"}},
    {ID: "store-audit", Plugin: "store", Outputs: map[string]string{StoreKey.Name(): "store.audit"}},
    {ID: "writer-main", Plugin: "writer", Inputs: map[string]string{StoreKey.Name(): "store.primary"}},
    {ID: "writer-audit", Plugin: "writer", Inputs: map[string]string{StoreKey.Name(): "store.audit"}},
}
```

插件只使用公共 Key，不知道部署槽位。`Parent` 用于生命周期归属，不能替代 Requires。附加能力可以设置 `Parent` 与 `Optional: true`；它失败时不影响父组 `GroupReady`，但 Start 仍返回错误，服务依赖也不会变成可选。

## 5. 动态实例

运行中的 Host 通过 `Preview` / `Apply` 提交完整 `InstanceSpec`。允许等待缺失服务的消费者必须设置 `AllowPending`；删除提供者并保留消费者时，还要在 `Change` 中设置 `AllowWaitingConsumers`。

变更可能已提交但实例仍在等待，因此分别使用 `WaitOperation` 和 `WaitReady`。详细语义见[动态管理](dynamic.zh.md)。

## 6. 进程外插件

业务 Plugin 仍只使用根包的 `Spec`、`Start`、`Get`、`Provide` 和 Scope API。装配层使用 `processbridge.Adapter`，独立程序使用 `processbridge.Serve`，双方通过显式 `Binding` 描述可跨进程的方法和 JSON DTO。

Adapter 应注册为业务的逻辑插件 ID，而不是 `greeter-process` 之类的执行模式 ID。Consumer 始终依赖相同的 `Key[T]`；进程代理由 Adapter 在 `Start` 时发布。

只有具备明确请求/响应、取消和错误语义的契约才适合跨进程。进程退出不等于业务清理成功；完整边界见[进程协议](process-protocol.zh.md)。

自带后端与 UI、且需要在 Host 启动后晚安装的应用，可以统一实现一个稳定的应用契约。应用 Plugin 在远端 Scope 内启动自己的 HTTP、gRPC 或其他数据面，再通过 `Endpoint` 服务发布私有监听地址；Host 只提供通用进程 Adapter、协议 Gateway 和 UI Loader，不需要知道应用的业务路由。数据面请求仍必须持有远端 Scope 租约，确保 `OnStop` 关闭入口后再排空请求和清理 Server。见 [application-plugin](../examples/application-plugin/README.md)。

## 示例索引

| 示例 | 重点 |
| --- | --- |
| [basic](../examples/basic/README.md) | 公共服务与实例配置 |
| [composition](../examples/composition/README.md) | 槽位、Parent 与多实例 |
| [optional](../examples/optional/README.md) | 可选子树与整组就绪 |
| [lifecycle](../examples/lifecycle/README.md) | 任务、租约和清理顺序 |
| [dynamic](../examples/dynamic/README.md) | 运行时装配与恢复 |
| [process](../examples/process/README.md) | 类型化进程服务 |
| [duplex](../examples/duplex/README.md) | Host 与插件双向调用 |
| [application-plugin](../examples/application-plugin/README.md) | 自带数据面后端与 Host UI 的应用插件 |
