[CmdletBinding()]
param(
    [string]$Msys2Root = 'C:\msys64',
    [string]$ToolchainRoot = '',
    [int]$Jobs = [Environment]::ProcessorCount,
    [switch]$InstallPinnedToolchain,
    [switch]$VerifyToolchainOnly,
    [switch]$SelfTest
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$Version = '9.0'
$ArchiveSha256 = '7f607a00dd0d28a729d5a4811205812eef01cf6ef6155025febb6f36a9062d52'
$SigningFingerprint = 'FCF986EA15E6E293A5644F10B4322F04D67658D8'
$SignedTag = 'n9.0'
$SignedTagCommit = 'd32b387f2b0a484599d4587d651891f0c63c4238'
$SignedTagFingerprint = 'DD1EC9E8DE085C629B3E1846B18E8928B3948D64'
$SourceDateEpoch = '1785797913'
$ReleaseBase = 'https://ffmpeg.org/releases'
$ArchiveName = "ffmpeg-$Version.tar.xz"
$ScriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepositoryRoot = [IO.Path]::GetFullPath((Join-Path $ScriptRoot '..'))
$DistRoot = Join-Path $RepositoryRoot 'dist'
$DownloadRoot = Join-Path $DistRoot 'ffmpeg-source'
$SourceRoot = Join-Path $DownloadRoot "ffmpeg-$Version"
$OutputRoot = Join-Path $DistRoot 'ffmpeg'
$FlagsPath = Join-Path $RepositoryRoot 'third_party\ffmpeg\configure.flags'
$ToolchainLockPath = Join-Path $RepositoryRoot 'third_party\ffmpeg\toolchain-lock.json'
$ToolchainCache = Join-Path $DistRoot 'msys2-toolchain'
$ToolchainRoot = if ($ToolchainRoot) { [IO.Path]::GetFullPath($ToolchainRoot) } else { Join-Path $DistRoot 'msys2-toolchain-root' }
$ArchivePath = Join-Path $DownloadRoot $ArchiveName
$SignaturePath = "$ArchivePath.asc"
$PublicKeyPath = Join-Path $DownloadRoot 'ffmpeg-devel.asc'
$GpgHome = Join-Path $DownloadRoot 'gnupg'
$HostBash = Join-Path $Msys2Root 'usr\bin\bash.exe'
$Bash = $HostBash
$GpgCommand = Get-Command gpg.exe -ErrorAction SilentlyContinue
$Gpg = if ($null -ne $GpgCommand) { $GpgCommand.Source } else { Join-Path $Msys2Root 'usr\bin\gpg.exe' }

if (-not (Test-Path -LiteralPath $HostBash)) {
    throw "MSYS2 was not found at $Msys2Root. Install MSYS2 with the UCRT64 toolchain or pass -Msys2Root."
}
if (-not (Test-Path -LiteralPath $Gpg)) { throw 'GPG is required to verify the FFmpeg release signature.' }
if ($Jobs -lt 1) { throw '-Jobs must be at least 1.' }

function Get-Sha256 {
    param([string]$Path)
    $Stream = [IO.File]::OpenRead($Path)
    $Algorithm = [Security.Cryptography.SHA256]::Create()
    try {
        return ([BitConverter]::ToString($Algorithm.ComputeHash($Stream))).Replace('-', '').ToLowerInvariant()
    } finally {
        $Algorithm.Dispose()
        $Stream.Dispose()
    }
}

function Get-ToolchainLockCanonicalSha256 {
    param([object]$Lock)
    $Lines = [Collections.Generic.List[string]]::new()
    $Lines.Add("schema=$($Lock.schema)")
    $Lines.Add("source=$($Lock.source)")
    foreach ($Package in @($Lock.packages)) {
        $Lines.Add("package`t$($Package.name)`t$($Package.version)`t$($Package.url)`t$($Package.sha256)`t$($Package.signature_url)`t$($Package.signature_sha256)")
    }
    $Lines.Add("gcc=$($Lock.executables.gcc)")
    $Lines.Add("ld=$($Lock.executables.ld)")
    $Lines.Add("make=$($Lock.executables.make)")
    $Bytes = [Text.Encoding]::UTF8.GetBytes(($Lines -join "`n") + "`n")
    $Algorithm = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($Algorithm.ComputeHash($Bytes))).Replace('-', '').ToLowerInvariant() } finally { $Algorithm.Dispose() }
}

