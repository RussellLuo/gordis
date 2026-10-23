# 示例

[English](README.md) | 中文

从 [basic](basic/README.zh.md) 开始：普通 Greeter Plugin、生成的 typed Binding、库内通用
process 加载器，以及独立插件程序 Unit。程序配置由 NewConfig 声明，New 提供默认值，
Configure 映射为普通实例配置。

再看 [duplex](duplex/README.zh.md)：插件程序导入 Host Directory，在 Greeter 调用中
嵌套回调 Host。两端仍使用普通业务 Plugin，一个 Import、一个 Export。
ConfigSpec 零值只接受缺省输入或空对象，Host 不选择配置模式。

然后看 [stream](stream/README.zh.md)：逐条消费 typed 结果，先完整消费，再通过
本地 yield 错误提前停止。

从 `rpc/` 运行 `GOWORK=off go test -race -count=1 ./examples/...`，验证真实
Host 和插件程序、配置、Local/process 对照、流式结果和依赖边界。

Unit 装配文档配方和 events 可选集成属于后续阶段，目前尚未实现。
