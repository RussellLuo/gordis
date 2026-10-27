# Gordis build

[English](README.md) | 中文

Gordis build 将选定的 Go 应用包和插件包编译成独立可执行程序，负责依赖解析、
入口生成、编译、应用校验和交付。应用继续负责运行配置与生命周期。

独立模块路径为 `github.com/RussellLuo/gordis/build`，目前尚未发布；
当前源码检查方式见[开发指南](docs/development.zh.md)。

## 从示例开始

| 使用方式 | 推荐入口 |
| --- | --- |
| 用 `gordis-build` 装配普通 Plugin 应用 | [Minimal](examples/minimal/README.zh.md)：构建英文问候应用，再加入中文插件。 |
| 调用 `Build`，自定义入口生成与装配检查 | [Custom](examples/custom/README.zh.md)：保留插件注册描述和默认姓名。 |

应用如需提供自己的构建命令，还可以通过 `Run` 复用标准参数解析与同一个构建引擎。

## 构建产物

构建成功后交付可执行程序，以及相邻的 `<output>.build/` 目录，保存依赖记录、
生成源码和日志。校验在交付前完成，已有输出路径不会被覆盖。
运行程序无需 Go、构建库或记录目录。

## 继续阅读

- [命令与 SDK 用法](docs/usage.zh.md)：CLI 约定、构建请求与适配。
- [构建记录](docs/records.zh.md)：记录内容与自定义编码。
- [开发检查](docs/development.zh.md)：独立模块与本地源码验证。