function Get-PEAuthenticodeContentSha256 {
    param([string]$Path)
    $Bytes = [IO.File]::ReadAllBytes($Path)
    if ($Bytes.Length -lt 256 -or [BitConverter]::ToUInt16($Bytes, 0) -ne 0x5a4d) { throw 'Built FFmpeg is not a valid PE image.' }
    $PE = [BitConverter]::ToUInt32($Bytes, 0x3c)
    if ($PE + 24 -gt $Bytes.Length -or [BitConverter]::ToUInt32($Bytes, $PE) -ne 0x00004550) { throw 'Built FFmpeg PE header is invalid.' }
    $Optional = $PE + 24
    $Magic = [BitConverter]::ToUInt16($Bytes, $Optional)
    $DataDirectory = $Optional + $(if ($Magic -eq 0x20b) { 112 } elseif ($Magic -eq 0x10b) { 96 } else { throw 'Built FFmpeg PE optional header is invalid.' })
    $Checksum = $Optional + 64
    $SecurityDirectory = $DataDirectory + 32
    $CertificateOffset = [BitConverter]::ToUInt32($Bytes, $SecurityDirectory)
    $CertificateSize = [BitConverter]::ToUInt32($Bytes, $SecurityDirectory + 4)
    if (($CertificateOffset -eq 0) -ne ($CertificateSize -eq 0) -or $CertificateOffset -gt $Bytes.Length -or $CertificateSize -gt $Bytes.Length - $CertificateOffset -or ($CertificateSize -gt 0 -and $CertificateOffset + $CertificateSize -ne $Bytes.Length)) { throw 'Built FFmpeg PE certificate table is invalid.' }
    $End = if ($CertificateSize -gt 0) { $CertificateOffset } else { $Bytes.Length }
    $Normalized = New-Object byte[] $End
    [Array]::Copy($Bytes, $Normalized, $End)
    [Array]::Clear($Normalized, $Checksum, 4)
    [Array]::Clear($Normalized, $SecurityDirectory, 8)
    $Algorithm = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($Algorithm.ComputeHash($Normalized))).Replace('-', '').ToLowerInvariant() } finally { $Algorithm.Dispose() }
}

New-Item -ItemType Directory -Force -Path $DownloadRoot, $OutputRoot, $GpgHome, $ToolchainCache | Out-Null
function Invoke-PinnedDownload {
    param([string[]]$Uris, [string]$Destination)
    $Partial = "$Destination.download"
    $CurlCommand = Get-Command curl.exe -ErrorAction SilentlyContinue
    $Downloaded = $false
    foreach ($Uri in $Uris) {
        Remove-Item -LiteralPath $Partial -Force -ErrorAction SilentlyContinue
        if ($null -ne $CurlCommand) {
            & $CurlCommand.Source --fail --location --http1.1 --connect-timeout 15 --max-time 120 --speed-limit 1024 --speed-time 30 --retry 1 --retry-delay 2 --retry-all-errors --output $Partial $Uri
            $Downloaded = $LASTEXITCODE -eq 0
        } else {
            try {
                Invoke-WebRequest -UseBasicParsing -Uri $Uri -OutFile $Partial
                $Downloaded = $true
            } catch {
                $Downloaded = $false
            }
        }
        if ($Downloaded) { break }
        Write-Warning "Download endpoint failed, trying the next pinned mirror: $Uri"
    }
    if (-not $Downloaded) { throw "Download failed from all pinned endpoints: $($Uris -join ', ')" }
    if (-not (Test-Path -LiteralPath $Partial) -or (Get-Item -LiteralPath $Partial).Length -eq 0) {
        throw "Download was empty: $($Uris -join ', ')"
    }
    Move-Item -LiteralPath $Partial -Destination $Destination -Force
}

function ConvertTo-MsysPath {
    param([string]$Path, [string]$BashPath = $Bash)
    $env:GIFT_PANEL_PATH_TO_CONVERT = $Path
    try {
        $Converted = & $BashPath --noprofile --norc -c 'export PATH=/usr/bin; cygpath -u "$GIFT_PANEL_PATH_TO_CONVERT"'
        if ($LASTEXITCODE -ne 0 -or -not $Converted) { throw "Could not convert path for MSYS2: $Path" }
        return $Converted.Trim()
    } finally {
        Remove-Item Env:GIFT_PANEL_PATH_TO_CONVERT -ErrorAction SilentlyContinue
    }
}

function Assert-ExactProperties {
    param([object]$Value, [string[]]$Names, [string]$Label)
    $Actual = @($Value.PSObject.Properties.Name | Sort-Object)
    $Expected = @($Names | Sort-Object)
    if (($Actual -join ',') -ne ($Expected -join ',')) { throw "$Label has an unexpected schema." }
}

