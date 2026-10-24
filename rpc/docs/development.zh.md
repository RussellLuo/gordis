# 开发与验证

[English](development.md) | 中文 · [返回 README](../README.zh.md)

以下命令从 `rpc/` 执行，需使用与 [go.mod](../go.mod) 匹配的 Go 工具链；可设置 `GOCACHE=/tmp/gordis-rpc-cache` 指定可写缓存。

模块已固定支持 jsonschemax 的 `github.com/RussellLuo/jsonschema-go v0.0.0-20261020143036-d0e0ff947dfa`。Go 按普通模块依赖下载，无需相邻 jsonschema-go 目录或本地 replace。

## 常用检查

Core 与 RPC 是独立模块：根目录的 `go test ./...` 不会测试 `rpc/`。Core 检查从 Gordis 根目录运行；以下命令从 `rpc/` 创建临时 workspace，同时使用本仓库的 Core 与 RPC 源码，测试中的子构建也继承该映射。

```sh
gordis_workspace=$(mktemp -d)
export GOWORK="$gordis_workspace/go.work"
go work init .. .
# 库、生成器及真实子进程回归。
go test -race -count=1 -timeout=180s ./...

# 只运行公开示例的真实 Host/child 与 Local/process 对照。
go test -race -count=1 ./examples/...

# 修改生成器或契约后，重新生成库内 fixture。
go generate ./internal/genfixture/contract ./internal/streamfixture/contract
```

生成后的只读一致性检查：

```sh
cd internal/genfixture/contract
go run ../../../cmd/rpcgen -type Service -key Key -version Version -package bindings -out ../bindings/service.go -check
```

basic 的生成与运行命令见 [Binding](bindings.zh.md) 和 [basic 示例](../examples/basic/README.zh.md)。生成文件提交源码，不应依赖使用者构建时生成。

## 测试分别证明什么

库内测试覆盖配置声明、Schema 适配、默认值、严格解码、映射与隔离，不初始化业务的 DescribeProgram 回收，Initialize 前的 Host 校验与 child 独立校验，以及 Unit/边界拒绝、握手校验、启动期限、导出校验取消、真实子进程调用与回调、流取消、激活失败回滚及回收。测试针对适配层职责，不重复 jsonschema-go 的类型/约束矩阵，也不证明完整 meta-schema 合法性。fixture 位于模块内，不依赖下游仓库。示例测试另检查 Host 不包含 child 业务实现、普通契约与业务包不依赖 RPC。

当前可运行示例见[示例导航](../examples/README.zh.md)，程序配置规则见[核心概念](concepts.zh.md#声明程序配置)。真实进程检查需要 Unix socket 与本地监听权限；校验输出保存到临时目录。

## 发行边界

本模块位于 Gordis 仓库的 `rpc/` 目录，module path 为 `github.com/RussellLuo/gordis/rpc`。Core 保留 `github.com/RussellLuo/gordis`，不依赖 RPC。RPC 使用 `rpc/v0.1.0` 形式的标签独立发布；Core 使用 `v0.1.0` 形式的根标签。RPC 尚未发布，go.mod 不包含本地 replace。jsonschema-go 使用固定远程版本。

当前固定的 Core 版本尚不包含新增 events 协作接口。独立发行前须将 go.mod 升级到包含这些接口的真实 Core 版本，再用 `GOWORK=off` 重跑完整检查；临时 workspace 验证不能代替发行依赖验证。

本地验证覆盖 darwin/arm64。正式版本构建、独立发行依赖和其他目标平台仍需各自验证。更改未发布 API 或协议时，Host、child 与生成代码须同步更新。
