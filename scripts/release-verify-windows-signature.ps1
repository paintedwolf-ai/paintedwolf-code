param(
    [Parameter(Mandatory = $true)]
    [string]$Path,
    [Parameter(Mandatory = $true)]
    [string]$UpdaterPath,
    [Parameter(Mandatory = $true)]
    [string]$ApplicationPath,
    [Parameter(Mandatory = $true)]
    [string]$SidecarPath,
    [Parameter(Mandatory = $true)]
    [string]$LogViewerPath,
    [Parameter(Mandatory = $true)]
    [string]$DocumentCorePath,
    [Parameter(Mandatory = $true)]
    [string]$DecisionEnginePath,
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
$resolvedUpdater = (Resolve-Path -LiteralPath $UpdaterPath).Path
$resolvedApplication = (Resolve-Path -LiteralPath $ApplicationPath).Path
$resolvedSidecar = (Resolve-Path -LiteralPath $SidecarPath).Path
$resolvedLogViewer = (Resolve-Path -LiteralPath $LogViewerPath).Path
$resolvedDocumentCore = (Resolve-Path -LiteralPath $DocumentCorePath).Path
$resolvedDecisionEngine = (Resolve-Path -LiteralPath $DecisionEnginePath).Path
$resolvedGit = (Resolve-Path -LiteralPath $GitPath).Path
$resolvedGitLFS = (Resolve-Path -LiteralPath $GitLFSPath).Path
$resolvedOpenGrep = (Resolve-Path -LiteralPath $OpenGrepPath).Path
$resolvedBrowser = (Resolve-Path -LiteralPath $BrowserPath).Path
$resolvedEngineRoot = (Resolve-Path -LiteralPath $EngineRootPath).Path
Assert-CodeSignature $resolved
Assert-CodeSignature $resolvedApplication
Assert-CodeSignature $resolvedSidecar
Assert-CodeSignature $resolvedLogViewer
Assert-CodeSignature $resolvedDocumentCore
Assert-CodeSignature $resolvedDecisionEngine
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
Assert-CodeSignature $resolvedUpdater
Assert-ProductVersion $resolvedUpdater $expected
$packageHash = (Get-FileHash -LiteralPath $resolved -Algorithm SHA256).Hash
$updaterHash = (Get-FileHash -LiteralPath $resolvedUpdater -Algorithm SHA256).Hash
if ($packageHash -ne $updaterHash) {
    throw "Windows installer and updater contain different signed executables"
}