if (-not (Test-Path -LiteralPath $ToolchainLockPath)) { throw 'The committed FFmpeg toolchain lock is missing.' }
try { $ToolchainLock = Get-Content -LiteralPath $ToolchainLockPath -Raw | ConvertFrom-Json -ErrorAction Stop } catch { throw 'The FFmpeg toolchain lock is not valid JSON.' }
Assert-ExactProperties $ToolchainLock @('schema', 'source', 'packages', 'executables') 'Toolchain lock'
if ($ToolchainLock.schema -ne 1 -or $ToolchainLock.source -ne 'https://repo.msys2.org' -or @($ToolchainLock.packages).Count -eq 0) { throw 'The FFmpeg toolchain lock source/schema is invalid.' }
Assert-ExactProperties $ToolchainLock.executables @('gcc', 'ld', 'make') 'Toolchain executable lock'
$SeenPackages = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
foreach ($Package in @($ToolchainLock.packages)) {
    Assert-ExactProperties $Package @('name', 'version', 'url', 'sha256', 'signature_url', 'signature_sha256') 'Toolchain package lock'
    if ($Package.name -notmatch '^[a-z0-9][a-z0-9+._-]*$' -or $Package.version -notmatch '^[0-9A-Za-z][0-9A-Za-z.+_~-]*$' -or -not $SeenPackages.Add([string]$Package.name)) { throw 'Toolchain package identity is invalid or duplicated.' }
    if ($Package.url -notmatch '^https://repo\.msys2\.org/(?:msys/x86_64|mingw/ucrt64)/[A-Za-z0-9+._~-]+\.pkg\.tar\.zst$' -or $Package.signature_url -ne "$($Package.url).sig") { throw 'Toolchain package URL is invalid.' }
    if ($Package.sha256 -notmatch '^[0-9a-f]{64}$' -or $Package.signature_sha256 -notmatch '^[0-9a-f]{64}$') { throw 'Toolchain package hash is invalid.' }
}
$RequiredToolchainPackages = @('bash','coreutils','diffutils','gawk','gcc-libs','gmp','grep','libiconv','libintl','libpcre','libreadline','make','mingw-w64-ucrt-x86_64-binutils','mingw-w64-ucrt-x86_64-crt','mingw-w64-ucrt-x86_64-gcc','mingw-w64-ucrt-x86_64-gcc-libs','mingw-w64-ucrt-x86_64-gettext-runtime','mingw-w64-ucrt-x86_64-gmp','mingw-w64-ucrt-x86_64-headers','mingw-w64-ucrt-x86_64-isl','mingw-w64-ucrt-x86_64-libiconv','mingw-w64-ucrt-x86_64-libwinpthread','mingw-w64-ucrt-x86_64-mpc','mingw-w64-ucrt-x86_64-mpfr','mingw-w64-ucrt-x86_64-tzdata','mingw-w64-ucrt-x86_64-windows-default-manifest','mingw-w64-ucrt-x86_64-winpthreads','mingw-w64-ucrt-x86_64-zlib','mingw-w64-ucrt-x86_64-zstd','mpfr','msys2-runtime','nasm','ncurses','pkgconf','sed')
if ((@($SeenPackages | Sort-Object) -join ',') -ne (@($RequiredToolchainPackages | Sort-Object) -join ',')) { throw 'Toolchain package closure differs from the approved exact set.' }
$ExpectedToolchainPath = Join-Path $ToolchainCache 'expected-packages.txt'
$ExpectedToolchain = @($ToolchainLock.packages | ForEach-Object { "$($_.name)=$($_.version)" } | Sort-Object)
[IO.File]::WriteAllText($ExpectedToolchainPath, ($ExpectedToolchain -join "`n") + "`n", (New-Object Text.UTF8Encoding($false)))
function Assert-ExactToolchainPackageSet {
    param([string[]]$Expected, [string[]]$Actual)
    if ((($Expected | Sort-Object) -join "`n") -ne (($Actual | Sort-Object) -join "`n")) { throw 'Installed isolated MSYS2 package database differs from the committed lock.' }
}
if ($SelfTest) {
    $Extra = @($ExpectedToolchain) + 'unexpected-package=1.0-1'
    $Rejected = $false
    try { Assert-ExactToolchainPackageSet $ExpectedToolchain $Extra } catch { $Rejected = $true }
    if (-not $Rejected) { throw 'Extra-package adversary was not detected.' }
    $CanonicalLF = Get-ToolchainLockCanonicalSha256 $ToolchainLock
    $CanonicalCRLF = Get-ToolchainLockCanonicalSha256 ((Get-Content -LiteralPath $ToolchainLockPath -Raw).Replace("`n", "`r`n") | ConvertFrom-Json)
    if ($CanonicalLF -ne $CanonicalCRLF) { throw 'Toolchain lock canonical hash depends on line endings.' }
    Write-Host 'FFmpeg build toolchain self-tests passed'
    exit 0
}

