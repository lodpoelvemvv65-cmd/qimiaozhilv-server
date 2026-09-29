# 服务端日志

- `runtime/`：当前及后续本地服务进程输出，文件名包含启动时间。
- `archive/`：历史启动、回归、迁移和诊断日志。

日志文件由根目录 `.gitignore` 的 `*.log`、`*.out`、`*.err` 规则排除，不提交到 Git；协议实测脚本写在 `logs/runtime/` 下的 JSON/JSONL 结果摘要同样按 `logs/runtime/*.json*` 排除。
启动本地服务时应将标准输出和标准错误重定向到 `logs/runtime/`，不要再写到
`server-mysql/` 根目录。

Go 的 `*_test.go` 必须继续与被测 `package main` 源码同目录，才能测试未导出实现；
它们不是运行日志，也不应移入本目录。
