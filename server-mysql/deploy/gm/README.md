# GM 后台部署

本目录包含 GM 后台的 Docker 配置。`Dockerfile` 使用的是 **预编译好的 Linux 二进制**，
这些二进制**不随仓库提交**，需要自行构建。

## 构建

在 `server-mysql/` 目录下交叉编译：

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o deploy/gm/gm-api   ./cmd/gm-api
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o deploy/gm/gm-admin ./cmd/gm-admin
```

PowerShell：

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -o deploy/gm/gm-api   ./cmd/gm-api
go build -o deploy/gm/gm-admin ./cmd/gm-admin
```

## 构建前端

`compose.yaml` 会把 `deploy/gm/dist` 挂载为 nginx 静态目录：

```bash
cd web
npm install
npm run build
# 将构建产物复制到 server-mysql/deploy/gm/dist
```

## 启动

```bash
cd server-mysql/deploy/gm
docker compose up -d --build
```

首次启动后需要创建管理员账号：

```bash
docker compose run --rm --no-deps \
  -e GM_ADMIN_PASSWORD='<至少 12 位的强密码>' \
  --entrypoint /gm-admin gm-api \
  -username admin -display-name 管理员 -role superadmin
```

## 端口

| 服务 | 端口 |
|---|---|
| GM API（容器内） | `8080/tcp` |
| nginx 前端 | `8897/tcp`（宿主） |

MySQL / Redis 通过外部 Docker 网络 `mhq_backend` 访问，不对外暴露。