if ($InstallPinnedToolchain) {
    $PinnedPackagePaths = @()
    foreach ($Package in @($ToolchainLock.packages)) {
        $PackageName = [IO.Path]::GetFileName(([Uri]$Package.url).AbsolutePath)
        $PackagePath = Join-Path $ToolchainCache $PackageName
        $SignatureFile = "$PackagePath.sig"
        if (-not (Test-Path -LiteralPath $PackagePath) -or (Get-Sha256 $PackagePath) -ne $Package.sha256) { Invoke-PinnedDownload -Uris @([string]$Package.url) -Destination $PackagePath }
        if (-not (Test-Path -LiteralPath $SignatureFile) -or (Get-Sha256 $SignatureFile) -ne $Package.signature_sha256) { Invoke-PinnedDownload -Uris @([string]$Package.signature_url) -Destination $SignatureFile }
        if ((Get-Sha256 $PackagePath) -ne $Package.sha256 -or (Get-Sha256 $SignatureFile) -ne $Package.signature_sha256) { throw "Pinned MSYS2 package hash mismatch: $PackageName" }
        $env:FFMPEG_PINNED_PACKAGE = ConvertTo-MsysPath $PackagePath
        $env:FFMPEG_PINNED_SIGNATURE = ConvertTo-MsysPath $SignatureFile
        try {
            $SignatureStatus = @(& $Bash -lc 'gpg --batch --homedir /etc/pacman.d/gnupg --status-fd 1 --verify "$FFMPEG_PINNED_SIGNATURE" "$FFMPEG_PINNED_PACKAGE" 2>/dev/null')
            if ($LASTEXITCODE -ne 0 -or -not ($SignatureStatus -match '^\[GNUPG:\] VALIDSIG ') -or -not ($SignatureStatus -match '^\[GNUPG:\] TRUST_(?:FULLY|ULTIMATE) ')) { throw "Pinned MSYS2 package signature failed: $PackageName" }
        } finally { Remove-Item Env:FFMPEG_PINNED_PACKAGE, Env:FFMPEG_PINNED_SIGNATURE -ErrorAction SilentlyContinue }
        $PinnedPackagePaths += ConvertTo-MsysPath $PackagePath
    }
    $ResolvedToolchainRoot = [IO.Path]::GetFullPath($ToolchainRoot)
    $ResolvedDistRoot = [IO.Path]::GetFullPath($DistRoot) + [IO.Path]::DirectorySeparatorChar
    if (-not $ResolvedToolchainRoot.StartsWith($ResolvedDistRoot, [StringComparison]::OrdinalIgnoreCase)) { throw 'The isolated toolchain root must stay beneath dist.' }
    if (Test-Path -LiteralPath $ResolvedToolchainRoot) { Remove-Item -LiteralPath $ResolvedToolchainRoot -Recurse -Force }
    New-Item -ItemType Directory -Force -Path $ResolvedToolchainRoot | Out-Null
    $env:FFMPEG_PINNED_PACKAGES = $PinnedPackagePaths -join "`n"
    $env:FFMPEG_PINNED_ROOT = ConvertTo-MsysPath $ResolvedToolchainRoot
    $InstallScript = Join-Path $ToolchainCache 'install-toolchain.sh'
    [IO.File]::WriteAllText($InstallScript, "set -euo pipefail`nexport PATH=/usr/bin`nmapfile -t packages <<< `"`$FFMPEG_PINNED_PACKAGES`"`nmkdir -p `"`$FFMPEG_PINNED_ROOT/tmp`" `"`$FFMPEG_PINNED_ROOT/var/lib/pacman/local`" `"`$FFMPEG_PINNED_ROOT/var/cache/pacman/pkg`" `"`$FFMPEG_PINNED_ROOT/var/log`"`npacman --root `"`$FFMPEG_PINNED_ROOT`" --dbpath `"`$FFMPEG_PINNED_ROOT/var/lib/pacman`" --cachedir `"`$FFMPEG_PINNED_ROOT/var/cache/pacman/pkg`" --logfile `"`$FFMPEG_PINNED_ROOT/var/log/pacman.log`" -U --noconfirm --nodeps --noscriptlet `"`${packages[@]}`"`n", (New-Object Text.UTF8Encoding($false)))
    try { & $HostBash (ConvertTo-MsysPath $InstallScript); if ($LASTEXITCODE -ne 0) { throw 'Pinned isolated MSYS2 toolchain installation failed.' } } finally { Remove-Item Env:FFMPEG_PINNED_PACKAGES, Env:FFMPEG_PINNED_ROOT -ErrorAction SilentlyContinue; Remove-Item -LiteralPath $InstallScript -Force -ErrorAction SilentlyContinue }
}

