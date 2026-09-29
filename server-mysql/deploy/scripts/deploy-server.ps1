[CmdletBinding()]
param(
    [string]$ServerHost = "127.0.0.1",
    [int]$SshPort = 22,
    [string]$SshUser = "root",
    [string]$RemoteDir = "/opt/mhq",
    [int]$GamePort = 7756,
    [ValidateSet("amd64", "arm64")]
    [string]$Architecture = "amd64",
    # 留空则跳过 SSH 主机指纹校验（仅建议在可信网络中使用）
    [string]$ExpectedHostKey = "",
    [string]$GameplayConfigPath = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Require-Command {
    param([Parameter(Mandatory = $true)][string]$Name)

    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if (-not $command) {
        throw "Required command is not installed or not in PATH: $Name"
    }
    return $command
}

function ConvertTo-PlainText {
    param([Parameter(Mandatory = $true)][Security.SecureString]$SecureString)

    $pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($SecureString)
    try {
        return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer)
    }
    finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer)
    }
}

$goCommand = Require-Command "go"
$pythonCommand = Require-Command "py"
$serverRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot "..\.."))
$composePath = Join-Path $serverRoot "deploy\compose.yaml"
$operationsConfigDir = Join-Path $serverRoot "config\operations"

if (-not (Test-Path -LiteralPath $composePath -PathType Leaf)) {
    throw "Deployment Compose file was not found: $composePath"
}
if (-not (Test-Path -LiteralPath $operationsConfigDir -PathType Container)) {
    throw "Operations configuration directory was not found: $operationsConfigDir"
}
$operationFiles = @(Get-ChildItem -LiteralPath $operationsConfigDir -File | Where-Object {
        $extension = $_.Extension.ToLowerInvariant()
        $extension -eq ".yaml" -or $extension -eq ".yml"
    } | Sort-Object Name)
if ($operationFiles.Count -eq 0) {
    throw "No YAML/YML operation configuration files were found in: $operationsConfigDir"
}

if ([string]::IsNullOrWhiteSpace($GameplayConfigPath)) {
    $GameplayConfigPath = Join-Path $serverRoot "config\operations\Gameplay.yaml"
}
if (-not (Test-Path -LiteralPath $GameplayConfigPath -PathType Leaf)) {
    throw "Gameplay configuration file was not found: $GameplayConfigPath"
}
$gameplayExtension = [IO.Path]::GetExtension($GameplayConfigPath).ToLowerInvariant()
if ($gameplayExtension -notin @(".yaml", ".yml")) {
    throw "Gameplay configuration must be a YAML/YML file: $GameplayConfigPath"
}
$GameplayConfigPath = (Resolve-Path -LiteralPath $GameplayConfigPath).Path

Write-Host "[1/6] Validating Gameplay configuration before deployment..."
Push-Location $serverRoot
try {
    & $goCommand.Source run .\cmd\publish-config -name Gameplay -source $GameplayConfigPath -validate-only
    if ($LASTEXITCODE -ne 0) {
        throw "Gameplay configuration validation failed with exit code $LASTEXITCODE"
    }
}
finally {
    Pop-Location
}

Write-Host "Validating all operation YAML files before deployment..."
Push-Location $serverRoot
try {
    foreach ($operationFile in $operationFiles) {
        if ($operationFile.BaseName -eq "CustomSkins") {
            # CustomSkins is an overlay validated by the in-process watcher;
            # its schema is covered by the server test suite.
            continue
        }
        & $goCommand.Source run .\cmd\publish-config -name $operationFile.BaseName -source $operationFile.FullName -validate-only
        if ($LASTEXITCODE -ne 0) {
            throw "Operation configuration validation failed: $($operationFile.Name)"
        }
    }
}
finally {
    Pop-Location
}

Write-Host "[2/6] Running Go tests and building Linux/$Architecture deployment binaries..."
& (Join-Path $PSScriptRoot "build-linux-release.ps1") -Architecture $Architecture
if ($LASTEXITCODE -ne 0) {
    throw "build-linux-release.ps1 failed with exit code $LASTEXITCODE"
}

