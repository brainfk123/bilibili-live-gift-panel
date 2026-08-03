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

$remoteUrl = "https://gitcode.com/$Owner/$Repository.git"
$basicCredential = [Convert]::ToBase64String(
  [Text.Encoding]::UTF8.GetBytes("${UserName}:${Token}")
)

try {
  $env:GIT_TERMINAL_PROMPT = "0"
  $env:GIT_CONFIG_COUNT = "1"
  $env:GIT_CONFIG_KEY_0 = "http.https://gitcode.com/.extraheader"
  $env:GIT_CONFIG_VALUE_0 = "Authorization: Basic $basicCredential"
  & git push $remoteUrl @RefSpec
  if ($LASTEXITCODE -ne 0) {
    throw "Pushing refs to GitCode failed with exit code $LASTEXITCODE."
  }
} finally {
  Remove-Item Env:GIT_CONFIG_COUNT -ErrorAction SilentlyContinue
  Remove-Item Env:GIT_CONFIG_KEY_0 -ErrorAction SilentlyContinue
  Remove-Item Env:GIT_CONFIG_VALUE_0 -ErrorAction SilentlyContinue
}

Write-Host "Pushed $($RefSpec.Count) ref(s) to GitCode."
