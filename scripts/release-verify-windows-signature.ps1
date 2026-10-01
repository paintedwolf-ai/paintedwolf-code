param(
    [Parameter(Mandatory = $true)]
    [string]$Path,
    [Parameter(Mandatory = $true)]
    [string]$UpdaterArchive,
    [Parameter(Mandatory = $true)]
    [string]$ApplicationPath,
    [Parameter(Mandatory = $true)]
    [string]$SidecarPath,
    [Parameter(Mandatory = $true)]
    [string]$LogViewerPath,
    [Parameter(Mandatory = $true)]
    [string]$GitPath,
    [Parameter(Mandatory = $true)]
    [string]$GitLFSPath,
    [Parameter(Mandatory = $true)]
    [string]$OpenGrepPath,
    [Parameter(Mandatory = $true)]
    [string]$BrowserPath,
    [Parameter(Mandatory = $true)]
    [string]$EngineRootPath,
    [Parameter(Mandatory = $true)]
    [string]$ExpectedVersion
)

$ErrorActionPreference = "Stop"
function Assert-CodeSignature([string]$Target) {
    $signature = Get-AuthenticodeSignature -LiteralPath $Target
    if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid) {
        throw "Authenticode signature is not valid for ${Target}: $($signature.StatusMessage)"
    }
    if ($null -eq $signature.SignerCertificate) {
        throw "Authenticode signature has no signer certificate for ${Target}"
    }
    if ($null -eq $signature.TimeStamperCertificate) {
        throw "Authenticode signature has no trusted timestamp for ${Target}"
    }
}

function Assert-ProductVersion([string]$Target, [Version]$Expected) {
    $actual = [Version](Get-Item -LiteralPath $Target).VersionInfo.ProductVersion
    if ($actual.Major -ne $Expected.Major -or
        $actual.Minor -ne $Expected.Minor -or
        $actual.Build -ne $Expected.Build) {
        throw "Windows product version is ${actual} for ${Target}; expected ${Expected}"
    }
}

$resolved = (Resolve-Path -LiteralPath $Path).Path
$resolvedArchive = (Resolve-Path -LiteralPath $UpdaterArchive).Path
$resolvedApplication = (Resolve-Path -LiteralPath $ApplicationPath).Path
$resolvedSidecar = (Resolve-Path -LiteralPath $SidecarPath).Path
$resolvedLogViewer = (Resolve-Path -LiteralPath $LogViewerPath).Path
$resolvedGit = (Resolve-Path -LiteralPath $GitPath).Path
$resolvedGitLFS = (Resolve-Path -LiteralPath $GitLFSPath).Path
$resolvedOpenGrep = (Resolve-Path -LiteralPath $OpenGrepPath).Path
$resolvedBrowser = (Resolve-Path -LiteralPath $BrowserPath).Path
$resolvedEngineRoot = (Resolve-Path -LiteralPath $EngineRootPath).Path
Assert-CodeSignature $resolved
Assert-CodeSignature $resolvedApplication
Assert-CodeSignature $resolvedSidecar
Assert-CodeSignature $resolvedLogViewer
Assert-CodeSignature $resolvedGit
Assert-CodeSignature $resolvedGitLFS
Assert-CodeSignature $resolvedOpenGrep
Assert-CodeSignature $resolvedBrowser
$engineExecutables = @(Get-ChildItem -LiteralPath $resolvedEngineRoot -Filter *.exe -File -Recurse)
if ($engineExecutables.Count -eq 0) {
    throw "Windows engine root contains no executable payloads"
}
foreach ($executable in $engineExecutables) {
    Assert-CodeSignature $executable.FullName
}
$expected = [Version]$ExpectedVersion
Assert-ProductVersion $resolved $expected
Assert-ProductVersion $resolvedApplication $expected
$extract = Join-Path $env:RUNNER_TEMP "windows-updater-signature-$([Guid]::NewGuid().ToString('N'))"
try {
    Expand-Archive -LiteralPath $resolvedArchive -DestinationPath $extract
    $installers = @(Get-ChildItem -LiteralPath $extract -Filter *.exe -File -Recurse)
    if ($installers.Count -ne 1) {
        throw "Windows updater archive must contain exactly one installer; found $($installers.Count)"
    }
    Assert-CodeSignature $installers[0].FullName
    $packageHash = (Get-FileHash -LiteralPath $resolved -Algorithm SHA256).Hash
    $archiveHash = (Get-FileHash -LiteralPath $installers[0].FullName -Algorithm SHA256).Hash
    if ($packageHash -ne $archiveHash) {
        throw "Windows installer and updater archive contain different signed executables"
    }
} finally {
    Remove-Item -LiteralPath $extract -Recurse -Force -ErrorAction SilentlyContinue
}
