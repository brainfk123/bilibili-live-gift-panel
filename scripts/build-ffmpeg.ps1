[CmdletBinding()]
param(
    [string]$Msys2Root = 'C:\msys64',
    [int]$Jobs = [Environment]::ProcessorCount
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
$ArchivePath = Join-Path $DownloadRoot $ArchiveName
$SignaturePath = "$ArchivePath.asc"
$PublicKeyPath = Join-Path $DownloadRoot 'ffmpeg-devel.asc'
$GpgHome = Join-Path $DownloadRoot 'gnupg'
$Bash = Join-Path $Msys2Root 'usr\bin\bash.exe'
$GpgCommand = Get-Command gpg.exe -ErrorAction SilentlyContinue
$Gpg = if ($null -ne $GpgCommand) { $GpgCommand.Source } else { Join-Path $Msys2Root 'usr\bin\gpg.exe' }

if (-not (Test-Path -LiteralPath $Bash)) {
    throw "MSYS2 was not found at $Msys2Root. Install MSYS2 with the UCRT64 toolchain or pass -Msys2Root."
}
if (-not (Test-Path -LiteralPath (Join-Path $Msys2Root 'ucrt64\bin\gcc.exe'))) {
    throw 'The MSYS2 UCRT64 GCC toolchain is missing. Install mingw-w64-ucrt-x86_64-toolchain, make, diffutils, and pkgconf.'
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

New-Item -ItemType Directory -Force -Path $DownloadRoot, $OutputRoot, $GpgHome | Out-Null
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

function ConvertTo-MsysPath {
    param([string]$Path)
    $env:GIFT_PANEL_PATH_TO_CONVERT = $Path
    try {
        $Converted = & $Bash -lc 'cygpath -u "$GIFT_PANEL_PATH_TO_CONVERT"'
        if ($LASTEXITCODE -ne 0 -or -not $Converted) { throw "Could not convert path for MSYS2: $Path" }
        return $Converted.Trim()
    } finally {
        Remove-Item Env:GIFT_PANEL_PATH_TO_CONVERT -ErrorAction SilentlyContinue
    }
}

$GpgHomeArgument = ConvertTo-MsysPath -Path $GpgHome
$PublicKeyArgument = ConvertTo-MsysPath -Path $PublicKeyPath
$SignatureArgument = ConvertTo-MsysPath -Path $SignaturePath
$ArchiveArgument = ConvertTo-MsysPath -Path $ArchivePath
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
$BuildCommand = @'
set -euo pipefail
export PATH="/ucrt64/bin:/usr/bin:$PATH"
export SOURCE_DATE_EPOCH="$FFMPEG_SOURCE_DATE_EPOCH"
expected_toolchain=$(cat <<'EOF'
diffutils=3.12-1
make=4.4.1-3
mingw-w64-ucrt-x86_64-binutils=2.47-3
mingw-w64-ucrt-x86_64-crt=14.0.0.r262.g5ea8e9fac-1
mingw-w64-ucrt-x86_64-gcc=16.2.0-3
mingw-w64-ucrt-x86_64-gcc-libs=16.2.0-3
mingw-w64-ucrt-x86_64-headers=14.0.0.r262.g5ea8e9fac-1
mingw-w64-ucrt-x86_64-libwinpthread=14.0.0.r262.g5ea8e9fac-1
mingw-w64-ucrt-x86_64-winpthreads=14.0.0.r262.g5ea8e9fac-1
mingw-w64-ucrt-x86_64-zlib=1.3.2-2
nasm=2.16.03-1
pkgconf=3.0.5-1
EOF
)
expected_toolchain=$(printf '%s\n' "$expected_toolchain" | sort)
toolchain_packages=$(printf '%s\n' "$expected_toolchain" | cut -d= -f1)
actual_toolchain=$(pacman -Q $toolchain_packages | sed 's/ /=/' | sort)
if ! diff -u <(printf '%s\n' "$expected_toolchain") <(printf '%s\n' "$actual_toolchain"); then
  echo 'MSYS2 build package versions differ from the pinned toolchain.' >&2
  exit 1
fi
gcc_version=$(gcc --version | head -n1)
ld_version=$(ld --version | head -n1)
make_version=$(make --version | head -n1)
test "$gcc_version" = 'gcc.exe (Rev3, Built by MSYS2 project) 16.2.0'
test "$ld_version" = 'GNU ld (GNU Binutils) 2.47.20260726'
test "$make_version" = 'GNU Make 4.4.1'
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
    Remove-Item Env:FFMPEG_SOURCE_DIR, Env:FFMPEG_OUTPUT_DIR, Env:FFMPEG_FLAGS_FILE, Env:FFMPEG_BUILD_JOBS, Env:FFMPEG_SOURCE_DATE_EPOCH, Env:FFMPEG_SOURCE_SHA256, Env:FFMPEG_VERSION -ErrorAction SilentlyContinue
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