$Bash = Join-Path $ToolchainRoot 'usr\bin\bash.exe'
if (-not (Test-Path -LiteralPath $Bash)) { throw "The isolated pinned toolchain is missing at $ToolchainRoot. Run with -InstallPinnedToolchain." }

$env:FFMPEG_TOOLCHAIN_EXPECTED_FILE = ConvertTo-MsysPath $ExpectedToolchainPath $HostBash
$env:FFMPEG_PINNED_ROOT = ConvertTo-MsysPath $ToolchainRoot $HostBash
$env:FFMPEG_EXPECTED_GCC_VERSION = [string]$ToolchainLock.executables.gcc
$env:FFMPEG_EXPECTED_LD_VERSION = [string]$ToolchainLock.executables.ld
$env:FFMPEG_EXPECTED_MAKE_VERSION = [string]$ToolchainLock.executables.make
$ToolchainVerifyScript = Join-Path $ToolchainCache 'verify-toolchain.sh'
$ToolchainExecutableVerifyCommand = @'
set -euo pipefail
export PATH="/ucrt64/bin:/usr/bin"
test "$(gcc --version | head -n1)" = "$FFMPEG_EXPECTED_GCC_VERSION"
test "$(ld --version | head -n1)" = "$FFMPEG_EXPECTED_LD_VERSION"
test "$(make --version | head -n1)" = "$FFMPEG_EXPECTED_MAKE_VERSION"
'@
[IO.File]::WriteAllText($ToolchainVerifyScript, $ToolchainExecutableVerifyCommand, (New-Object Text.UTF8Encoding($false)))
try {
    $ActualToolchain = @(& $HostBash --noprofile --norc -c 'export PATH=/usr/bin; pacman --root "$FFMPEG_PINNED_ROOT" --dbpath "$FFMPEG_PINNED_ROOT/var/lib/pacman" -Q 2>/dev/null' | ForEach-Object { $_ -replace ' ', '=' })
    if ($LASTEXITCODE -ne 0) { throw 'Could not query the isolated MSYS2 package database.' }
    Assert-ExactToolchainPackageSet $ExpectedToolchain $ActualToolchain
    & $Bash --noprofile --norc (ConvertTo-MsysPath $ToolchainVerifyScript)
    if ($LASTEXITCODE -ne 0) { throw 'Installed isolated MSYS2 executables differ from the committed lock.' }
} finally {
    Remove-Item Env:FFMPEG_TOOLCHAIN_EXPECTED_FILE, Env:FFMPEG_PINNED_ROOT, Env:FFMPEG_EXPECTED_GCC_VERSION, Env:FFMPEG_EXPECTED_LD_VERSION, Env:FFMPEG_EXPECTED_MAKE_VERSION -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $ToolchainVerifyScript -Force -ErrorAction SilentlyContinue
}
if ($VerifyToolchainOnly) { Write-Host "Verified pinned MSYS2 toolchain from $ToolchainLockPath"; exit 0 }

if (-not (Test-Path -LiteralPath $ArchivePath) -or (Get-Sha256 -Path $ArchivePath) -ne $ArchiveSha256) {
    Invoke-PinnedDownload -Uris @(
        "$ReleaseBase/$ArchiveName",
        "https://download.videolan.org/pub/contrib/ffmpeg/$ArchiveName"
    ) -Destination $ArchivePath
} else {
    Write-Host "Reusing hash-verified source archive $ArchivePath"
}
if (-not (Test-Path -LiteralPath $SignaturePath) -or (Get-Item -LiteralPath $SignaturePath).Length -eq 0) {
    Invoke-PinnedDownload -Uris @("$ReleaseBase/$ArchiveName.asc") -Destination $SignaturePath
}
if (-not (Test-Path -LiteralPath $PublicKeyPath) -or (Get-Item -LiteralPath $PublicKeyPath).Length -eq 0) {
    Invoke-PinnedDownload -Uris @('https://ffmpeg.org/ffmpeg-devel.asc') -Destination $PublicKeyPath
}

