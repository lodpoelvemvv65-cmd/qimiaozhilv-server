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
    [string]$ExpectedHostKey = ""
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

$pythonCommand = Require-Command "py"
$serverRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot "..\.."))
$buildScript = Join-Path $PSScriptRoot "build-linux-release.ps1"
$binaryPath = Join-Path $serverRoot "deploy\runtime\mhqserver"

Write-Host "[1/4] Running Go tests and building Linux/$Architecture game binary..."
& $buildScript -Architecture $Architecture
if ($LASTEXITCODE -ne 0) {
    throw "build-linux-release.ps1 failed with exit code $LASTEXITCODE"
}
if (-not (Test-Path -LiteralPath $binaryPath -PathType Leaf)) {
    throw "Linux game binary was not created: $binaryPath"
}
$binaryHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binaryPath).Hash.ToLowerInvariant()

Write-Host "[2/4] Checking the SSH deployment dependency..."
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
    MHQ_DEPLOY_EXPECTED_HOST_KEY = $ExpectedHostKey
    MHQ_DEPLOY_BINARY = (Resolve-Path -LiteralPath $binaryPath).Path
    MHQ_DEPLOY_BINARY_SHA256 = $binaryHash
}
$previousEnvironment = @{}

try {
    foreach ($entry in $deployEnvironment.GetEnumerator()) {
        $previousEnvironment[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key, "Process")
        [Environment]::SetEnvironmentVariable($entry.Key, [string]$entry.Value, "Process")
    }

    Write-Host "[3/4] Uploading only the game binary and restarting the game container..."
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
expected_host_key = os.environ.get("MHQ_DEPLOY_EXPECTED_HOST_KEY", "").strip()
local_binary = os.environ["MHQ_DEPLOY_BINARY"]
expected_binary_hash = os.environ["MHQ_DEPLOY_BINARY_SHA256"].lower()

def key_fingerprint(key):
    digest = hashlib.sha256(key.asbytes()).digest()
    return "SHA256:" + base64.b64encode(digest).decode("ascii").rstrip("=")

class PinnedHostKeyPolicy(paramiko.MissingHostKeyPolicy):
    def missing_host_key(self, client, hostname, key):
        actual = key_fingerprint(key)
        if actual != expected_host_key:
            raise paramiko.SSHException("SSH host key mismatch: expected %s, received %s" % (expected_host_key, actual))
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

def rollback_remote(client, remote_dir, backup):
    script = "set -Eeuo pipefail; cd %s; test -f %s; cp %s runtime/mhqserver; " \
             "chmod 0555 runtime/mhqserver; docker compose build game; " \
             "docker compose up -d --no-deps --force-recreate game" % (
                 shlex.quote(remote_dir), shlex.quote(backup), shlex.quote(backup)
             )
    run_remote(client, script, timeout=300)

if file_sha256(local_binary) != expected_binary_hash:
    raise RuntimeError("local game binary changed after the PowerShell hash check")
with open(local_binary, "rb") as executable:
    if executable.read(4) != b"\x7fELF":
        raise RuntimeError("local game binary is not an ELF binary")

client = paramiko.SSHClient()
if expected_host_key:
    client.set_missing_host_key_policy(PinnedHostKeyPolicy())
else:
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    print("WARNING: MHQ_DEPLOY_EXPECTED_HOST_KEY is empty; SSH host key will not be pinned.")
client.connect(host, port=ssh_port, username=username, password=password,
               timeout=15, banner_timeout=15, auth_timeout=15,
               allow_agent=False, look_for_keys=False)
stamp = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
staging_dir = "/tmp/mhq-deploy-code-only-%s-%d" % (stamp, os.getpid())
incoming = "%s/mhqserver" % staging_dir
backup = "/root/mhq-backups/mhqserver-before-%s" % stamp

try:
    actual_host_key = key_fingerprint(client.get_transport().get_remote_server_key())
    if expected_host_key:
        if actual_host_key != expected_host_key:
            raise RuntimeError("SSH host key changed after authentication")
        print("SSH host key verified: %s" % actual_host_key)
    else:
        print("WARNING: SSH host key not pinned; connected host key: %s" % actual_host_key)
    run_remote(client, "set -eu; test \"$(id -u)\" = 0; cd %s; "
               "test -f compose.yaml; test -f .env; "
               "test -f runtime/Dockerfile; test -f runtime/mhqserver; "
               "docker compose version" % shlex.quote(remote_dir), timeout=30)
    sftp = client.open_sftp()
    try:
        sftp.mkdir(staging_dir, mode=0o700)
        sftp.put(local_binary, incoming)
        sftp.chmod(incoming, 0o555)
    finally:
        sftp.close()
    remote_hash = run_remote(client, "sha256sum %s" % shlex.quote(incoming), timeout=30).split()[0].lower()
    if remote_hash != expected_binary_hash:
        run_remote(client, "rm -f -- %s" % shlex.quote(incoming), timeout=30)
        raise RuntimeError("uploaded game binary hash mismatch: local=%s remote=%s" % (expected_binary_hash, remote_hash))
    print("Uploaded game binary SHA256 verified: %s" % remote_hash)

    remote_script = r'''set -Eeuo pipefail
remote_dir={remote_dir}
staging_dir={staging_dir}
incoming={incoming}
backup={backup}
game_port={game_port}
expected_public_address={expected_public_address}
cd "$remote_dir"
install -d -m 700 /root/mhq-backups
cp -p runtime/mhqserver "$backup"
chmod 600 "$backup"

rollback() {{
    rc=$?
    trap - EXIT
    if [ "$rc" -ne 0 ]; then
        echo "Code-only deployment failed; restoring the previous binary." >&2
        cp "$backup" runtime/mhqserver
        chmod 0555 runtime/mhqserver
        docker compose build game || true
        docker compose up -d --no-deps --force-recreate game || true
    fi
    rm -rf -- "$staging_dir"
    exit "$rc"
}}
trap rollback EXIT

mv "$incoming" runtime/mhqserver
chmod 0555 runtime/mhqserver
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
    echo 'The code-only game container did not pass its startup checks.' >&2
    exit 1
fi

published=$(docker compose port game 7756)
case "$published" in
    *:"$game_port") ;;
    *) echo "Unexpected game port mapping: $published" >&2; exit 1 ;;
esac

trap - EXIT
rm -rf -- "$staging_dir"
echo "REMOTE_CODE_ONLY_DEPLOYMENT=OK"
echo "BACKUP=$backup"
echo "PUBLISHED=$published"
docker compose ps game
docker compose logs --no-color --tail=30 game
'''.format(
        remote_dir=shlex.quote(remote_dir),
        staging_dir=shlex.quote(staging_dir),
        incoming=shlex.quote(incoming),
        backup=shlex.quote(backup),
        game_port=game_port,
        expected_public_address=shlex.quote("%s:%d" % (host, game_port)),
    )
    encoded_script = base64.b64encode(remote_script.encode("utf-8")).decode("ascii")
    run_remote(client, "printf %s %s | base64 -d | bash" % (shlex.quote("%s"), shlex.quote(encoded_script)), timeout=300)
    print("[4/4] Checking public TCP connectivity to %s:%d..." % (host, game_port))
    try:
        with socket.create_connection((host, game_port), timeout=10):
            pass
    except Exception:
        print("Public TCP verification failed; restoring the previous binary.", file=sys.stderr)
        rollback_remote(client, remote_dir, backup)
        raise
    print("PUBLIC_TCP=OK")
except Exception:
    try:
        run_remote(client, "rm -rf -- %s" % shlex.quote(staging_dir), timeout=30)
    except Exception:
        pass
    raise
finally:
    client.close()

print("CODE_ONLY_DEPLOYMENT_VERIFIED=OK")
'@

    $deployHelper | & $pythonCommand.Source -
    if ($LASTEXITCODE -ne 0) {
        throw "Remote code-only deployment failed with exit code $LASTEXITCODE"
    }
}
finally {
    foreach ($entry in $previousEnvironment.GetEnumerator()) {
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, "Process")
    }
    if ($passwordWasPrompted) {
        Remove-Item Env:MHQ_SSH_PASSWORD -ErrorAction SilentlyContinue
    }
}
