# 嵌套生命周期所有权

[English](README.md) | 中文

本示例在 owner 激活期间挂载一个 child，随后重启 owner。

```text
owner generation 1 ── owns ──> owner/child generation 1
        Restart
owner generation 2 ── owns ──> owner/child generation 2
```

## 运行

在仓库根目录执行：

```sh
go run ./examples/ownership
```

预期输出：

```text
start owner / generation 1
start owner/child / generation 1
close owner/child / generation 1
close owner / generation 1
start owner / generation 2
start owner/child / generation 2
close owner/child / generation 2
close owner / generation 2
```

## 实现原理

owner 在 `Activate` 期间调用 `scope.Env().Mount`。该 Env 会为 child 分配带 owner 限定的
ID，并将它绑定到当前 owner generation。

`Instance.Restart` 先按 child-before-owner 顺序清理旧子树。下一个 owner generation
会再次运行 `Activate` 并声明新 child。最后，`Host.Shutdown` 以相同顺序清理第二棵子树。

完整程序见 [main.go](main.go)；child 的 Service 可用性独立于 owner 就绪状态的示例见
[readiness](../readiness/README.zh.md)。
