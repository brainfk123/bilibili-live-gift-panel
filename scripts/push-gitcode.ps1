[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Token,

  [Parameter(Mandatory = $true)]
  [string[]]$RefSpec,

  [string]$UserName = "brainfk",
  [string]$Owner = "brainfk",
  [string]$Repository = "bilibili-live-gift-panel"
)

$ErrorActionPreference = "Stop"
if ([string]::IsNullOrWhiteSpace($Token)) {
  throw "GitCode token is empty."
}

$credentialPath = Join-Path $env:RUNNER_TEMP "gitcode-credentials-$PID.txt"
$encodedUser = [Uri]::EscapeDataString($UserName)
$encodedToken = [Uri]::EscapeDataString($Token)
$remoteUrl = "https://gitcode.com/$Owner/$Repository.git"
$credentialUrl = "https://${encodedUser}:${encodedToken}@gitcode.com"

try {
  Set-Content -LiteralPath $credentialPath -Value $credentialUrl -Encoding utf8 -NoNewline
  $env:GIT_TERMINAL_PROMPT = "0"
  & git -c "credential.helper=store --file=$credentialPath" push $remoteUrl @RefSpec
  if ($LASTEXITCODE -ne 0) {
    throw "Pushing refs to GitCode failed with exit code $LASTEXITCODE."
  }
} finally {
  Remove-Item -LiteralPath $credentialPath -Force -ErrorAction SilentlyContinue
}

Write-Host "Pushed $($RefSpec.Count) ref(s) to GitCode."
