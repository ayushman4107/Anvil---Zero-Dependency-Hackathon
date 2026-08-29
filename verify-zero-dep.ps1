[CmdletBinding()]
param(
    [string]$GoPath = "go",
    [string]$RepositoryRoot = $PSScriptRoot
)

$ErrorActionPreference = "Stop"

function Invoke-GoChecked {
    param(
        [Parameter(Mandatory = $true)]
        [string[]]$Arguments
    )

    $output = & $script:GoExecutable @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "go $($Arguments -join ' ') failed:`n$($output -join [Environment]::NewLine)"
    }
    return @($output)
}

function Get-ProductionImports {
    param(
        [Parameter(Mandatory = $true)]
        [IO.FileInfo[]]$Files
    )

    $imports = [Collections.Generic.List[string]]::new()
    foreach ($file in $Files) {
        $insideImportBlock = $false
        foreach ($line in [IO.File]::ReadLines($file.FullName)) {
            if (-not $insideImportBlock) {
                if ($line -match '^\s*import\s*\(') {
                    $insideImportBlock = $true
                    continue
                }
                if ($line -match '^\s*import\s+(?:(?:[._A-Za-z][A-Za-z0-9_]*)\s+)?["`](?<path>[^"`]+)["`]') {
                    $imports.Add($Matches.path)
                }
                continue
            }

            if ($line -match '^\s*\)') {
                $insideImportBlock = $false
                continue
            }
            if ($line -match '^\s*(?:(?:[._A-Za-z][A-Za-z0-9_]*)\s+)?["`](?<path>[^"`]+)["`]') {
                $imports.Add($Matches.path)
            }
        }
    }
    return @($imports)
}

$goCommand = Get-Command -Name $GoPath -ErrorAction Stop
$script:GoExecutable = $goCommand.Source
if (-not $script:GoExecutable) {
    $script:GoExecutable = $goCommand.Path
}

$repository = (Resolve-Path -LiteralPath $RepositoryRoot).Path
$manifest = Join-Path $repository "go.mod"
if (-not (Test-Path -LiteralPath $manifest -PathType Leaf)) {
    throw "Missing go.mod at $manifest"
}

$manifestText = [IO.File]::ReadAllText($manifest)
$forbiddenDirectives = [regex]::Matches(
    $manifestText,
    '(?m)^\s*(require|replace|exclude|retract)\b'
) | ForEach-Object { $_.Groups[1].Value } | Sort-Object -Unique
if ($forbiddenDirectives.Count -ne 0) {
    throw "Forbidden go.mod directive(s): $($forbiddenDirectives -join ', ')"
}

$forbiddenArtifacts = @("go.sum", "go.work", "go.work.sum")
foreach ($artifact in $forbiddenArtifacts) {
    $path = Join-Path $repository $artifact
    if (Test-Path -LiteralPath $path) {
        throw "Forbidden dependency/workspace artifact exists: $artifact"
    }
}
if (Test-Path -LiteralPath (Join-Path $repository "vendor")) {
    throw "Forbidden dependency directory exists: vendor"
}

$previousGoWork = $env:GOWORK
$previousToolchain = $env:GOTOOLCHAIN
$previousGoCache = $env:GOCACHE
$previousGoModCache = $env:GOMODCACHE
$temporaryParent = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar)
$temporaryRoot = Join-Path $temporaryParent ("anvil-zero-dep-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $temporaryRoot | Out-Null
try {
    $env:GOWORK = "off"
    $env:GOTOOLCHAIN = "local"
    $env:GOCACHE = Join-Path $temporaryRoot "build-cache"
    $env:GOMODCACHE = Join-Path $temporaryRoot "module-cache"
    Push-Location $repository
    try {
        $modulePath = @(Invoke-GoChecked -Arguments @("list", "-m", "-f", "{{.Path}}"))[0].Trim()
        if ([string]::IsNullOrWhiteSpace($modulePath)) {
            throw "The main module path is empty"
        }

        $modules = @(Invoke-GoChecked -Arguments @("list", "-m", "-f", "{{.Path}}", "all")) |
            ForEach-Object { $_.Trim() } |
            Where-Object { $_ }
        $externalModules = @($modules | Where-Object { $_ -ne $modulePath })
        if ($externalModules.Count -ne 0) {
            throw "External module(s) detected: $($externalModules -join ', ')"
        }

        $standardPackages = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
        foreach ($package in (Invoke-GoChecked -Arguments @("list", "std"))) {
            if ($package) {
                [void]$standardPackages.Add($package.Trim())
            }
        }

        $productionFiles = @(Get-ChildItem -LiteralPath $repository -Recurse -File -Filter "*.go" |
            Where-Object {
                $_.Name -notlike "*_test.go" -and
                $_.FullName -notlike "$repository\.git\*" -and
                $_.FullName -notlike "$repository\vendor\*"
            })
        $imports = @(Get-ProductionImports -Files $productionFiles | Sort-Object -Unique)
        $forbiddenHTTP = @($imports | Where-Object { $_ -eq "net/http" -or $_ -like "net/http/*" })
        if ($forbiddenHTTP.Count -ne 0) {
            throw "Production net/http import(s) detected: $($forbiddenHTTP -join ', ')"
        }

        $externalImports = @($imports | Where-Object {
            -not $standardPackages.Contains($_) -and
            $_ -ne $modulePath -and
            -not $_.StartsWith($modulePath + "/", [StringComparison]::Ordinal)
        })
        if ($externalImports.Count -ne 0) {
            throw "External production import(s) detected: $($externalImports -join ', ')"
        }

        # This also validates the selected build graph with module downloads disabled.
        $previousGoFlags = $env:GOFLAGS
        try {
            $env:GOFLAGS = "-mod=readonly"
            [void](Invoke-GoChecked -Arguments @("list", "-deps", "./..."))
        }
        finally {
            $env:GOFLAGS = $previousGoFlags
        }
    }
    finally {
        Pop-Location
    }
}
finally {
    $env:GOWORK = $previousGoWork
    $env:GOTOOLCHAIN = $previousToolchain
    $env:GOCACHE = $previousGoCache
    $env:GOMODCACHE = $previousGoModCache
    if (Test-Path -LiteralPath $temporaryRoot) {
        $resolved = (Resolve-Path -LiteralPath $temporaryRoot).Path
        $expectedPrefix = $temporaryParent + [IO.Path]::DirectorySeparatorChar + "anvil-zero-dep-"
        if (-not $resolved.StartsWith($expectedPrefix, [StringComparison]::OrdinalIgnoreCase)) {
            throw "Refusing to clean unexpected path: $resolved"
        }
        Remove-Item -LiteralPath $resolved -Recurse -Force
    }
}

"module=$modulePath"
"module_count=$($modules.Count)"
"production_go_files=$($productionFiles.Count)"
"production_imports=$($imports.Count)"
"production_net_http=false"
"zero_dependency=true"
