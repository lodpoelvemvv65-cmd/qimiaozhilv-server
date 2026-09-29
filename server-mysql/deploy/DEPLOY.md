# Game binary and operation configuration deployment

The remote build context is `runtime/`. Its `.dockerignore` permits only the
Linux executable and the runtime Dockerfile, so no Go source is sent to the
Docker daemon or copied into the game image. Automated updates additionally
synchronize the reviewed `config/operations/*.yaml` set to the host, mount it
read-only at `/config/operations`, and upload a short-lived configuration
publisher. The YAML files are editable inputs; authoritative runtime state is
still rebuilt from MySQL after every validated publication.

## 1. Build on the development machine

Run from `server-mysql`:

```powershell
.\deploy\scripts\build-linux-release.ps1 -Architecture amd64
```

This builds `deploy/runtime/mhqserver` and
`deploy/tools/publish-config`. The latter is an operational tool and is never
copied into the game image.

For local development, start the game with its built-in Go watcher:

```powershell
.\mhqserver-mysql.exe -listen 127.0.0.1:7756 -advertise 127.0.0.1:7756 -watch-config
```

The watcher monitors `config/operations/*.yaml`, automatically validates and
publishes each settled file save to MySQL, and broadcasts Redis so the game
process hot-reloads the new snapshot without a restart. It is enabled by
default; pass `-watch-config=false` only when a deployment explicitly wants
manual publishing. Go code changes still require rebuilding and restarting the
local process.

Use `arm64` only when the target server reports `uname -m` as `aarch64` or
`arm64`.

## 2. Produce the final database dump

Use a maintenance window. Stop the Windows game process first so every online
session is saved and no write occurs during export. Keep a separate backup.

```powershell
New-Item -ItemType Directory -Force .\deploy\database | Out-Null
& 'C:\Program Files\MySQL\MySQL Server 8.0\bin\mysqldump.exe' `
  -h 127.0.0.1 -P 3306 -uroot -p `
  --single-transaction --routines --events --triggers `
  --set-gtid-purged=OFF --column-statistics=0 `
  --databases mhq `
  --result-file=.\deploy\database\001-mhq.sql
```

Enter the password at the prompt. Confirm the dump is non-empty before moving
on. The dump contains account and player data and must be transferred as a
secret.

## 3. Create the upload archive

Only package these runtime artifacts:

```powershell
tar -czf mhq-runtime-linux-amd64.tar.gz -C .\deploy `
  compose.yaml .env.example DEPLOY.md runtime database
```

The archive contains no Go source. Transfer it to the Linux server over SSH,
extract it into a dedicated directory, then create `.env` from `.env.example`.
Set `MHQ_PUBLIC_ADDR` to the public IP or domain plus `:7756`.

## 4. Start on Linux

```bash
cp .env.example .env
chmod 600 .env
# Edit all passwords and MHQ_PUBLIC_ADDR before continuing.
docker compose up -d mysql redis
docker compose logs -f mysql
```

Wait until MySQL reports that it is ready for connections and the import has
finished. Then start the game:

```bash
docker compose up -d --build game
docker compose ps
docker compose logs -f game
```

The host firewall/security group should expose TCP 7756 only. MySQL and Redis
are attached only to the internal Docker network.

The MySQL import bind mount uses Docker's `Z` relabel option for SELinux hosts
such as Rocky Linux. Keep the `database` directory traversable/readable by the
container until the first import finishes.

## 5. Verify

The game log must show the expected public gate address, 77 configuration
names loading from MySQL, Redis ready, and TCP `:7756` listening. From another
machine verify the port before patching the client:

```powershell
Test-NetConnection PUBLIC_IP_OR_DOMAIN -Port 7756
```

MySQL imports `database/001-mhq.sql` only for a new empty `mysql-data` volume.
Never delete that volume on a live deployment. After verification, move the
SQL dump to encrypted backup storage and remove it from the server directory.

## 6. Publish routine updates

Run the pinned-host deployment script from the repository root after reviewing
`server-mysql/config/operations/Gameplay.yaml`:

```powershell
$env:MHQ_SSH_PASSWORD = 'set only in this process'
.\server-mysql\deploy\scripts\deploy-server.ps1
Remove-Item Env:MHQ_SSH_PASSWORD
```

The deployment scripts connect as the non-root `ww` account by default. That
account must have password-based `sudo` permission for Docker and the
`/opt/mhq` deployment directory. `MHQ_SSH_PASSWORD` is also used as the sudo
password; set `MHQ_SUDO_PASSWORD` in the current process only when the sudo
password differs. Root SSH login is not required.

The script validates the YAML locally, runs all Go tests, builds and hashes both
Linux executables, deploys and verifies the game, and synchronizes every YAML in
`config/operations/` to the remote host. After the container starts, the built-in
watcher validates and publishes the non-Gameplay files to MySQL and confirms the
Redis hot reload; `Gameplay` is then published through the private Docker
network with its exact revision confirmed. MySQL and Redis remain private, and
no player data, database volumes, or Go source files are replaced.

Use `-GameplayConfigPath <path>` only when intentionally publishing a reviewed
alternate YAML file. Invalid or unknown fields stop deployment before SSH upload.

## 7. Deploy the GM console

The GM console is built from `web/` and served by an Nginx container. The Nginx
container is attached to the public Docker network and publishes TCP `8897`;
the GM API remains on the private backend network and is reached only through
the `/api/` reverse proxy. The API uses the same MySQL and Redis containers as
the game and never exposes their ports publicly.

Build the frontend and Linux GM binaries, then deploy the complete static
bundle and backend image:

```powershell
Push-Location .\web
npm ci --offline --no-audit --no-fund
npm run build
Pop-Location

Push-Location .\server-mysql
$env:GOOS = 'linux'; $env:GOARCH = 'amd64'; $env:CGO_ENABLED = '0'
go build -buildvcs=false -tags timetzdata -trimpath -ldflags '-s -w -buildid=' -o deploy/gm/gm-api .\cmd\gm-api
go build -buildvcs=false -tags timetzdata -trimpath -ldflags '-s -w -buildid=' -o deploy/gm/gm-admin .\cmd\gm-admin
Pop-Location

$env:MHQ_SSH_PASSWORD = 'set only in this process'
$env:GM_ADMIN_PASSWORD = 'at least 12 characters; set only in this process'
.\server-mysql\deploy\scripts\deploy-gm.ps1
Remove-Item Env:MHQ_SSH_PASSWORD, Env:GM_ADMIN_PASSWORD
```

The script pins the SSH host key, hashes every uploaded binary and static file,
keeps the previous GM directory under `/root/mhq-backups`, initializes the
`superadmin` account with `gm-admin`, and verifies both `/api/v1/health` and
the public `http://127.0.0.1:8897/` endpoint. It does not touch the game
container or any database volume.
