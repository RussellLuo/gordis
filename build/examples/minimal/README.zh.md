# 最小问候应用

[English](README.md) | 中文

直接用 `gordis-build` 构建英文问候应用，再加入可选的中文插件。
本例使用普通 Gordis 插件，无需应用构建包装。

## 运行

build 模块尚未发布，以下安装与构建步骤需等发布后执行。
当前源码检查方式见[开发指南](../../docs/development.zh.md)。

在本例的 module 目录（`build/examples/minimal`）执行：

```sh
go install github.com/RussellLuo/gordis/build/cmd/gordis-build@latest

gordis-build \
  --app example.com/gordis-minimal/app \
  --replace example.com/gordis-minimal=. \
  --output ./dist/hello
./dist/hello
```

运行程序输出：

```text
Hello, World!
```

通过 `--with` 加入中文插件：

```sh
gordis-build \
  --app example.com/gordis-minimal/app \
  --with example.com/gordis-minimal/plugins/chinese \
  --replace example.com/gordis-minimal=. \
  --output ./dist/bilingual
./dist/bilingual --plugin greet/chinese --name Ada
```

运行程序输出：

```text
你好，Ada！
```

每次构建使用新的输出路径。二进制旁边会生成包含构建记录的 `.build/` 目录。

## 实现原理

`app.Run` 默认加入英文插件。中文包导出 `Plugins()`，通过 `--with` 选择后，
生成入口会加入它提供的插件。`--replace` 仅映射应用自身源码，
Gordis core 作为 go.mod 中的普通依赖。

应用通过 `NewHost` 登记插件类型，将 `--name` 写入实例配置，挂载所选插件，
最后关闭 Host。`--check` 只检查类型登记，不激活插件。

应用入口见 [app/run.go](app/run.go)；自定义注册描述见
[custom](../custom/README.zh.md)；交付细节见[构建记录](../../docs/records.zh.md)。
