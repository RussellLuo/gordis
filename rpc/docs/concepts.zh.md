# 核心概念

[English](concepts.md) | 中文 · [返回 README](../README.zh.md)

以 Greeter 为例：业务 Plugin 实现 `Greeter`，消费者通过 `scope.Get(contract.Key)` 调用它。把 provider 放进子进程后，消费者取得的是实现同一接口的代理；业务契约和 Plugin 仍使用普通 Gordis API。

## 从接口到进程

| 概念 | 解决什么问题 | Greeter 中的对应物 |
| --- | --- | --- |
| 契约（contract） | 调用什么服务 | Go 接口 `Greeter`、类型化 `Key` 和契约版本 |
| Binding | 如何跨进程调用该契约 | 将 typed client、方法 handler 和版本绑定到 Key |
| Boundary | 程序边界允许哪些服务进出 | child 需要的 Imports、提供的 Exports |
| Unit | 子进程里运行哪些实例，如何装配 | Plugin、实例、程序配置、导入和带来源的导出 |
| Session | 某次启动后，调用属于哪个运行实例 | 会话 ID、Host 实例及 generation、诊断 |

契约和业务 Plugin 无需导入 RPC。生成或手写的 Binding 放在适配包，Host 与 child 的装配代码显式引用它。参见 [basic 文件布局](../examples/basic/README.zh.md#普通代码与程序装配)。

## Imports / Exports 始终以 child 为参照

- **Imports**：child 依赖、由 Host 提供的服务，例如日志或回调。Host 通过 Scope.Get 固定依赖，再提供回调通道。
- **Exports**：child 提供、供 Host 使用的服务，例如 Greeter。Host 取得相应的 typed client。

因此 `NewProcess` 将 Imports 声明为 Host Plugin 的 Requires，将 Exports 声明为 Provides。两端校验完整的契约名、版本和方向，不能各自只声明需要调用的部分。

Host 在 `ProcessOptions.ChildBoundary` 或 `AttachOptions.ChildBoundary` 中声明预期的子进程边界；子进程用 `Unit.Imports` 和 `Unit.Exports` 声明实际边界，两端方向相同。`Unit.Boundary()` 保留 Imports、提取每个 Export 的 Binding，省略内部来源 Instance，得到与 Host 声明匹配的契约。

没有 Imports 时无需回调 server。零导出、仅导入或无服务的任务 Unit 也合法。

## Unit 是闭合的静态装配

`Unit.Plugins` 注册普通 Plugin，`Instances` 指定运行实例；`Config` 通过 `NewConfig[Config]()` 声明程序级配置，由 Config 的 `New` 提供默认值、`Configure(ctx)` 映射到内部实例。每个 Export 明确写出来源 `Instance` 与 Binding。

所有 Requires 必须由 Unit 内实例或 Imports 满足，每个服务名只能有一个 provider；当前不支持 Unit 内多 provider 的 View 路由。依赖排序、循环检测、激活和回滚由 Gordis 负责。

Imports / Exports 只描述跨进程公开范围；内部 Plugin 之间的依赖与服务不必列入其中。

一个 Unit 可以包含多个 Plugin、多个实例和多个导出；它不等于一个业务 Plugin。

## 程序身份与服务契约各自命名

`Identity.ID` 是程序的稳定身份，`Identity.Version` 是程序版本，`Identity.Protocol` 是底座协议 `rpc.Protocol`。服务 Key 的名称与 Binding.Version 则标识契约及其兼容版本。

一个程序可以提供多个契约，因此程序 ID 无需等于契约 ID。`ProcessOptions.PluginID` 又是 Host 注册加载器的逻辑 Plugin ID。basic 在 [Host](../examples/basic/host/cmd/main.go) 和 [插件程序](../examples/basic/plugin/cmd/main.go) 入口直接声明程序身份与各自的边界，普通接口、Key 和契约版本放在 [contract](../examples/basic/contract/service.go)。

Host 可用非零 Identity 固定预期身份；零值允许启动后通过 Describe 发现身份，ChildBoundary 仍须精确匹配。

## 配置与运行实例

`NewProcess` 创建可注册的 Plugin 原型，不启动程序。每个 `Spec.New` 产生独立状态，激活时才建立自己的子进程与 Session。

实例 Config 是程序级 JSON 对象，例如 `{"prefix":"Welcome"}`。Host 从本次 Describe 获取 Schema，在 Initialize 前校验输入；child 独立校验，再调用 `New`、严格解码和 `Configure`。映射按内部实例 ID 完整替换配置，未映射实例保留预设值；普通 Plugin 仍自行解码和 Validate。`Unit.Config` 零值只接受缺省输入或空对象，Host 无须选择配置模式。

`DescribeProgram(ctx, command, limits)` 真实启动程序，返回身份、child 视角 Contracts 和 ConfigSchema，最终回收进程；不执行初始化、配置方法或业务清理。required 字段须显式提供，不能由 New 补齐。

程序路径、参数、环境、身份、ChildBoundary 和 Limits 来自 ProcessOptions，与业务配置分开。

## 声明程序配置

在程序装配包中定义具名、非指针结构体，方法使用值接收者。[basic 入口](../examples/basic/plugin/cmd/main.go)使用以下声明：

```go
type Config struct {
    Prefix string `json:"prefix,omitempty" jsonschema:"Greeting prefix" jsonschemax:"minLength=1"`
}

func (Config) New() Config { return Config{Prefix: "Hello"} }
func (c Config) Configure(context.Context) (map[string]any, error) {
    return map[string]any{"greeter": c}, nil
}
```

调用 `config, err := rpc.NewConfig[Config]()`，处理错误后在 ServeUnit 前设置 `Unit.Config = config`。NewConfig 生成并编译 Schema，不调用 New 或 Configure；无需源码扫描、配置生成命令或额外 Schema 文件。跨字段业务检查放在 Configure 开头；程序配置 API 不调用独立 Validate 方法。普通业务 Plugin 仍保留自身的配置及 Validate。

`json` 控制字段名、排除与可省略性，`jsonschema` 提供描述，`jsonschemax` 声明校验约束。支持的类型、tag 语法和约束详见 [jsonschema-go：Validation tags](https://pkg.go.dev/github.com/RussellLuo/jsonschema-go/jsonschema#hdr-Validation_tags)。

required 在 New 前校验原始输入。省略字段保留 New 的默认值，显式零值覆盖，null 遵循 Schema 与 encoding/json 规则。默认值不写入 Schema，也不自动验证，作者须通过测试保证正确。New 每次返回独立的可变默认值；New 和 Configure 只做短时计算，不获取资源，Configure 须响应 context 取消。

Configure 的键是 child 内部实例 ID。一份程序配置可映射到多个实例，采用各自的内部配置结构体。所有目标必须存在，每个值须能序列化为 JSON 对象；nil 或空 map 表示无覆盖。每项完整替换对应实例的预设 Config，不做合并。向内部传递显式零值时，使用不带 omitempty/omitzero 的字段或 map。内部实例 ID 和类型无需暴露给 Host。

输入必须是单个 JSON 对象，缺省规范化为 `{}`；根 null、数组、标量、尾随值及未知字段均失败。Schema 使用 float64 校验，严格 Go 解码仍读取原始字节，保留数值表示并拒绝 Go 溢出或不兼容字面量。约 ±2^53 以外的 Schema 边界及枚举可能损失精度；自定义 JSON 表示与推导 Schema 的一致性由作者保证。

Host 接受显式 object 根的 Schema，使用库原生 Resolve/Validate，当前缺省按 Draft 2020-12 解释；显式 dialect 与本地引用沿用库规则，gordis-rpc 不增加版本白名单。未提供外部 loader，不能获取网络或文件引用，这不等于完整标准 meta-schema 校验。Schema 随程序描述传输，Plugins 和 Instances 保留在 child。先前 DescribeProgram 探测只供参考，Attach 始终重新检查本次进程描述。Schema 及校验语义变更随程序身份版本管理，包括依赖升级引入的变化。

接下来阅读[架构与生命周期](architecture.zh.md)，了解何时服务可用，以及失败时如何回收。
