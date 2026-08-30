[CmdletBinding()]
param(
    [string]$GoPath = "go"
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
    throw "Reproducible-build evidence requires Go 1.27.0; found: $version"
}

& (Join-Path $PSScriptRoot "verify-zero-dep.ps1") -GoPath $goExecutable -RepositoryRoot $PSScriptRoot

$repository = (Resolve-Path -LiteralPath $PSScriptRoot).Path
$temporaryParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar)
$temporaryRoot = Join-Path $temporaryParent ("anvil-repro-" + [guid]::NewGuid().ToString("N"))
$previousCGO = $env:CGO_ENABLED
$previousToolchain = $env:GOTOOLCHAIN
$previousGoCache = $env:GOCACHE
$previousGoModCache = $env:GOMODCACHE

New-Item -ItemType Directory -Path $temporaryRoot | Out-Null

try {
    $env:CGO_ENABLED = "0"
    $env:GOTOOLCHAIN = "local"
    $env:GOCACHE = Join-Path $temporaryRoot "build-cache"
    $env:GOMODCACHE = Join-Path $temporaryRoot "module-cache"
    $first = Join-Path $temporaryRoot "anvil-first"
    $second = Join-Path $temporaryRoot "anvil-second"
    $arguments = @(
        "build",
        "-trimpath",
        "-buildvcs=false",
        "-ldflags=-s -w -buildid=",
        "-o"
    )

    Push-Location $repository
    try {
        & $goExecutable @arguments $first "./src"
        if ($LASTEXITCODE -ne 0) {
            throw "First reproducible build failed with exit code $LASTEXITCODE"
        }
        & $goExecutable @arguments $second "./src"
        if ($LASTEXITCODE -ne 0) {
            throw "Second reproducible build failed with exit code $LASTEXITCODE"
        }
    }
    finally {
        Pop-Location
    }

    $firstHash = (Get-FileHash -LiteralPath $first -Algorithm SHA256).Hash
    $secondHash = (Get-FileHash -LiteralPath $second -Algorithm SHA256).Hash
    if ($firstHash -ne $secondHash) {
        throw "Reproducible-build mismatch: $firstHash != $secondHash"
    }

    "toolchain=$version"
    "flags=-trimpath -buildvcs=false -ldflags=-s -w -buildid= CGO_ENABLED=0"
    "first_sha256=$firstHash"
    "second_sha256=$secondHash"
    "reproducible=true"
}
finally {
    $env:CGO_ENABLED = $previousCGO
    $env:GOTOOLCHAIN = $previousToolchain
    $env:GOCACHE = $previousGoCache
    $env:GOMODCACHE = $previousGoModCache
    if (Test-Path -LiteralPath $temporaryRoot) {
        $resolved = (Resolve-Path -LiteralPath $temporaryRoot).Path
        $expectedPrefix = $temporaryParent + [IO.Path]::DirectorySeparatorChar + "anvil-repro-"
        if (-not $resolved.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to clean unexpected path: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}
