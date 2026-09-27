# 类型化服务与配置

[English](README.md) | 中文

本示例将项目 README 中的 greeter 扩展为两种插件类型。一个 `greeter` 实例提供类型化
`Greeter` 服务；两个 `greeting` 实例以不同的 JSON 配置消费该服务。

## 运行

在仓库根目录执行：

```sh
go run ./examples/basic
```

预期输出：

```text
alice: Hello, Alice
bob: Hello, Bob
```

## 实现原理

`greeterKey` 定义共享契约。provider 在 `Provides` 中声明它，并通过 `gordis.Provide`
发布值；每个 consumer 在 `Requires` 中声明它，再通过 `gordis.Get` 读取其固定绑定。

`NewHost` 只注册这两种插件类型。程序随后通过 root Env 使用 `MountReady`
直接挂载 provider、Alice 和 Bob。Host 会将每个实例的 `Config` 解码到新创建的插件对象，
并在 `Activate` 前调用 `Validate`。

完整程序见 [main.go](main.go)；多 provider 和隔离 Env 见
[isolation](../isolation/README.zh.md)；child 生命周期所有权见
[ownership](../ownership/README.zh.md)。