$ActualHash = Get-Sha256 -Path $ArchivePath
if ($ActualHash -ne $ArchiveSha256) {
    throw "FFmpeg source SHA-256 mismatch: got $ActualHash, expected $ArchiveSha256."
}

$GpgHomeArgument = ConvertTo-MsysPath -Path $GpgHome -BashPath $HostBash
$PublicKeyArgument = ConvertTo-MsysPath -Path $PublicKeyPath -BashPath $HostBash
$SignatureArgument = ConvertTo-MsysPath -Path $SignaturePath -BashPath $HostBash
$ArchiveArgument = ConvertTo-MsysPath -Path $ArchivePath -BashPath $HostBash
& $Gpg --batch --homedir $GpgHomeArgument --import $PublicKeyArgument | Out-Host
if ($LASTEXITCODE -ne 0) { throw 'Failed to import the official FFmpeg signing key.' }
$Fingerprints = & $Gpg --batch --homedir $GpgHomeArgument --with-colons --fingerprint
if ($LASTEXITCODE -ne 0) { throw 'Failed to inspect the FFmpeg signing key.' }
$ImportedFingerprints = @($Fingerprints | Where-Object { $_ -like 'fpr:*' } | ForEach-Object { ($_ -split ':')[9] })
if ($SigningFingerprint -notin $ImportedFingerprints) {
    throw "Official FFmpeg signing fingerprint $SigningFingerprint was not imported."
}
$PreviousErrorActionPreference = $ErrorActionPreference
try {
    $ErrorActionPreference = 'Continue'
    $VerificationStatus = @(& $Gpg --batch --homedir $GpgHomeArgument --status-fd 1 --verify $SignatureArgument $ArchiveArgument 2>&1)
    $VerificationExitCode = $LASTEXITCODE
} finally {
    $ErrorActionPreference = $PreviousErrorActionPreference
}
$VerificationStatus | Out-Host
if ($VerificationExitCode -ne 0) { throw 'FFmpeg detached signature verification failed.' }
if (-not ($VerificationStatus -match "^\[GNUPG:\] VALIDSIG $SigningFingerprint(?: |$)")) {
    throw "FFmpeg detached signature was not made by pinned fingerprint $SigningFingerprint."
}

if (Test-Path -LiteralPath $SourceRoot) {
    $ResolvedDownloadRoot = [IO.Path]::GetFullPath($DownloadRoot).TrimEnd('\') + '\'
    $ResolvedSourceRoot = [IO.Path]::GetFullPath($SourceRoot)
    if (-not $ResolvedSourceRoot.StartsWith($ResolvedDownloadRoot, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to remove source directory outside $DownloadRoot."
    }
    Remove-Item -LiteralPath $ResolvedSourceRoot -Recurse -Force
}
tar.exe -xf $ArchivePath -C $DownloadRoot
if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath (Join-Path $SourceRoot 'configure'))) {
    throw 'FFmpeg source extraction failed.'
}

$env:FFMPEG_SOURCE_DIR = $SourceRoot
$env:FFMPEG_OUTPUT_DIR = $OutputRoot
$env:FFMPEG_FLAGS_FILE = $FlagsPath
$env:FFMPEG_BUILD_JOBS = [string]$Jobs
$env:FFMPEG_SOURCE_DATE_EPOCH = $SourceDateEpoch
$env:FFMPEG_SOURCE_SHA256 = $ArchiveSha256
$env:FFMPEG_VERSION = $Version
$env:FFMPEG_TOOLCHAIN_EXPECTED_FILE = $ExpectedToolchainPath
$env:FFMPEG_TOOLCHAIN_LOCK_SHA256 = Get-ToolchainLockCanonicalSha256 $ToolchainLock
$env:FFMPEG_EXPECTED_GCC_VERSION = [string]$ToolchainLock.executables.gcc
$env:FFMPEG_EXPECTED_LD_VERSION = [string]$ToolchainLock.executables.ld
$env:FFMPEG_EXPECTED_MAKE_VERSION = [string]$ToolchainLock.executables.make
$BuildCommand = @'
set -euo pipefail
export PATH="/ucrt64/bin:/usr/bin"
export SOURCE_DATE_EPOCH="$FFMPEG_SOURCE_DATE_EPOCH"
expected_toolchain=$(LC_ALL=C sort "$(cygpath -u "$FFMPEG_TOOLCHAIN_EXPECTED_FILE")")
actual_toolchain="$expected_toolchain"
gcc_version=$(gcc --version | head -n1)
ld_version=$(ld --version | head -n1)
make_version=$(make --version | head -n1)
test "$gcc_version" = "$FFMPEG_EXPECTED_GCC_VERSION"
test "$ld_version" = "$FFMPEG_EXPECTED_LD_VERSION"
test "$make_version" = "$FFMPEG_EXPECTED_MAKE_VERSION"
source_dir="$(cygpath -u "$FFMPEG_SOURCE_DIR")"
output_dir="$(cygpath -u "$FFMPEG_OUTPUT_DIR")"
flags_file="$(cygpath -u "$FFMPEG_FLAGS_FILE")"
mapfile -t configure_flags < <(sed -e 's/\r$//' -e '/^[[:space:]]*$/d' "$flags_file")
cd "$source_dir"
./configure "${configure_flags[@]}"
if grep -Eq '^#define CONFIG_(GPL|NONFREE) 1$' config.h; then
  echo 'GPL or nonfree configuration was enabled.' >&2
  exit 1
