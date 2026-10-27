# 命令与 SDK 用法

[English](usage.md) | 中文 · [README](../README.zh.md)

## 原生命令

原生命令必填 `--app package[@query][=replacement]`，通过 `--with` 选择额外插件包，与 SDK CLI 共用 `--replace`/`--output`。应用导出 `func Run([]string, []gordis.Plugin) error` 并支持纯 `--check`；所选插件包导出 `func Plugins() []gordis.Plugin`。默认能力与运行生命周期归应用。签名由 Go 编译器检查，重复类型 ID 由应用的 Host 登记检查。命令使用公共记录格式，默认输出 `./gordis-app`（Windows 加 `.exe`）。使用方式与发布状态见 [minimal](../examples/minimal/README.zh.md)。

## Build API

```go
import builder "github.com/RussellLuo/gordis/build"

result, err := builder.Build(ctx, builder.Request{
	ModulePath: "example.com/compiled-app",
	Dir:        cwd,
	Output:     "dist/app",
	Packages: []builder.Package{{
		Path:  "example.com/myapp/run",
		Query: builder.Query{Value: "latest"},
	}},
	Replacements: []builder.Replacement{{
		Module: "example.com/myapp",
		Source: builder.Source{Directory: "./app-source"},
	}},
}, builder.Adapter{
	Generate: generateMain,
	Validate: builder.Validation{Args: []string{"--verify"}},
})
```

此片段中的 `generateMain(Plan) (Project, error)` 由应用实现，返回导入所选包的根 Go main 文件；生成程序自行支持不启动业务的 `--verify`。引擎不解释工厂函数名或应用注册类型。Record 为 nil 时使用 `gordis.build-record/v1alpha1`；自定义编码器接收 Facts，须返回含合法 JSON 对象的 record.json。详见[记录结构与编码](records.zh.md)。

`Run(ctx, args, stdout, stderr, Command)` 复用 `--with`、`--replace`、`--output`、帮助、错误反馈与退出码。Command.Prepare 补应用默认值，返回 Request/Adapter，再调用同一个 Build。`AcceptApp` 开启 `--app` 的独立解析，角色由应用策略决定。帮助在 Prepare、源码检查或 Go 命令之前返回。成功/帮助退出 0，参数/Prepare 错误退出 2，Build 失败退出 1。

Packages 选择可导入代码；Modules 只增加版本要求，不选择工厂；Replacements 只替换源码。Source 二选一填 Directory 或 Module + Query。隐式本地查询使用开发占位，不查询发布版本；显式移动查询仍解析原 module 身份。Plan 的 require 版本与 Facts.Modules 的实际 MVS 版本分开记录，Exact 拒绝版本提升。

Generate/Record 是受信任的纯计算回调，接收独立快照。候选校验默认超时 30 秒，并受外层 Context 约束。错误带阶段，且可 Unwrap 原始错误。校验、输入复核与记录编码成功后，才交付二进制与 `<output>.build/`；任一路径已有内容即拒绝，正常失败清理本轮部分交付。

首版固定本机 Go 工具链与 OS/ARCH、CGO_ENABLED=0、GOWORK=off、GOFLAGS 为空、GOTOOLCHAIN=local。生成程序运行不需要构建器、源码、记录目录或 Go 可执行文件。输入摘要比较两次快照，不提供文件系统隔离；回调与候选检查也不在进程沙箱内运行。
