# 开发检查

[English](development.md) | 中文 · [README](../README.zh.md)

core、build 和两个示例分别是独立 Go module，仓库根目录的测试不会覆盖嵌套模块。

## 构建模块

在 `build/` 目录独立检查，不依赖 core 根模块的测试发现：

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
```

## 使用本地源码检查示例

在 `build/` 目录执行。临时 workspace 让示例测试在 build 模块发布前使用当前源码。
显式版本替换只对这个 workspace 生效，包括尚未发布的开发占位版本；普通用户通过
go.mod 获取依赖，无需这些映射。

`go work edit -replace` 修改临时 go.work，示例的 go.mod 保持不变。生成应用时，
构建引擎仍使用 `GOWORK=off`，源码映射由请求或测试夹具明确提供。

创建 workspace 并检查两个示例：

```sh
check_dir=$(mktemp -d)
core_dir=$(cd .. && pwd)
export GOWORK="$check_dir/go.work"
go work init "$core_dir" "$PWD" "$PWD/examples/minimal" "$PWD/examples/custom"
go work edit -replace "github.com/RussellLuo/gordis@v0.0.0-20261014054822-1b6675a88714=$core_dir"
go work edit -replace "github.com/RussellLuo/gordis/build@v0.0.0=$PWD"
go test ./examples/minimal/... ./examples/custom/...
unset GOWORK
```

workspace 只在源码检查中映射依赖版本，不修改输入 module 文件。测试使用本地
module 与 file proxy，覆盖解析、生成、校验、记录和交付。native 与 custom 集成测试
还检查插件激活、实例配置和生产 package 闭包，确认运行程序不携带构建器。
