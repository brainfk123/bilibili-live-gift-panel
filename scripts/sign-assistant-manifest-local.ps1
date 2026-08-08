param(
  [Parameter(Mandatory = $true, Position = 0)]
  [string]$UnsignedManifest,

  [Parameter(Mandatory = $true, Position = 1)]
  [string]$SignedManifest,

  [string]$KeyFile = (Join-Path $env:LOCALAPPDATA 'BilibiliLiveGiftPanel\secrets\assistant-manifest-ed25519.private.pem'),

  [string]$ProtectedPassphraseFile = (Join-Path $env:LOCALAPPDATA 'BilibiliLiveGiftPanel\secrets\assistant-manifest-ed25519.passphrase.dpapi')
)

$ErrorActionPreference = 'Stop'
$entropy = [Text.Encoding]::UTF8.GetBytes('BilibiliLiveGiftPanel/assistant-manifest/v1')
$plainPassphrase = $null

try {
  if (-not (Test-Path -LiteralPath $KeyFile)) {
    throw "Assistant manifest private key does not exist: $KeyFile"
  }
  if (-not (Test-Path -LiteralPath $ProtectedPassphraseFile)) {
    throw "Assistant manifest DPAPI credential does not exist: $ProtectedPassphraseFile"
  }
  $protectedPassphrase = [IO.File]::ReadAllBytes($ProtectedPassphraseFile)
  $plainPassphrase = [Security.Cryptography.ProtectedData]::Unprotect(
    $protectedPassphrase,
    $entropy,
    [Security.Cryptography.DataProtectionScope]::CurrentUser
  )
  $env:ASSISTANT_MANIFEST_PRIVATE_KEY_FILE = $KeyFile
  $env:ASSISTANT_MANIFEST_PRIVATE_KEY_PASSPHRASE = [Text.Encoding]::UTF8.GetString($plainPassphrase)
  & node (Join-Path $PSScriptRoot 'sign-assistant-manifest.mjs') $UnsignedManifest $SignedManifest
  if ($LASTEXITCODE -ne 0) {
    throw "Assistant manifest signing failed with exit code $LASTEXITCODE."
  }
} finally {
  Remove-Item Env:ASSISTANT_MANIFEST_PRIVATE_KEY_FILE -ErrorAction SilentlyContinue
  Remove-Item Env:ASSISTANT_MANIFEST_PRIVATE_KEY_PASSPHRASE -ErrorAction SilentlyContinue
  if ($null -ne $plainPassphrase) {
    [Security.Cryptography.CryptographicOperations]::ZeroMemory($plainPassphrase)
  }
}
