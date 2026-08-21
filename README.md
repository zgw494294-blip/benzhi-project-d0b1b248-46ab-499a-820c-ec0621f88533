# 弧证 ArcProof

弧证是一个面向压力设备与重型制造场景的离线焊接工艺适用性审查服务。它把规则版本、工艺评定证据、规程覆盖范围、生产接头匹配、变更影响和复核任务保存在同一条可校验的审计链中。

项目只依赖 Go 标准库，不需要数据库、消息队列或外部网络。所有说明、错误消息和命令输出均使用简体中文。

## 启动

需要 Go 1.22 或更高版本。

```bash
go run ./cmd/arcproof -addr=127.0.0.1:19463 -data-dir=./var/data
```

显式 `-addr` 的优先级最高。未提供时读取 `PORT` 并绑定 `127.0.0.1:<PORT>`；两者都没有时使用 `127.0.0.1:19463`。服务拒绝通配地址、低位端口以及 3000、8080。

写请求必须携带 `X-Actor`，需要幂等语义的证据、接头和批量导入请求还应携带 `Idempotency-Key`。健康检查：

```bash
curl http://127.0.0.1:19463/api/v1/healthz
```

真实监听与持久化自检：

```bash
go run ./cmd/arcproof -addr=127.0.0.1:19463 -data-dir=./var/self-check -self-check
```

## 批量导入

输入是每行一个生产接头要求的 NDJSON 文件。程序先校验完整批次；任一行非法时不会落盘。

```bash
go run ./cmd/arcproof-import \
  -data-dir=./var/data \
  -file=./examples/joints.ndjson \
  -actor=zhang-san \
  -batch-key=shift-20260821
```

增加 `-dry-run` 可只做业务校验。相同批次键和相同规范化内容会返回原结果，相同键配不同内容会报冲突。

## 验证

```bash
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./...
```

两条公开端到端验收命令见 `.benzhi/plan.json`。它们使用动态高位回环端口，不依赖共享固定端口或外部服务。

业务规则和接口说明位于 [docs/business-rules.md](docs/business-rules.md) 与 [docs/api.md](docs/api.md)，数据恢复方法位于 [docs/recovery.md](docs/recovery.md)。

