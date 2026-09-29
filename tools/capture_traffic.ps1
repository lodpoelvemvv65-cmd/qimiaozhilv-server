#Requires -RunAsAdministrator
<#
.SYNOPSIS
    用 Windows 内置的 pktmon 抓 TCP 流量，转换并归档到 参考数据\抓包归档\。

.DESCRIPTION
    线上《梦幻奇遇记》服务端是 101.43.6.155，实测同一 IP 上有 7757/7758（游戏协议）
    和 2083（资源/CDN）多个端口，因此筛选器默认只按 IP 过滤、不限端口；限端口反而会
    漏掉真正的游戏流量。

    pktmon 抓不到回环（127.0.0.1）流量——它走 NDIS 旁路，所以本脚本只用于抓远程服务器；
    本地服（127.0.0.1:7756）的协议回归请直接用 server-mysql/test/ 里连 127.0.0.1 的脚本。

    两个动作分开跑，中间的「去游戏里操作」由人工完成：

        # 1. 开始抓（会清掉已有筛选器，避免串味）
        .\tools\capture_traffic.ps1 -Mode start -Label character-build
        # 2. 按脚本提示在游戏里操作完，回来停并归档
        .\tools\capture_traffic.ps1 -Mode stop

    想随时看状态（是否在抓、筛选器、开始时间）用 -Mode status。

.PARAMETER Mode
    start 开始抓；stop 停止并把 pcapng + etl 归档；status 查看当前状态。

.PARAMETER Target
    目标服务器 IP，默认线上 101.43.6.155。抓自建服务器时传其地址。

.PARAMETER Port
    限定端口，0（默认）表示不限端口。线上同一 IP 有 7757/7758/2083 多个端口，
    除非确定只关心一个端口，否则保持 0。

.PARAMETER Label
    用途标签，进文件名：online-<Label>-<日期>.pcapng，例如 character-build。

.PARAMETER Prefix
    文件名前缀，默认 online（自建远程服可传 local-public）。

.PARAMETER MaxFileMB
    循环日志上限，默认 2048MB。游戏协议流量很小，正常一次会话几十 MB 足够；
    超过上限会从头覆盖，所以长时间挂机抓包可以调大。

.PARAMETER ArchiveDir
    归档目录，默认工作区 参考数据\抓包归档。
#>
[CmdletBinding()]
param(
    [ValidateSet('start', 'stop', 'status')]
    [string]$Mode = 'start',

    [string]$Target = '101.43.6.155',
    [int]$Port = 0,
    [string]$Label = 'character-build',
    [string]$Prefix = 'online',
    [int]$MaxFileMB = 2048,
    [string]$ArchiveDir = ''
)

$ErrorActionPreference = 'Stop'

# 工作区根目录 = 本脚本所在目录的上一级（tools\ 的上一层）
$WorkspaceRoot = Split-Path -Parent $PSScriptRoot
if (-not $ArchiveDir) { $ArchiveDir = Join-Path $WorkspaceRoot '参考数据\抓包归档' }

$EtlPath = Join-Path $env:TEMP 'mhq_capture.etl'
$PcapngPath = Join-Path $env:TEMP 'mhq_capture.pcapng'
$StatePath = Join-Path $env:TEMP 'mhq_capture.state.txt'

function Get-State {
    if (-not (Test-Path $StatePath)) { return $null }
    $state = @{}
    foreach ($line in Get-Content -LiteralPath $StatePath -Encoding UTF8) {
        $parts = $line -split '=', 2
        if ($parts.Count -eq 2) { $state[$parts[0]] = $parts[1] }
    }
    return $state
}

function Write-State($target, $label, $prefix, $archive) {
    @(
        "target=$target"
        "label=$label"
        "prefix=$prefix"
        "archive=$archive"
        "started=$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')"
    ) | Set-Content -LiteralPath $StatePath -Encoding UTF8
}

