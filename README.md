# backup

MySQL 从库备份平台：`backup-server`（中心 Web 平台）与 `backup-agent`（部署在每台备份服务器）。

需求见 [docs/prd.md](docs/prd.md)。

## 开发

需要 Go 1.27+。

```bash
make test    # go test -race ./...
make vet
make build   # 输出到 bin/，默认 linux/amd64
```

## 开发流程

PRD → GitHub Issue → PR：每个 Issue 一个分支（`feat/<issue>-<name>`），PR 描述写 `Closes #<issue>`，CI 通过后 squash 合并。
