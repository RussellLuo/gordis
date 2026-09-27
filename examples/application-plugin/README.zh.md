# 应用插件

[English](README.md) | 中文

本示例在 Host 启动后交付一个带版本的应用插件。`sampler` bundle 包含后端可执行文件和
浏览器 UI，并将 Alpha 和 Beta 声明为共用同一插件页面的独立进程。

Host 只提供通用进程、HTTP gateway 和 UI 加载支持，不会链接 sampler 实现。

## 运行

要求：Go 1.27+ 和 Node.js 22+。

在第一个终端进入本示例目录，然后构建并启动 Host：

```sh
cd examples/application-plugin
npm --prefix web ci
node build.mjs --host-only --output=/tmp/gordis-app-demo
/tmp/gordis-app-demo/host
```

在第二个终端构建 sampler v1：

```sh
cd examples/application-plugin
node build.mjs --packages-only --version=1.1.0 --output=/tmp/gordis-app-demo
```

打开 <http://127.0.0.1:8090/demo/> 并加载 v1.1.0。Alpha 和 Beta 应具有不同的 PID。
sampler 页面会出现在导航中，每个实例都有自己的面板，第一次采样返回 `1007`。

要尝试升级，请先重新启用任何已停止的实例，再构建 v2 并在 UI 中选择它：

```sh
node build.mjs --packages-only --version=2.1.0 --output=/tmp/gordis-app-demo
```

后端此时返回 `2007`，UI 版本随之变化，Host PID 保持不变。卸载会移除活动实例、
endpoint 和 UI，但会保留包文件，以便再次加载。

包版本目录是不可变的。如果修改了源码却要使用同一版本重新构建，请改用新输出目录，
或先将旧目录移开。

## 实现原理

1. `build.mjs` 将 backend、manifest 和带哈希的 UI 资源写入 `packages/sampler/<version>`。
2. 运行中的 Host 发现该包，并在自身 catalog 中公布它。
3. 应用 Loader 保留 package/version、enabled 状态和稳定的 Instance handle。它将包变更转换为
   `ChangeSet.Apply → Operation`；Gordis 不管理包元数据。
4. 每个 backend 都从隔离其 endpoint Service 的 Env 挂载。backend ready 后，Loader 从
   `backend.Env()` 挂载 gateway child，因此 child 会共享该 View，并随 backend 一起被回收。
5. 每个 backend 发布一个私有 HTTP endpoint。通用 gateway 在 `/api/extensions/<instance>/` 下代理它。
6. 实例 ready 后，浏览器导入通过校验的 UI 模块，并注册其页面和实例面板。
7. disable、upgrade 和 uninstall 操作会撤回 gateway、排空请求，并通过
   `quiesce -> dispose -> shutdown` 停止 backend。

建议从 [build.mjs](build.mjs)、[host/app.go](host/app.go)、[host/gateway.go](host/gateway.go)、
[sampler backend](plugins/sampler/main.go) 和 [UI loader](web/src/loader.ts) 开始阅读。

## 验证

```sh
cd examples/application-plugin
make test
make check
make verify
```

verifier 覆盖真实 HTTP 调用、子进程、升级、无效包拒绝、卸载和进程回收，
但不验证浏览器渲染。
