[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Token,

  [Parameter(Mandatory = $true)]
  [string]$Tag,

  [Parameter(Mandatory = $true)]
  [string[]]$AssetPath,

  [string]$Owner = "brainfk",
  [string]$Repository = "bilibili-live-gift-panel"
)

$ErrorActionPreference = "Stop"
$apiRoot = "https://api.gitcode.com/api/v5/repos/$Owner/$Repository"
$headers = @{
  Accept = "application/json"
  Authorization = "Bearer $Token"
}

function Invoke-GitCodeJson {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Method,
    [Parameter(Mandatory = $true)]
    [string]$Uri,
    [object]$Body
  )

  $arguments = @{
    Method = $Method
    Uri = $Uri
    Headers = $headers
  }
  if ($null -ne $Body) {
    $arguments.ContentType = "application/json; charset=utf-8"
    $arguments.Body = $Body | ConvertTo-Json -Depth 8 -Compress
  }
  Invoke-RestMethod @arguments
}

function Get-StatusCode {
  param([System.Management.Automation.ErrorRecord]$ErrorRecord)
  if ($null -eq $ErrorRecord.Exception.Response) {
    return 0
  }
  return [int]$ErrorRecord.Exception.Response.StatusCode
}

function Get-UploadTarget {
  param([object]$UploadResponse)
  $payload = $UploadResponse
  $data = $UploadResponse.PSObject.Properties["data"]
  if ($null -ne $data -and $null -ne $data.Value) {
    $payload = $data.Value
  }

  $urlProperty = $payload.PSObject.Properties["url"]
  if ($null -eq $urlProperty -or [string]::IsNullOrWhiteSpace([string]$urlProperty.Value)) {
    throw "GitCode did not return a usable Release upload URL."
  }
  $headersProperty = $payload.PSObject.Properties["headers"]
  return [pscustomobject]@{
    Url = [string]$urlProperty.Value
    Headers = if ($null -eq $headersProperty) { $null } else { $headersProperty.Value }
  }
}

function Invoke-CurlUpload {
  param(
    [Parameter(Mandatory = $true)]
    [object]$UploadTarget,
    [Parameter(Mandatory = $true)]
    [string]$FilePath
  )

  $curlArguments = @(
    "--fail-with-body",
    "--silent",
    "--show-error",
    "--location",
    "--request", "PUT"
  )
  if ($null -ne $UploadTarget.Headers) {
    foreach ($property in $UploadTarget.Headers.PSObject.Properties) {
      $curlArguments += @("--header", "$($property.Name): $($property.Value)")
    }
  }
  $curlArguments += @("--data-binary", "@$FilePath", [string]$UploadTarget.Url)
  & curl.exe @curlArguments
  return $LASTEXITCODE -eq 0
}

$encodedTag = [Uri]::EscapeDataString($Tag)
$releaseUri = "$apiRoot/releases/$encodedTag"
$releaseCreateBody = @{
  tag_name = $Tag
  name = $Tag
  body = "详细更新说明：https://github.com/brainfk123/bilibili-live-gift-panel/releases/tag/$Tag"
  release_status = "latest"
}
$releaseUpdateBody = @{
  name = $releaseCreateBody.name
  body = $releaseCreateBody.body
  release_status = $releaseCreateBody.release_status
}

$releaseExists = $false
$releaseInfo = $null
try {
  $releaseInfo = Invoke-GitCodeJson -Method Get -Uri $releaseUri
  $releaseExists = $true
} catch {
  if ((Get-StatusCode $_) -ne 404) {
    throw
  }
}

if ($releaseExists) {
  $releaseInfo = Invoke-GitCodeJson -Method Patch -Uri $releaseUri -Body $releaseUpdateBody
} else {
  $releaseInfo = Invoke-GitCodeJson -Method Post -Uri "$apiRoot/releases" -Body $releaseCreateBody
}

foreach ($path in $AssetPath) {
  $resolvedPath = (Resolve-Path -LiteralPath $path).Path
  $fileName = Split-Path -Leaf $resolvedPath
  $existingAsset = @($releaseInfo.assets) | Where-Object { $_.name -eq $fileName } | Select-Object -First 1
  if ($null -ne $existingAsset -and -not [string]::IsNullOrWhiteSpace([string]$existingAsset.id)) {
    $deleteUri = "$releaseUri/attach_files/$([Uri]::EscapeDataString([string]$existingAsset.id))"
    try {
      $null = Invoke-GitCodeJson -Method Delete -Uri $deleteUri
    } catch {
      if ((Get-StatusCode $_) -ne 404) {
        throw
      }
    }
  }

  $encodedName = [Uri]::EscapeDataString($fileName)
  $uploadResponse = Invoke-GitCodeJson -Method Get -Uri "$releaseUri/upload_url?file_name=$encodedName"
  $uploadTarget = Get-UploadTarget $uploadResponse
  $uploaded = Invoke-CurlUpload -UploadTarget $uploadTarget -FilePath $resolvedPath
  if (-not $uploaded) {
    throw "Uploading $fileName to GitCode failed."
  }
}

Write-Host "Published $Tag and $($AssetPath.Count) assets to GitCode."