$binaryPath = Join-Path $serverRoot "deploy\runtime\mhqserver"
$publisherPath = Join-Path $serverRoot "deploy\tools\publish-config"
if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) {
    throw "Linux binary was not created: $binaryPath"
}
if (-not (Test-Path -LiteralPath $publisherPath -PathType Leaf)) {
    throw "Linux configuration publisher was not created: $publisherPath"
}
$binaryHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binaryPath).Hash.ToLowerInvariant()
$publisherHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $publisherPath).Hash.ToLowerInvariant()
$composeHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $composePath).Hash.ToLowerInvariant()
$gameplayConfigHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $GameplayConfigPath).Hash.ToLowerInvariant()

Write-Host "[3/6] Checking the SSH deployment dependency..."
& $pythonCommand.Source -c "import paramiko" 2>$null
if ($LASTEXITCODE -ne 0) {
    throw "Python package 'paramiko' is required. Install it with: py -m pip install paramiko"
}

$passwordWasPrompted = $false
if ([string]::IsNullOrWhiteSpace($env:MHQ_SSH_PASSWORD)) {
    $securePassword = Read-Host "SSH password for $SshUser@$ServerHost" -AsSecureString
    $env:MHQ_SSH_PASSWORD = ConvertTo-PlainText $securePassword
    $passwordWasPrompted = $true
}

$deployEnvironment = @{
    MHQ_DEPLOY_HOST = $ServerHost
    MHQ_DEPLOY_SSH_PORT = $SshPort.ToString()
    MHQ_DEPLOY_USER = $SshUser
    MHQ_DEPLOY_REMOTE_DIR = $RemoteDir
    MHQ_DEPLOY_GAME_PORT = $GamePort.ToString()
    MHQ_DEPLOY_ARCH = $Architecture
    MHQ_DEPLOY_EXPECTED_HOST_KEY = $ExpectedHostKey
    MHQ_DEPLOY_BINARY = (Resolve-Path -LiteralPath $binaryPath).Path
    MHQ_DEPLOY_BINARY_SHA256 = $binaryHash
    MHQ_DEPLOY_PUBLISHER = (Resolve-Path -LiteralPath $publisherPath).Path
    MHQ_DEPLOY_PUBLISHER_SHA256 = $publisherHash
    MHQ_DEPLOY_COMPOSE = (Resolve-Path -LiteralPath $composePath).Path
    MHQ_DEPLOY_COMPOSE_SHA256 = $composeHash
    MHQ_DEPLOY_OPERATIONS_DIR = (Resolve-Path -LiteralPath $operationsConfigDir).Path
    MHQ_DEPLOY_GAMEPLAY_CONFIG = $GameplayConfigPath
    MHQ_DEPLOY_GAMEPLAY_CONFIG_SHA256 = $gameplayConfigHash
}
$previousEnvironment = @{}

