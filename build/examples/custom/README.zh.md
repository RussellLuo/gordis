# 定制问候应用

[English](README.md) | 中文

本例直接调用 `Build`，保留应用自己的注册信息。每个问候插件提供默认姓名：
英文为 `World`，中文为 `朋友`，运行参数可以覆盖各自的默认值。

## 运行

build 模块尚未发布，发布后先用下述 `go get` 替换开发占位版本。
当前源码检查方式见[开发指南](../../docs/development.zh.md)。

在本例的 module 目录（`build/examples/custom`）执行：

```sh
go get github.com/RussellLuo/gordis/build@latest
go run ./cmd/build --output ./dist/greeting
```

使用默认姓名或显式传入姓名：

```sh
./dist/greeting
./dist/greeting --plugin greet/chinese
./dist/greeting --name Ada
./dist/greeting --plugin greet/chinese --name Ada
```

预期依次输出：

```text
Hello, World!
你好，朋友！
Hello, Ada!
你好，Ada！
```

每次构建使用新的输出路径。`./dist/greeting --verify-assembly` 只检查装配，
不激活插件，也不输出问候。

## 实现原理

各插件包导出 `Registration() app.Registration`，包含 Gordis prototype 和
`DefaultName`。`app.Compose` 检查描述并登记 prototype；`app.Run` 将所选描述的
默认姓名或显式姓名写入实例配置，再挂载插件。

构建配方向 `Build` 提供 Generate 和 Validate：前者生成 `Registration`、
`Compose`、`Run` 的调用，后者执行 `--verify-assembly`。本地只映射应用自身源码，
core/build 使用普通 go.mod 依赖，运行程序不依赖构建库。

构建配方见 [buildconfig/adapter.go](buildconfig/adapter.go)，应用注册模型见
[app/registration.go](app/registration.go)。原生命令用法见
[minimal](../minimal/README.zh.md)，交付细节见[构建记录](../../docs/records.zh.md)。