fi
if ! grep -Eq '^#define CONFIG_D3D11VA 1$' config.h; then
  echo 'The required D3D11VA infrastructure was disabled by configure.' >&2
  exit 1
fi
if ! grep -Eq '^#define CONFIG_MEDIAFOUNDATION 1$' config.h; then
  echo 'The required Media Foundation infrastructure was disabled by configure.' >&2
  exit 1
fi
if ! grep -Eq '^#define CONFIG_H264_MF_ENCODER 1$' config_components.h; then
  echo 'The required h264_mf encoder was disabled by configure.' >&2
  exit 1
fi
if ! grep -Eq '^#define CONFIG_IMAGE_WEBP_PIPE_DEMUXER 1$' config_components.h; then
  echo 'The required static WebP pipe demuxer was disabled by configure.' >&2
  exit 1
fi
if ! grep -Eq '^#define CONFIG_WEBP_ANIM_DEMUXER 1$' config_components.h ||
   ! grep -Eq '^#define CONFIG_WEBP_ANIM_DECODER 1$' config_components.h; then
  echo 'The required animated WebP demuxer or decoder was disabled by configure.' >&2
  exit 1
fi
if ! grep -Eq '^#define CONFIG_GIF_PARSER 1$' config_components.h; then
  echo 'The required GIF parser was disabled by configure.' >&2
  exit 1
fi
if ! grep -Eq '^#define CONFIG_H264_PARSER 1$' config_components.h; then
  echo 'The required H.264 parser was disabled by configure.' >&2
  exit 1
fi
if grep -Eq '^#define CONFIG_LOOP_FILTER 1$' config_components.h; then
  echo 'The cycle-caching loop filter must remain disabled.' >&2
  exit 1
fi
expected_components=$(cat <<'EOF'
AAC_ADTSTOASC_BSF
AC3_PARSER
AFORMAT_FILTER
ALPHAMERGE_FILTER
ANULL_FILTER
ATRIM_FILTER
CROP_FILTER
FILE_PROTOCOL
FORMAT_FILTER
FPS_FILTER
GIF_DECODER
GIF_DEMUXER
GIF_PARSER
H264_DECODER
H264_MF_ENCODER
H264_PARSER
HFLIP_FILTER
IMAGE2_DEMUXER
IMAGE_WEBP_PIPE_DEMUXER
MOV_DEMUXER
MOV_MUXER
MP4_MUXER
NULL_FILTER
OVERLAY_FILTER
PIPE_PROTOCOL
PNG_DECODER
ROTATE_FILTER
SCALE_FILTER
SETPTS_FILTER
SPLIT_FILTER
TRANSPOSE_FILTER
TRIM_FILTER
VFLIP_FILTER
VP8_DECODER
VP9_SUPERFRAME_BSF
WEBP_ANIM_DECODER
WEBP_ANIM_DEMUXER
WEBP_DECODER
EOF
)
actual_components=$(sed -nE 's/^#define CONFIG_([A-Z0-9_]+_(DECODER|ENCODER|PARSER|DEMUXER|MUXER|PROTOCOL|FILTER|BSF|HWACCEL|INDEV|OUTDEV)) 1$/\1/p' config_components.h | sort)
if ! diff -u <(printf '%s\n' "$expected_components") <(printf '%s\n' "$actual_components"); then
  echo 'Enabled FFmpeg component macros differ from the approved exact set.' >&2
  exit 1