try {
    foreach ($entry in $deployEnvironment.GetEnumerator()) {
        $previousEnvironment[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key, "Process")
        [Environment]::SetEnvironmentVariable($entry.Key, [string]$entry.Value, "Process")
    }

    Write-Host "[4/6] Uploading the game binary, Compose file, and validated operation YAML files..."
    $deployHelper = @'
import base64
import hashlib
import os
import shlex
import socket
import sys
import time
from datetime import datetime, timezone

import paramiko


host = os.environ["MHQ_DEPLOY_HOST"]
ssh_port = int(os.environ["MHQ_DEPLOY_SSH_PORT"])
username = os.environ["MHQ_DEPLOY_USER"]
password = os.environ["MHQ_SSH_PASSWORD"]
sudo_password = os.environ.get("MHQ_SUDO_PASSWORD") or password
remote_dir = os.environ["MHQ_DEPLOY_REMOTE_DIR"].rstrip("/")
game_port = int(os.environ["MHQ_DEPLOY_GAME_PORT"])
architecture = os.environ["MHQ_DEPLOY_ARCH"]
expected_host_key = os.environ.get("MHQ_DEPLOY_EXPECTED_HOST_KEY", "").strip()
local_binary = os.environ["MHQ_DEPLOY_BINARY"]
expected_binary_hash = os.environ["MHQ_DEPLOY_BINARY_SHA256"].lower()
local_publisher = os.environ["MHQ_DEPLOY_PUBLISHER"]
expected_publisher_hash = os.environ["MHQ_DEPLOY_PUBLISHER_SHA256"].lower()
local_compose = os.environ["MHQ_DEPLOY_COMPOSE"]
expected_compose_hash = os.environ["MHQ_DEPLOY_COMPOSE_SHA256"].lower()
local_operations_dir = os.environ["MHQ_DEPLOY_OPERATIONS_DIR"]
local_gameplay_config = os.environ["MHQ_DEPLOY_GAMEPLAY_CONFIG"]
expected_gameplay_config_hash = os.environ["MHQ_DEPLOY_GAMEPLAY_CONFIG_SHA256"].lower()


def key_fingerprint(key):
    digest = hashlib.sha256(key.asbytes()).digest()
    return "SHA256:" + base64.b64encode(digest).decode("ascii").rstrip("=")


class PinnedHostKeyPolicy(paramiko.MissingHostKeyPolicy):
    def missing_host_key(self, client, hostname, key):
        actual = key_fingerprint(key)
        if actual != expected_host_key:
            raise paramiko.SSHException(
                "SSH host key mismatch: expected %s, received %s"
                % (expected_host_key, actual)
            )
        client.get_host_keys().add(hostname, key.get_name(), key)


def run_remote(client, command, timeout=180):
    if username != "root":
        command = "sudo -S -p '' -- bash -c %s" % shlex.quote(command)
    channel = client.get_transport().open_session(timeout=10)
    channel.set_combine_stderr(True)
    channel.exec_command(command)
    if username != "root":
        channel.sendall((sudo_password + "\n").encode("utf-8"))
        channel.shutdown_write()
    chunks = []
    deadline = time.monotonic() + timeout
    while True:
        while channel.recv_ready():
            chunks.append(channel.recv(65536))
        if channel.exit_status_ready() and not channel.recv_ready():
            break
        if time.monotonic() >= deadline:
            channel.close()
            raise TimeoutError("remote command timed out after %d seconds" % timeout)
        time.sleep(0.05)
    exit_code = channel.recv_exit_status()
    output = b"".join(chunks).decode("utf-8", "replace")
    if output.strip():
        print(output.rstrip())
    if exit_code != 0:
        raise RuntimeError("remote command failed with exit code %d" % exit_code)
    return output


def file_sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


for label, path, expected_hash in (
    ("game binary", local_binary, expected_binary_hash),
    ("configuration publisher", local_publisher, expected_publisher_hash),
    ("Compose file", local_compose, expected_compose_hash),
    ("Gameplay configuration", local_gameplay_config, expected_gameplay_config_hash),
):
    actual_hash = file_sha256(path)
    if actual_hash != expected_hash:
        raise RuntimeError("local %s changed after the PowerShell hash check" % label)
for label, path in (("game binary", local_binary), ("configuration publisher", local_publisher)):
    with open(path, "rb") as executable:
        if executable.read(4) != b"\x7fELF":
            raise RuntimeError("local %s is not an ELF binary" % label)

client = paramiko.SSHClient()
if expected_host_key:
    client.set_missing_host_key_policy(PinnedHostKeyPolicy())
else:
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    print("WARNING: MHQ_DEPLOY_EXPECTED_HOST_KEY is empty; SSH host key will not be pinned.")
client.connect(
    host,
    port=ssh_port,
    username=username,
    password=password,
    timeout=15,
    banner_timeout=15,
    auth_timeout=15,
    allow_agent=False,
    look_for_keys=False,
)

try:
    actual_host_key = key_fingerprint(client.get_transport().get_remote_server_key())
    if expected_host_key:
        if actual_host_key != expected_host_key:
            raise RuntimeError("SSH host key changed after authentication")
        print("SSH host key verified: %s" % actual_host_key)
    else:
        print("WARNING: SSH host key not pinned; connected host key: %s" % actual_host_key)

    quoted_dir = shlex.quote(remote_dir)
    run_remote(
        client,
        "set -eu; test \"$(id -u)\" = 0; "
        "cd %s; test -f compose.yaml; test -f .env; "
        "test -f runtime/Dockerfile; test -f runtime/mhqserver; "
        "docker compose version" % quoted_dir,
        timeout=30,
    )

    stamp = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    staging_dir = "/tmp/mhq-deploy-%s-%d" % (stamp, os.getpid())
    incoming = "%s/mhqserver" % staging_dir
    publisher_incoming = "%s/publish-config" % staging_dir
    compose_incoming = "%s/compose.yaml" % staging_dir
    operations_dir = "%s/config/operations" % remote_dir
    gameplay_incoming = "%s/Gameplay.yaml" % staging_dir
    operation_files = []
    for filename in sorted(os.listdir(local_operations_dir)):
        if os.path.splitext(filename)[1].lower() not in (".yaml", ".yml"):
            continue
        local_path = os.path.join(local_operations_dir, filename)
        remote_path = "%s/operations/%s" % (staging_dir, filename)
        operation_files.append((filename, local_path, remote_path, file_sha256(local_path)))
    run_remote(client, "install -d -m 0755 %s" % shlex.quote(operations_dir), timeout=30)
    sftp = client.open_sftp()
    try:
        sftp.mkdir(staging_dir, mode=0o700)
        sftp.mkdir("%s/operations" % staging_dir, mode=0o700)
        sftp.put(local_binary, incoming)
        sftp.chmod(incoming, 0o555)
        sftp.put(local_publisher, publisher_incoming)
        sftp.chmod(publisher_incoming, 0o555)
        sftp.put(local_compose, compose_incoming)
        sftp.chmod(compose_incoming, 0o444)
        sftp.put(local_gameplay_config, gameplay_incoming)
        sftp.chmod(gameplay_incoming, 0o444)
        for filename, local_path, remote_path, _ in operation_files:
            sftp.put(local_path, remote_path)
            sftp.chmod(remote_path, 0o444)
    finally:
        sftp.close()

    try:
        for label, path, expected_hash in (
            ("game binary", incoming, expected_binary_hash),
            ("configuration publisher", publisher_incoming, expected_publisher_hash),
            ("Compose file", compose_incoming, expected_compose_hash),
            ("Gameplay configuration", gameplay_incoming, expected_gameplay_config_hash),
        ):
            remote_hash_output = run_remote(
                client, "sha256sum %s" % shlex.quote(path), timeout=30
            )
            remote_hash = remote_hash_output.split()[0].lower()
            if remote_hash != expected_hash:
                raise RuntimeError(
                    "uploaded %s hash mismatch: local=%s remote=%s"
                    % (label, expected_hash, remote_hash)
                )
            print("Uploaded %s SHA256 verified: %s" % (label, remote_hash))
        for filename, _, remote_path, expected_hash in operation_files:
            remote_hash_output = run_remote(client, "sha256sum %s" % shlex.quote(remote_path), timeout=30)
            remote_hash = remote_hash_output.split()[0].lower()
            if remote_hash != expected_hash:
                raise RuntimeError("uploaded operation YAML hash mismatch for %s: local=%s remote=%s" % (filename, expected_hash, remote_hash))
            print("Uploaded operation YAML SHA256 verified: %s (%s)" % (filename, remote_hash))
    except Exception:
        run_remote(
            client,
            "rm -rf -- %s" % shlex.quote(staging_dir),
            timeout=30,
        )
        raise

    expected_public_address = "%s:%d" % (host, game_port)
    operation_moves = "\n".join(
        'mv %s %s' % (shlex.quote(remote_path), shlex.quote("%s/%s" % (operations_dir, filename)))
        for filename, _, remote_path, _ in operation_files
    )
    watch_checks = "\n".join(
        '    if [ %s = CustomSkins ]; then pattern="config watcher published CustomSkins"; else pattern="config watcher published name=%s"; fi\n'
        '    if ! printf \'%%s\' "$logs" | grep -Fq "$pattern"; then all_published=0; fi'
        % (shlex.quote(os.path.splitext(filename)[0]), shlex.quote(os.path.splitext(filename)[0]))
        for filename, _, _, _ in operation_files
        if os.path.splitext(filename)[0] != "Gameplay"
    )
    operation_touches = "\n".join(
        "touch %s" % shlex.quote("%s/%s" % (operations_dir, filename))
        for filename, _, _, _ in operation_files
        if os.path.splitext(filename)[0] != "Gameplay"
    )
    watched_names = [
        os.path.splitext(filename)[0]
        for filename, _, _, _ in operation_files
        if os.path.splitext(filename)[0] != "Gameplay"
    ]
    final_watch_name = watched_names[-1]
    if final_watch_name == "CustomSkins":
        final_watch_pattern = "config watcher published CustomSkins"
    else:
        final_watch_pattern = "config watcher published name=%s" % final_watch_name
    remote_script = r'''set -Eeuo pipefail
remote_dir={remote_dir}
staging_dir={staging_dir}
incoming={incoming}
publisher_incoming={publisher_incoming}
compose_incoming={compose_incoming}
gameplay_incoming={gameplay_incoming}
operations_dir={operations_dir}
game_port={game_port}
expected_public_address={expected_public_address}
stamp={stamp}
cd "$remote_dir"
install -d -m 700 /root/mhq-backups
backup="/root/mhq-backups/mhqserver-before-${{stamp}}"
cp -p runtime/mhqserver "$backup"
chmod 600 "$backup"
compose_backup="/root/mhq-backups/compose-before-${{stamp}}.yaml"
operations_backup="/root/mhq-backups/config-operations-before-${{stamp}}"
if [ -f compose.yaml ]; then cp -p compose.yaml "$compose_backup"; else : > "$compose_backup.missing"; fi
if [ -d "$operations_dir" ]; then cp -a "$operations_dir" "$operations_backup"; else : > "$operations_backup.missing"; fi

rollback() {{
    rc=$?
    trap - EXIT
    if [ "$rc" -ne 0 ]; then
        echo "Deployment failed; restoring the previous binary." >&2
        cp "$backup" runtime/mhqserver
        chmod 0555 runtime/mhqserver
        if [ -f "$compose_backup" ]; then cp "$compose_backup" compose.yaml; elif [ -f "$compose_backup.missing" ]; then rm -f compose.yaml; fi
        rm -rf "$operations_dir"
        if [ -d "$operations_backup" ]; then cp -a "$operations_backup" "$operations_dir"; elif [ -f "$operations_backup.missing" ]; then install -d -m 0755 "$operations_dir"; fi
        docker compose build game || true
        docker compose up -d --no-deps --force-recreate game || true
    fi
    rm -rf -- "$staging_dir"
    exit "$rc"
}}
trap rollback EXIT

mv "$incoming" runtime/mhqserver
chmod 0555 runtime/mhqserver
mv "$compose_incoming" compose.yaml
chmod 0444 compose.yaml
install -d -m 0755 "$operations_dir"
rm -f "$operations_dir"/*.yaml "$operations_dir"/*.yml
{operation_moves}
find "$operations_dir" -maxdepth 1 -type f \( -name '*.yaml' -o -name '*.yml' \) -exec chmod 0444 {{}} +
docker compose build game
docker compose up -d --no-deps --force-recreate game

ready=0
for attempt in $(seq 1 60); do
    container_id=$(docker compose ps -q game)
    if [ -n "$container_id" ] && [ "$(docker inspect -f '{{{{.State.Running}}}}' "$container_id")" = "true" ]; then
        logs=$(docker compose logs --no-color --tail=200 game 2>&1 || true)
        if printf '%s' "$logs" | grep -Fq 'MySQL store ready:' && \
           printf '%s' "$logs" | grep -Fq 'Redis cache ready:' && \
           printf '%s' "$logs" | grep -Fq "public gate address: $expected_public_address" && \
           printf '%s' "$logs" | grep -Fq 'mhqserver listening on tcp'; then
            ready=1
            break
        fi
    fi
    sleep 1
done

if [ "$ready" -ne 1 ]; then
    docker compose ps >&2 || true
    docker compose logs --no-color --tail=200 game >&2 || true
    echo 'The new game container did not pass its startup checks.' >&2
    exit 1
fi

# 'listening on tcp' is logged before the watcher finishes its initial content
# scan, so a touch issued right here can land inside that scan. Such a file has
# its post-touch mtime recorded as the baseline stamp and is then swallowed by
# the watcher's initial flag, i.e. it is never published at all. Wait for the
# scan to finish before touching anything.
watcher_scanned=0
for attempt in $(seq 1 60); do
    if docker compose logs --no-color --tail=400 game 2>&1 | grep -Fq 'config watcher enabled'; then
        watcher_scanned=1
        break
    fi
    sleep 1
done
if [ "$watcher_scanned" -ne 1 ]; then
    docker compose logs --no-color --tail=200 game >&2 || true
    echo 'The configuration watcher did not finish its initial scan.' >&2
    exit 1
fi

# The watcher deliberately ignores files seen during its initial scan. Touch
# every non-Gameplay operation file after startup so the exact local YAML set
# is validated, published to MySQL, and hot-reloaded through Redis.
watch_started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
{operation_touches}
watch_ready=0
for attempt in $(seq 1 60); do
    logs=$(docker compose logs --no-color --since "$watch_started" game 2>&1 || true)
    all_published=1
{watch_checks}
    if [ "$all_published" -eq 1 ]; then
        final_publish=$(printf '%s\n' "$logs" | grep -F {final_watch_pattern} | tail -n 1 || true)
        final_revision=$(printf '%s\n' "$final_publish" | sed -n 's/.* revision=\([0-9][0-9]*\).*/\1/p')
        if [ -n "$final_revision" ] && printf '%s' "$logs" | grep -Fq "config reloaded revision=$final_revision "; then
            watch_ready=1
            break
        fi
    fi
    sleep 1
done
if [ "$watch_ready" -ne 1 ]; then
    docker compose logs --no-color --since "$watch_started" game >&2 || true
    echo 'The operation YAML watcher did not publish the complete local file set.' >&2
    exit 1
fi
echo "OPERATION_YAML_HOT_RELOAD=OK"

published=$(docker compose port game 7756)
case "$published" in
    *:"$game_port") ;;
    *) echo "Unexpected game port mapping: $published" >&2; exit 1 ;;
esac

trap - EXIT
echo "REMOTE_DEPLOYMENT=OK"
echo "BACKUP=$backup"
echo "PUBLISHED=$published"
docker compose ps game
docker compose logs --no-color --tail=30 game
'''.format(
        remote_dir=shlex.quote(remote_dir),
        staging_dir=shlex.quote(staging_dir),
        incoming=shlex.quote(incoming),
        publisher_incoming=shlex.quote(publisher_incoming),
        compose_incoming=shlex.quote(compose_incoming),
        gameplay_incoming=shlex.quote(gameplay_incoming),
        operations_dir=shlex.quote(operations_dir),
        operation_moves=operation_moves,
        operation_touches=operation_touches,
        watch_checks=watch_checks,
        final_watch_pattern=shlex.quote(final_watch_pattern),
        game_port=game_port,
        expected_public_address=shlex.quote(expected_public_address),
        stamp=shlex.quote(stamp),
    )
    encoded_script = base64.b64encode(remote_script.encode("utf-8")).decode("ascii")
    print("Rebuilding and restarting only the game container...")
    run_remote(
        client,
        "printf %s %s | base64 -d | bash"
        % (shlex.quote("%s"), shlex.quote(encoded_script)),
        timeout=300,
    )

    print("Checking public TCP connectivity to %s:%d..." % (host, game_port))
    try:
        with socket.create_connection((host, game_port), timeout=10):
            pass
    except Exception:
        print("Public TCP verification failed; restoring the previous binary.", file=sys.stderr)
        backup = "/root/mhq-backups/mhqserver-before-%s" % stamp
        rollback_script = r'''set -Eeuo pipefail
cd {remote_dir}
test -f {backup}
cp {backup} runtime/mhqserver
chmod 0555 runtime/mhqserver
if [ -f {compose_backup} ]; then cp {compose_backup} compose.yaml; elif [ -f {compose_backup_missing} ]; then rm -f compose.yaml; fi
rm -rf {operations_dir}
if [ -d {operations_backup} ]; then cp -a {operations_backup} {operations_dir}; elif [ -f {operations_backup_missing} ]; then install -d -m 0755 {operations_dir}; fi
docker compose build game
docker compose up -d --no-deps --force-recreate game
rm -rf -- {staging_dir}
'''.format(
            remote_dir=shlex.quote(remote_dir),
            backup=shlex.quote(backup),
            compose_backup=shlex.quote("/root/mhq-backups/compose-before-%s.yaml" % stamp),
            compose_backup_missing=shlex.quote("/root/mhq-backups/compose-before-%s.yaml.missing" % stamp),
            operations_dir=shlex.quote(operations_dir),
            operations_backup=shlex.quote("/root/mhq-backups/config-operations-before-%s" % stamp),
            operations_backup_missing=shlex.quote("/root/mhq-backups/config-operations-before-%s.missing" % stamp),
            staging_dir=shlex.quote(staging_dir),
        )
        encoded_rollback = base64.b64encode(rollback_script.encode("utf-8")).decode("ascii")
        run_remote(
            client,
            "printf %s %s | base64 -d | bash"
            % (shlex.quote("%s"), shlex.quote(encoded_rollback)),
            timeout=300,
        )
        raise
    print("PUBLIC_TCP=OK")

    publication_note = "deploy-server game=%s gameplay=%s" % (
        expected_binary_hash[:12],
        expected_gameplay_config_hash[:12],
    )
    publish_script = r'''set -Eeuo pipefail
cd {remote_dir}
staging_dir={staging_dir}
publisher={publisher_incoming}
gameplay={gameplay_incoming}
cleanup() {{
    rm -rf -- "$staging_dir"
}}
trap cleanup EXIT
test -x "$publisher"
test -r "$gameplay"

publish_started=$(date -u +%Y-%m-%dT%H:%M:%SZ)
set +e
publish_output=$(docker compose run --rm --no-deps \
    -v "$publisher:/publish-config:ro,Z" \
    -v "$gameplay:/config/Gameplay.yaml:ro,Z" \
    --entrypoint /publish-config \
    game \
    -name Gameplay \
    -source /config/Gameplay.yaml \
    -publisher deploy-server \
    -note {publication_note} 2>&1)
publish_rc=$?
set -e
printf '%s\n' "$publish_output"
if [ "$publish_rc" -ne 0 ]; then
    echo 'Gameplay configuration publication failed.' >&2
    exit "$publish_rc"
fi

revision=$(printf '%s\n' "$publish_output" | sed -n 's/.*published Gameplay revision=\([0-9][0-9]*\).*/\1/p' | tail -n 1)
if [ -z "$revision" ]; then
    echo 'Gameplay publisher did not report a revision.' >&2
    exit 1
fi

reloaded=0
for attempt in $(seq 1 30); do
    logs=$(docker compose logs --no-color --since "$publish_started" game 2>&1 || true)
    if printf '%s' "$logs" | grep -Fq "config reloaded revision=$revision names=[Gameplay]"; then
        reloaded=1
        break
    fi
    sleep 1
done
if [ "$reloaded" -ne 1 ]; then
    docker compose logs --no-color --since "$publish_started" game >&2 || true
    echo "Gameplay revision $revision was published but the game did not confirm its hot reload." >&2
    exit 1
fi
echo "GAMEPLAY_PUBLICATION=OK revision=$revision"
'''.format(
        remote_dir=shlex.quote(remote_dir),
        staging_dir=shlex.quote(staging_dir),
        publisher_incoming=shlex.quote(publisher_incoming),
        gameplay_incoming=shlex.quote(gameplay_incoming),
        publication_note=shlex.quote(publication_note),
    )
    encoded_publication = base64.b64encode(publish_script.encode("utf-8")).decode("ascii")
    print("Publishing Gameplay configuration through the private Docker network...")
    run_remote(
        client,
        "printf %s %s | base64 -d | bash"
        % (shlex.quote("%s"), shlex.quote(encoded_publication)),
        timeout=180,
    )
except Exception:
    try:
        if "staging_dir" in locals():
            run_remote(client, "rm -rf -- %s" % shlex.quote(staging_dir), timeout=30)
    except Exception:
        pass
    raise
finally:
    client.close()

print("DEPLOYMENT_VERIFIED=OK")
'@

    $deployHelper | & $pythonCommand.Source -
    if ($LASTEXITCODE -ne 0) {
        throw "Remote deployment failed with exit code $LASTEXITCODE"
    }

    Write-Host "[5/6] Remote container, operation YAML watcher, and public TCP $ServerHost`:$GamePort passed verification."
    Write-Host "[6/6] All operation YAML files were synchronized; Gameplay publication and hot reload were confirmed."
    Write-Host "Deployment complete. Published: game binary + complete operation YAML set."
}
finally {
    foreach ($entry in $previousEnvironment.GetEnumerator()) {
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, "Process")
    }
    if ($passwordWasPrompted) {
        Remove-Item Env:MHQ_SSH_PASSWORD -ErrorAction SilentlyContinue
    }
}