function Start-Capture {
    Write-Host "清空已有筛选器…"
    pktmon filter remove | Out-Null

    $filterArgs = @('filter', 'add', 'MHQCapture', '-i', $Target, '-t', 'TCP')
    if ($Port -gt 0) { $filterArgs += @('-p', "$Port") }
    Write-Host "添加筛选器：$($filterArgs -join ' ')"
    & pktmon @filterArgs | Out-Null

    if (Test-Path $EtlPath) { Remove-Item -LiteralPath $EtlPath -Force }
    Write-Host "启动捕获（--comp nics --pkt-size 0，整包、不含重复的分层副本）…"
    & pktmon start --capture --comp nics --pkt-size 0 --file-size $MaxFileMB --file-name $EtlPath | Out-Null

    Write-State $Target $Label $Prefix $ArchiveDir

    $shown = if ($Port -gt 0) { "$Target`:$Port" } else { "$Target 全部 TCP 端口" }
    Write-Host ""
    Write-Host "=== 已在抓 $shown ===" -ForegroundColor Green
    Write-Host "客户端：D:\梦幻奇遇记-2026.1.3.2（电脑版）\梦幻奇遇记-2026.1.3.2\梦幻奇遇记.exe"
    Write-Host "（原版客户端，连线上 101.43.6.155；不要用工作区里改过地址的 client-test / client-127.0.0.1）"
    Write-Host ""
    Write-Host "请在游戏里依次做这些操作，做完回来执行 stop："
    Write-Host "  1. 登录进入角色，原地停 5 秒（拿 20252→20003 角色信息 + 20170 面板全量）"
    Write-Host "  2. 打开人物信息 → 属性页，停 5 秒，把面板数字拍照记下"
    Write-Host "  3. 打开背包 → 星魂界面，逐页翻一遍（最关键：20400/20401 星魂全量与主副属性）"
    Write-Host "  4. 打开装备/穿戴栏（20259/20260 背包与穿戴快照）"
    Write-Host "  5. 脱一件装备再穿回去（20273/20274，用增量反推装备加成）"
    Write-Host "  6. 原地停 2 分钟不动（挂机经验节拍基线）"
    Write-Host ""
    Write-Host "停并归档： .\tools\capture_traffic.ps1 -Mode stop"
}

function Stop-Capture {
    $state = Get-State
    if (-not $state) {
        Write-Warning "没有找到抓包状态文件（$StatePath）。先执行 -Mode start。"
        return
    }

    Write-Host "停止捕获…"
    & pktmon stop | Out-Null
    Start-Sleep -Seconds 1

    if (-not (Test-Path $EtlPath)) {
        throw "没有生成 $EtlPath —— 捕获期间可能没有任何匹配流量（确认游戏确实连的是 $($state['target'])）"
    }

    Write-Host "转换为 pcapng…"
    if (Test-Path $PcapngPath) { Remove-Item -LiteralPath $PcapngPath -Force }
    $convert = & pktmon etl2pcap $EtlPath --out $PcapngPath 2>&1
    $convert | Where-Object { $_ -match '数据包总数|格式化数据包数|数据包丢弃' } | ForEach-Object { Write-Host "  $_" }
    if (-not (Test-Path $PcapngPath)) { throw "转换失败：$convert" }

    # 归档目录以 start 时记录的为准：stop 时不必重复传 -ArchiveDir，传了也以 start 为准。
    if ($state['archive']) { $ArchiveDir = $state['archive'] }
    if (-not (Test-Path $ArchiveDir)) { New-Item -ItemType Directory -Path $ArchiveDir -Force | Out-Null }
    $stamp = Get-Date -Format 'yyyyMMdd'
    $baseName = "$($state['prefix'])-$($state['label'])-$stamp"
    $suffix = ''
    $index = 2
    while ((Test-Path (Join-Path $ArchiveDir "$baseName$suffix.pcapng")) -or
           (Test-Path (Join-Path $ArchiveDir "$baseName$suffix.etl"))) {
        $suffix = "-$index"
        $index++
    }

    $etlTarget = Join-Path $ArchiveDir "$baseName$suffix.etl"
    $pcapngTarget = Join-Path $ArchiveDir "$baseName$suffix.pcapng"
    Move-Item -LiteralPath $EtlPath -Destination $etlTarget -Force
    Move-Item -LiteralPath $PcapngPath -Destination $pcapngTarget -Force
    Remove-Item -LiteralPath $StatePath -Force

    Write-Host ""
    Write-Host "=== 已归档 ===" -ForegroundColor Green
    Write-Host "  $(Split-Path -Leaf $pcapngTarget)"
    Write-Host "  $(Split-Path -Leaf $etlTarget)（原始文件，按归档约定一并保留）"
    Write-Host ""
    Write-Host "接着分析："
    Write-Host "  cd server-mysql"
    # 归档目录若在工作区内，按 test/ 脚本的用法给出 ../ 相对路径；否则给绝对路径。
    $shownPath = $pcapngTarget
    if ($pcapngTarget.StartsWith($WorkspaceRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
        $shownPath = '../' + ($pcapngTarget.Substring($WorkspaceRoot.Length).TrimStart('\') -replace '\\', '/')
    }
    Write-Host "  python test/inspect_character_build_capture.py `"$shownPath`" --bonus"
    Write-Host "  # 想只看某一类消息：--op 20400 / --op 20259 / --json out.json"
}

function Show-Status {
    Write-Host "== pktmon 状态 =="
    & pktmon status
    Write-Host ""
    Write-Host "== 当前筛选器 =="
    & pktmon filter list
    Write-Host ""
    $state = Get-State
    if ($state) {
        Write-Host "== 抓包状态文件 =="
        $state.GetEnumerator() | Sort-Object Name | ForEach-Object { Write-Host "  $($_.Key) = $($_.Value)" }
    } else {
        Write-Host "== 抓包状态文件 ==`n  无（当前没有正在进行的抓包记录）"
    }
}

switch ($Mode) {
    'start' { Start-Capture }
    'stop' { Stop-Capture }
    'status' { Show-Status }
}
