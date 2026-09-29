param(
    [ValidateSet("amd64", "arm64")]
    [string]$Architecture = "amd64"
)

$ErrorActionPreference = "Stop"
$serverRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot "..\.."))
$runtimeDir = Join-Path $serverRoot "deploy\runtime"
$outputPath = Join-Path $runtimeDir "mhqserver"
$toolsDir = Join-Path $serverRoot "deploy\tools"
$publisherPath = Join-Path $toolsDir "publish-config"

New-Item -ItemType Directory -Path $runtimeDir -Force | Out-Null
New-Item -ItemType Directory -Path $toolsDir -Force | Out-Null

$previousGoOS = $env:GOOS
$previousGoArch = $env:GOARCH
$previousCGO = $env:CGO_ENABLED
$previousTestDSN = $env:MHQ_TEST_MYSQL_DSN

# The store/persistence tests need a local MySQL account that can create and
# drop databases. Without MHQ_TEST_MYSQL_DSN, mysqlTestDSN in mysql_test.go
# calls t.Skip, so 56 tests silently do not run and the `go test ./...` gate
# before a release checks much less than it looks like. Keep this ASCII-only:
# the file has no BOM and Windows PowerShell would mis-decode non-ASCII.
# An externally provided DSN always wins.
if (-not $env:MHQ_TEST_MYSQL_DSN) {
    $env:MHQ_TEST_MYSQL_DSN = "root:123456@tcp(127.0.0.1:3306)/"
}
try {
    Push-Location $serverRoot
    try {
        go test ./...
        if ($LASTEXITCODE -ne 0) {
            throw "go test failed"
        }

        $env:GOOS = "linux"
        $env:GOARCH = $Architecture
        $env:CGO_ENABLED = "0"
        go build -buildvcs=false -tags timetzdata -trimpath -ldflags "-s -w -buildid=" -o $outputPath .
        if ($LASTEXITCODE -ne 0) {
            throw "linux game build failed"
        }
        go build -buildvcs=false -tags timetzdata -trimpath -ldflags "-s -w -buildid=" -o $publisherPath .\cmd\publish-config
        if ($LASTEXITCODE -ne 0) {
            throw "linux configuration publisher build failed"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:GOOS = $previousGoOS
    $env:GOARCH = $previousGoArch
    $env:CGO_ENABLED = $previousCGO
    $env:MHQ_TEST_MYSQL_DSN = $previousTestDSN
}

foreach ($artifactPath in @($outputPath, $publisherPath)) {
    $header = [System.IO.File]::ReadAllBytes($artifactPath)[0..3]
    if ($header[0] -ne 0x7f -or $header[1] -ne 0x45 -or $header[2] -ne 0x4c -or $header[3] -ne 0x46) {
        throw "output is not an ELF binary: $artifactPath"
    }
}

$hash = Get-FileHash -Algorithm SHA256 -LiteralPath $outputPath
$publisherHash = Get-FileHash -Algorithm SHA256 -LiteralPath $publisherPath
Write-Host "Linux/$Architecture binary: $outputPath"
Write-Host "SHA256: $($hash.Hash)"
Write-Host "Linux/$Architecture config publisher: $publisherPath"
Write-Host "SHA256: $($publisherHash.Hash)"