fi
mkdir -p "$output_dir"
configure_sha=$(printf '%s\n' "${configure_flags[@]}" | sha256sum | cut -d' ' -f1)
component_gate="$output_dir/../ffmpeg-component-gate.txt"
rm -f "$component_gate" "$component_gate.partial"
make -j"$FFMPEG_BUILD_JOBS" ffmpeg.exe
cp -f ffmpeg.exe "$output_dir/ffmpeg.exe"
binary_sha=$(sha256sum "$output_dir/ffmpeg.exe" | cut -d' ' -f1)
binary_size=$(wc -c < "$output_dir/ffmpeg.exe" | tr -d '[:space:]')
{
  printf 'ffmpeg_version=%s\n' "$FFMPEG_VERSION"
  printf 'source_sha256=%s\n' "$FFMPEG_SOURCE_SHA256"
  printf 'source_date_epoch=%s\n' "$FFMPEG_SOURCE_DATE_EPOCH"
  printf 'configure_sha256=%s\n' "$configure_sha"
  printf 'binary_sha256=%s\n' "$binary_sha"
  printf 'binary_size=%s\n' "$binary_size"
  printf 'toolchain_lock_sha256=%s\n' "$FFMPEG_TOOLCHAIN_LOCK_SHA256"
  printf '[toolchain]\n%s\n' "$actual_toolchain"
  printf 'gcc_version=%s\n' "$gcc_version"
  printf 'ld_version=%s\n' "$ld_version"
  printf 'make_version=%s\n' "$make_version"
  printf '[components]\n%s\n' "$actual_components"
  printf '[infrastructure]\nD3D11VA\nMEDIAFOUNDATION\n'
} > "$component_gate.partial"
"$output_dir/ffmpeg.exe" -buildconf > "$output_dir/../ffmpeg-build-config.txt"
'@
$BuildScriptPath = Join-Path $DownloadRoot 'build-ffmpeg.sh'
[IO.File]::WriteAllText($BuildScriptPath, $BuildCommand, (New-Object Text.UTF8Encoding($false)))
$BuildScriptArgument = ConvertTo-MsysPath -Path $BuildScriptPath
try {
    & $Bash $BuildScriptArgument
    if ($LASTEXITCODE -ne 0) { throw "FFmpeg build failed with exit code $LASTEXITCODE." }
} finally {
    Remove-Item Env:FFMPEG_SOURCE_DIR, Env:FFMPEG_OUTPUT_DIR, Env:FFMPEG_FLAGS_FILE, Env:FFMPEG_BUILD_JOBS, Env:FFMPEG_SOURCE_DATE_EPOCH, Env:FFMPEG_SOURCE_SHA256, Env:FFMPEG_VERSION, Env:FFMPEG_TOOLCHAIN_EXPECTED_FILE, Env:FFMPEG_TOOLCHAIN_LOCK_SHA256, Env:FFMPEG_EXPECTED_GCC_VERSION, Env:FFMPEG_EXPECTED_LD_VERSION, Env:FFMPEG_EXPECTED_MAKE_VERSION -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $BuildScriptPath -Force -ErrorAction SilentlyContinue
}

$BuiltBinary = Join-Path $OutputRoot 'ffmpeg.exe'
if (-not (Test-Path -LiteralPath $BuiltBinary)) { throw 'FFmpeg build did not produce dist/ffmpeg/ffmpeg.exe.' }
$ComponentGate = Join-Path $DistRoot 'ffmpeg-component-gate.txt'
$ComponentGatePartial = "$ComponentGate.partial"
if (-not (Test-Path -LiteralPath $ComponentGatePartial)) { throw 'FFmpeg build did not produce a component gate record.' }
$PEContentHash = Get-PEAuthenticodeContentSha256 -Path $BuiltBinary
$GateText = [IO.File]::ReadAllText($ComponentGatePartial)
$GateText = $GateText -replace '(?m)^(binary_size=[0-9]+\r?\n)', "`$1pe_authenticode_content_sha256=$PEContentHash`n"
[IO.File]::WriteAllText("$ComponentGate.final", $GateText, (New-Object Text.UTF8Encoding($false)))
Move-Item -LiteralPath "$ComponentGate.final" -Destination $ComponentGate -Force
Remove-Item -LiteralPath $ComponentGatePartial -Force
Write-Host "Built FFmpeg $Version at $BuiltBinary"
Write-Host "Source SHA-256: $ArchiveSha256"
Write-Host "Signing fingerprint: $SigningFingerprint"
Write-Host "Supplemental signed tag: $SignedTag ($SignedTagCommit, signer $SignedTagFingerprint)"
