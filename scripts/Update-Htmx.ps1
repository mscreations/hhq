<#
.SYNOPSIS
    Checks/updates/diffs the vendored web/static/js/htmx.min.js against the
    latest htmx.org release on GitHub.

.DESCRIPTION
    Deliberately refuses to *adopt* a release until it has aged past
    -MinAgeDays (default 90) - a compromised release has time to be caught
    and yanked upstream before we'd ever vendor it. Diff mode ignores that
    gate since it doesn't change anything - it's just a preview.

.PARAMETER Mode
    Check (default) - report current vs. latest, don't change anything.
    Update          - actually vendor the new file + version if eligible.
    Diff            - prettify current vs. latest and show a unified diff.

.PARAMETER MinAgeDays
    Quarantine window in days. Releases newer than this are left alone in
    Check/Update mode.

.EXAMPLE
    ./scripts/Update-Htmx.ps1
    ./scripts/Update-Htmx.ps1 -Mode Update
    ./scripts/Update-Htmx.ps1 -Mode Diff
#>
[CmdletBinding()]
param(
    [ValidateSet('Check', 'Update', 'Diff')]
    [string]$Mode = 'Check',

    [int]$MinAgeDays = 90
)

$ErrorActionPreference = 'Stop'

$repoRoot = (git rev-parse --show-toplevel 2>$null)
if (-not $repoRoot) { $repoRoot = (Get-Location).Path }
$versionFile = Join-Path $repoRoot 'web/static/js/htmx.version'
$jsFile = Join-Path $repoRoot 'web/static/js/htmx.min.js'
$prettifyScript = Join-Path $repoRoot 'scripts/prettify_js.py'

$currentVersion = if (Test-Path $versionFile) { (Get-Content $versionFile -Raw).Trim() } else { 'unknown' }

# htmx does not set GitHub's `prerelease` flag on its own alpha/beta/rc tags
# (e.g. v4.0.0-beta5 comes back as prerelease=false), so /releases/latest
# can't be trusted to skip them. Instead, walk the release list ourselves
# and take the newest tag that looks like a plain "vX.Y.Z" release.
$releases = Invoke-RestMethod -Uri 'https://api.github.com/repos/bigskysoftware/htmx/releases?per_page=30' `
    -Headers @{ 'User-Agent' = 'hhq-update-htmx' }
$release = $releases | Where-Object { $_.tag_name -match '^v\d+\.\d+\.\d+$' } | Select-Object -First 1

if (-not $release) {
    Write-Error "No plain release tag (vX.Y.Z) found in the last 30 releases"
    exit 1
}

$latestVersion = $release.tag_name.TrimStart('v')
$publishedAt = [DateTime]$release.published_at
$ageDays = [int]((Get-Date).ToUniversalTime() - $publishedAt.ToUniversalTime()).TotalDays

Write-Host "Current vendored version: $currentVersion"
Write-Host "Latest upstream release:  $latestVersion (published $ageDays days ago)"

if ($latestVersion -eq $currentVersion) {
    Write-Host "Already up to date."
    exit 0
}

$downloadUrl = "https://unpkg.com/htmx.org@$latestVersion/dist/htmx.min.js"

if ($Mode -eq 'Diff') {
    if (-not (Get-Command python -ErrorAction SilentlyContinue)) {
        Write-Error "python is required for -Mode Diff"
        exit 1
    }

    if ($ageDays -lt $MinAgeDays) {
        Write-Host "(note: still within the $MinAgeDays-day quarantine window - preview only)"
    }

    Write-Host "Downloading $downloadUrl ..."
    $workDir = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $workDir | Out-Null
    try {
        $newMin = Join-Path $workDir "htmx-$latestVersion.min.js"
        Invoke-WebRequest -Uri $downloadUrl -OutFile $newMin

        $currentPretty = Join-Path $workDir "current-$currentVersion.js"
        $latestPretty = Join-Path $workDir "latest-$latestVersion.js"
        python $prettifyScript $jsFile | Set-Content -Path $currentPretty -Encoding utf8
        python $prettifyScript $newMin | Set-Content -Path $latestPretty -Encoding utf8

        & git diff --no-index -- $currentPretty $latestPretty
    }
    finally {
        Remove-Item -Recurse -Force $workDir -Confirm:$false -ErrorAction SilentlyContinue
    }
    exit 0
}

if ($ageDays -lt $MinAgeDays) {
    Write-Host "Latest release is only $ageDays day(s) old (< $MinAgeDays-day quarantine window) - not upgrading yet."
    exit 0
}

if ($Mode -ne 'Update') {
    Write-Host "A newer, aged release is available: $latestVersion. Run with -Mode Update to vendor it (or -Mode Diff to preview it)."
    exit 0
}

Write-Host "Downloading $downloadUrl ..."
$tmpFile = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
Invoke-WebRequest -Uri $downloadUrl -OutFile $tmpFile

if ((Get-Item $tmpFile).Length -eq 0) {
    Write-Error "Download failed or produced an empty file"
    Remove-Item -Force $tmpFile -ErrorAction SilentlyContinue
    exit 1
}

Move-Item -Force $tmpFile $jsFile
Set-Content -Path $versionFile -Value $latestVersion -NoNewline
Add-Content -Path $versionFile -Value ''
Write-Host "Updated htmx.min.js to $latestVersion - review the diff before committing."
