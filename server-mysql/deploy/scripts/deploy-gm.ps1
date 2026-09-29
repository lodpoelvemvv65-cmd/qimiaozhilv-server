[CmdletBinding()]
param(
    [string]$ServerHost = "127.0.0.1",
    [int]$SshPort = 22,
    [string]$SshUser = "root",
    [string]$RemoteDir = "/opt/mhq",
    [int]$GmPort = 8897,
    # 留空则跳过 SSH 主机指纹校验（仅建议在可信网络中使用）
    [string]$ExpectedHostKey = "",
    [string]$AdminUsername = "admin",
    [string]$AdminDisplayName = "GM Administrator"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Require-Command([string]$Name) {
    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if (-not $command) { throw "Required command is not installed or not in PATH: $Name" }
    return $command
}

$null = Require-Command "py"
$serverRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot "..\.."))
$webRoot = [IO.Path]::GetFullPath((Join-Path $serverRoot "..\web"))
$gmRoot = Join-Path $serverRoot "deploy\gm"
$distRoot = Join-Path $webRoot "dist"
foreach ($path in @(
    (Join-Path $gmRoot "gm-api"), (Join-Path $gmRoot "gm-admin"),
    (Join-Path $gmRoot "Dockerfile"), (Join-Path $gmRoot "compose.yaml"),
    (Join-Path $gmRoot "nginx.conf"), $distRoot
)) {
    if (-not (Test-Path -LiteralPath $path)) { throw "GM deployment artifact is missing: $path" }
}

if ([string]::IsNullOrWhiteSpace($env:MHQ_SSH_PASSWORD)) {
    throw "Set MHQ_SSH_PASSWORD in the current process before deploying."
}
if ([string]::IsNullOrWhiteSpace($env:GM_ADMIN_PASSWORD) -or $env:GM_ADMIN_PASSWORD.Length -lt 12) {
    throw "Set GM_ADMIN_PASSWORD to a password of at least 12 characters in the current process."
}

$deployEnvironment = @{
    MHQ_DEPLOY_HOST = $ServerHost
    MHQ_DEPLOY_SSH_PORT = $SshPort.ToString()
    MHQ_DEPLOY_USER = $SshUser
    MHQ_DEPLOY_REMOTE_DIR = $RemoteDir.TrimEnd('/')
    MHQ_DEPLOY_GM_PORT = $GmPort.ToString()
    MHQ_DEPLOY_EXPECTED_HOST_KEY = $ExpectedHostKey
    MHQ_DEPLOY_ADMIN_USERNAME = $AdminUsername
    MHQ_DEPLOY_ADMIN_DISPLAY_NAME = $AdminDisplayName
    MHQ_DEPLOY_GM_ROOT = (Resolve-Path -LiteralPath $gmRoot).Path
    MHQ_DEPLOY_DIST_ROOT = (Resolve-Path -LiteralPath $distRoot).Path
}
$previousEnvironment = @{}
foreach ($entry in $deployEnvironment.GetEnumerator()) {
    $previousEnvironment[$entry.Key] = [Environment]::GetEnvironmentVariable($entry.Key, "Process")
    [Environment]::SetEnvironmentVariable($entry.Key, [string]$entry.Value, "Process")
}

