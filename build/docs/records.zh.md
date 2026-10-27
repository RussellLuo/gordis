# 构建记录

[English](records.md)

成功交付包含二进制及相邻的 `<output>.build/`：

```text
record.json          默认信封或应用编码
project/             生成文件、最终 go.mod 和存在时的 go.sum
dependencies.json    go list -deps -json . 的对象流，不是数组
logs/NN.log          命令输出，由 Commands 索引
```

`Adapter.Record == nil` 时，record.json 使用两空格缩进、末尾换行，`apiVersion` 为 `gordis.build-record/v1alpha1`，Facts 全部字段平铺到顶层。空集合使用 `[]`/`{}`。公共格式与 Qi schema 分开。

| 字段 | 含义 |
| --- | --- |
| request | 原始包选择、查询、module 要求、源码映射与路径，保留输入意图。 |
| plan | 生成前固定的包归属、有效查询、require 版本与替换。 |
| modules | Go 最终 MVS 图；Path/Version/Sum/GoModSum 与 Replace 沿用 Go 字段名。require 不一定等于实际版本。 |
| replacements | 每条固定映射与真实 used，包括未使用映射。 |
| inputs | 本地 module 绝对目录、实际编译/嵌入文件及 module 文件、逐文件和聚合 SHA-256。未使用映射仍记录 module 文件。 |
| commands | 截至最后一次输入发现的有序阶段、argv、相对日志路径、退出码、超时与取消事实。 |
| environment | GOVERSION、GOOS/GOARCH、CGO_ENABLED、GOWORK、GOFLAGS、GOTOOLCHAIN 等选定事实，不复制全部进程环境。 |
| generatedSHA256 | 生成工程及最终 module 文件的摘要，键相对 project/。 |
| binarySHA256 | 交付二进制字节的 SHA-256。 |
| localDevelopment | 存在本地源码映射，不代表发布验收。 |

摘要为小写十六进制 SHA-256。输入聚合摘要计算紧凑 `json.Marshal(Files)` 的字节，map 键排序。Plan 的包/要求/替换、modules 和 inputs 固定排序，commands 保持执行顺序。输入比较是快照，不提供连续文件系统隔离或源码归档；不承诺不同工具链生成相同二进制。

GOFLAGS 记录有效的空参数集 `""`。执行环境使用空白值阻止 Go 回退到用户持久配置中的 GOFLAGS；其他代理、缓存配置继续继承。模块归属发现中的未命中探测也保留在 commands/logs 中，因此成功构建可以包含非零退出的探测命令。

自定义 `Record func(Facts) (map[string][]byte, error)` 接收校验后的独立快照，**完整替换**默认编码。必须返回包含合法 JSON 对象的 record.json，可另加 `metadata/selection.json` 等应用文件。schema/version 归应用，库不会把默认 Facts 再合并进去。修改快照不影响引擎计划、二进制摘要或日志。与 Generate 一样，Record 必须是纯计算；库在回调前后检查取消，不强制中断回调内部执行。

文件名必须是规范的相对 slash 路径。拒绝绝对路径、越界、反斜线、大小写别名和文件/目录冲突。`project/`、`logs/`、`dependencies.json`（含大小写变体与子路径）由库独占。编码错误、无效文件/JSON、取消或源码变化均在交付前失败。任一输出路径已有内容即拒绝。正常失败清理本轮部分交付，不承诺崩溃/断电时两个路径的事务原子性。

[record_test.go](../record_test.go) 与 [build_test.go](../build_test.go) 通过真实编译/校验验证替换编码、附加文件、回调数据隔离、无效输出、编码错误和取消。公开 Go 类型与 JSON 字段名见 [types.go](../types.go)。
