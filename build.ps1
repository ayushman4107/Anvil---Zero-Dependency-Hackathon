[CmdletBinding()]
param(
    [string]$GoPath = "go",
    [string]$OutputPath = "anvil.exe"
)

$ErrorActionPreference = "Stop"

$goCommand = Get-Command -Name $GoPath -ErrorAction Stop
$goExecutable = $goCommand.Source
if (-not $goExecutable) {
    $goExecutable = $goCommand.Path
}

$version = (& $goExecutable version | Out-String).Trim()
if ($LASTEXITCODE -ne 0) {
    throw "Unable to execute the Go toolchain at $goExecutable"
}
if ($version -notmatch '\bgo1\.27\.0\b') {
    throw "Anvil requires Go 1.27.0; found: $version"
}

$repository = (Resolve-Path -LiteralPath $PSScriptRoot).Path
if ([IO.Path]::IsPathRooted($OutputPath)) {
    $output = [IO.Path]::GetFullPath($OutputPath)
}
else {
    $output = [IO.Path]::GetFullPath((Join-Path $repository $OutputPath))
}
$outputParent = Split-Path -Parent $output
if (-not (Test-Path -LiteralPath $outputParent -PathType Container)) {
    throw "Output directory does not exist: $outputParent"
}

& (Join-Path $repository "verify-zero-dep.ps1") -GoPath $goExecutable -RepositoryRoot $repository

$previousCGO = $env:CGO_ENABLED
$previousToolchain = $env:GOTOOLCHAIN
try {
    $env:CGO_ENABLED = "0"
    $env:GOTOOLCHAIN = "local"
    Push-Location $repository
    try {
        & $goExecutable build `
            -trimpath `
            -buildvcs=false `
            '-ldflags=-s -w -buildid=' `
            -o $output `
            ./src
        if ($LASTEXITCODE -ne 0) {
            throw "Anvil build failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:CGO_ENABLED = $previousCGO
    $env:GOTOOLCHAIN = $previousToolchain
}

$hash = (Get-FileHash -LiteralPath $output -Algorithm SHA256).Hash
"artifact=$output"
"sha256=$hash"