try {
    $helper = @'
import base64
import hashlib
import os
import posixpath
import shlex
import socket
import time
from datetime import datetime, timezone

import paramiko

host = os.environ["MHQ_DEPLOY_HOST"]
ssh_port = int(os.environ["MHQ_DEPLOY_SSH_PORT"])
username = os.environ["MHQ_DEPLOY_USER"]
password = os.environ["MHQ_SSH_PASSWORD"]
sudo_password = os.environ.get("MHQ_SUDO_PASSWORD") or password
remote_dir = os.environ["MHQ_DEPLOY_REMOTE_DIR"]
gm_port = int(os.environ["MHQ_DEPLOY_GM_PORT"])
expected_host_key = os.environ.get("MHQ_DEPLOY_EXPECTED_HOST_KEY", "").strip()
local_root = os.environ["MHQ_DEPLOY_GM_ROOT"]
local_dist = os.environ["MHQ_DEPLOY_DIST_ROOT"]
admin_username = os.environ["MHQ_DEPLOY_ADMIN_USERNAME"]
admin_display_name = os.environ["MHQ_DEPLOY_ADMIN_DISPLAY_NAME"]
admin_password = os.environ["GM_ADMIN_PASSWORD"]

def fingerprint(key):
    return "SHA256:" + base64.b64encode(hashlib.sha256(key.asbytes()).digest()).decode().rstrip("=")

class Pinned(paramiko.MissingHostKeyPolicy):
    def missing_host_key(self, client, hostname, key):
        actual = fingerprint(key)
        if actual != expected_host_key:
            raise paramiko.SSHException("SSH host key mismatch")
        client.get_host_keys().add(hostname, key.get_name(), key)

def run(client, command, timeout=240):
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
            raise TimeoutError("remote command timed out")
        time.sleep(0.05)
    output = b"".join(chunks).decode("utf-8", "replace")
    if output.strip():
        print(output.rstrip())
    code = channel.recv_exit_status()
    if code != 0:
        raise RuntimeError("remote command failed with exit code %d" % code)
    return output

def digest(path):
    h = hashlib.sha256()
    with open(path, "rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()

files = [
    ("Dockerfile", os.path.join(local_root, "Dockerfile")),
    ("compose.yaml", os.path.join(local_root, "compose.yaml")),
    ("nginx.conf", os.path.join(local_root, "nginx.conf")),
    ("gm-api", os.path.join(local_root, "gm-api")),
    ("gm-admin", os.path.join(local_root, "gm-admin")),
]
for name, path in files:
    if digest(path) == "":
        raise RuntimeError("empty deployment artifact: " + name)
    if name in ("gm-api", "gm-admin"):
        with open(path, "rb") as source:
            if source.read(4) != b"\x7fELF":
                raise RuntimeError("GM executable is not a Linux ELF binary: " + name)
for root, _, names in os.walk(local_dist):
    for name in names:
        path = os.path.join(root, name)
        relative = os.path.relpath(path, local_dist).replace(os.sep, "/")
        files.append(("dist/" + relative, path))

client = paramiko.SSHClient()
if expected_host_key:
    client.set_missing_host_key_policy(Pinned())
else:
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())
    print("WARNING: MHQ_DEPLOY_EXPECTED_HOST_KEY is empty; SSH host key will not be pinned.")
client.connect(host, port=ssh_port, username=username, password=password,
               timeout=15, banner_timeout=15, auth_timeout=15,
               allow_agent=False, look_for_keys=False)
try:
    connected_host_key = fingerprint(client.get_transport().get_remote_server_key())
    if expected_host_key:
        if connected_host_key != expected_host_key:
            raise RuntimeError("SSH host key changed after authentication")
        print("SSH host key verified: %s" % connected_host_key)
    else:
        print("WARNING: SSH host key not pinned; connected host key: %s" % connected_host_key)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    staging_dir = "/tmp/mhq-gm-deploy-%s-%d" % (stamp, os.getpid())
    incoming = "%s/gm" % staging_dir
    final_dir = "%s/gm" % remote_dir
    backup = "/root/mhq-backups/gm-before-%s" % stamp
    run(client, "set -eu; test -f {0}/.env; test -d /var/run; docker network inspect mhq_backend >/dev/null".format(shlex.quote(remote_dir)), 30)
    sftp = client.open_sftp()
    try:
        sftp.mkdir(staging_dir, mode=0o700)
        sftp.mkdir("%s/dist" % staging_dir, mode=0o700)
        created_dirs = {staging_dir, "%s/dist" % staging_dir}
        for relative, local_path in files:
            remote_path = posixpath.join(incoming, relative)
            parent = posixpath.dirname(remote_path)
            if parent not in created_dirs:
                missing = []
                current = parent
                while current not in created_dirs and current != "/":
                    missing.append(current)
                    current = posixpath.dirname(current)
                for directory in reversed(missing):
                    sftp.mkdir(directory, mode=0o700)
                    created_dirs.add(directory)
            sftp.put(local_path, remote_path)
            if relative in ("gm-api", "gm-admin"):
                sftp.chmod(remote_path, 0o555)
            else:
                sftp.chmod(remote_path, 0o444)
    finally:
        sftp.close()
    for relative, local_path in files:
        remote_path = posixpath.join(incoming, relative)
        expected = digest(local_path)
        actual = run(client, "sha256sum %s" % shlex.quote(remote_path), 30).split()[0].lower()
        if actual != expected:
            raise RuntimeError("uploaded artifact hash mismatch: " + relative)
    run(client, "set -eu; find %s -type d -exec chmod 0755 {} +" % shlex.quote(incoming), 30)
    run(client, "set -eu; mkdir -p /root/mhq-backups; if [ -d {final} ]; then mv {final} {backup}; fi; mv {incoming} {final}; cd {final}; docker compose --env-file {root}/.env -f compose.yaml config >/dev/null".format(final=shlex.quote(final_dir), backup=shlex.quote(backup), incoming=shlex.quote(incoming), root=shlex.quote(remote_dir)), 30)
    compose = "cd {final}; docker compose --env-file {root}/.env -f compose.yaml build gm-api && docker compose --env-file {root}/.env -f compose.yaml up -d --force-recreate gm-api nginx".format(final=shlex.quote(final_dir), root=shlex.quote(remote_dir))
    run(client, compose, 300)
    run(client, "cd {final}; docker compose --env-file {root}/.env -f compose.yaml ps".format(final=shlex.quote(final_dir), root=shlex.quote(remote_dir)), 30)
    admin_env = shlex.quote(admin_password)
    admin_cmd = "cd {final}; docker compose --env-file {root}/.env -f compose.yaml run --rm --no-deps -e GM_ADMIN_PASSWORD={pw} --entrypoint /gm-admin gm-api -username {user} -display-name {display} -role superadmin".format(final=shlex.quote(final_dir), root=shlex.quote(remote_dir), pw=admin_env, user=shlex.quote(admin_username), display=shlex.quote(admin_display_name))
    run(client, admin_cmd, 120)
    health = run(client, "curl -fsS http://127.0.0.1:{0}/api/v1/health".format(gm_port), 30)
    if '"status":"ok"' not in health and '"status": "ok"' not in health:
        raise RuntimeError("GM health endpoint did not return status ok")
    run(client, "curl -fsS http://127.0.0.1:{0}/ | grep -Fq 'MHQ'".format(gm_port), 30)
    with socket.create_connection((host, gm_port), timeout=10):
        pass
    print("GM_DEPLOYMENT=OK")
    print("GM_PUBLIC=http://%s:%d/" % (host, gm_port))
    print("GM_ADMIN_USERNAME=%s" % admin_username)
except Exception:
    try:
        if "staging_dir" in locals():
            run(client, "rm -rf -- %s" % shlex.quote(staging_dir), 30)
    except Exception:
        pass
    raise
finally:
    client.close()
'@
    $helper | & py -
    if ($LASTEXITCODE -ne 0) { throw "GM deployment failed with exit code $LASTEXITCODE" }
}
finally {
    foreach ($entry in $previousEnvironment.GetEnumerator()) {
        [Environment]::SetEnvironmentVariable($entry.Key, $entry.Value, "Process")
    }
}
