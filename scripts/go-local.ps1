param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]] $GoArgs
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$cacheRoot = Join-Path $repoRoot ".cache"

$env:GOCACHE = Join-Path $cacheRoot "go-build"
$env:GOMODCACHE = Join-Path $cacheRoot "go-mod"
$env:GOPATH = Join-Path $cacheRoot "go-path"
$env:GOTMPDIR = Join-Path $cacheRoot "go-tmp"
$env:TEMP = $env:GOTMPDIR
$env:TMP = $env:GOTMPDIR
$env:GOENV = "off"
$env:GOTELEMETRY = "off"

@(
    $env:GOCACHE,
    $env:GOMODCACHE,
    $env:GOPATH,
    $env:GOTMPDIR
) | ForEach-Object {
    New-Item -ItemType Directory -Force -Path $_ | Out-Null
}

& go @GoArgs
exit $LASTEXITCODE
