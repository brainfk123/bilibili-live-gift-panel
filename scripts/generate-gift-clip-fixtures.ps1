[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$ffmpeg = @($env:FFMPEG_FULL_BIN, 'D:\Program Files\ffmpeg\bin\ffmpeg.exe') |
  Where-Object { $_ -and (Test-Path -LiteralPath $_) } |
  Select-Object -First 1
if (-not $ffmpeg) { throw 'Set FFMPEG_FULL_BIN to a full FFmpeg executable.' }

$version = @(& $ffmpeg -version)
if ($LASTEXITCODE -ne 0 -or $version.Count -eq 0) {
  throw 'The selected full FFmpeg executable did not report a version.'
}
Write-Host "Gift clip fixture FFmpeg: $($version[0])"

$encoders = @(& $ffmpeg -hide_banner -encoders 2>&1)
if ($LASTEXITCODE -ne 0) { throw 'The selected full FFmpeg executable could not list encoders.' }
foreach ($requiredEncoder in @('libwebp_anim', 'libx264')) {
  if (-not ($encoders | Select-String -SimpleMatch $requiredEncoder -Quiet)) {
    throw "The selected full FFmpeg executable is missing required encoder $requiredEncoder."
  }
}

$repositoryRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$fixtureRoot = Join-Path $repositoryRoot 'tests\fixtures\gift-clip-media'
New-Item -ItemType Directory -Force -Path $fixtureRoot | Out-Null

$gifPath = Join-Path $fixtureRoot 'input-10fps.gif'
$webpPath = Join-Path $fixtureRoot 'input-20fps.webp'
$effectPath = Join-Path $fixtureRoot 'packed-alpha-24fps.mp4'
$layoutPath = Join-Path $fixtureRoot 'packed-alpha-layout.json'

& $ffmpeg -f lavfi -i 'testsrc2=size=320x180:rate=10:duration=2' -loop 0 -y $gifPath
if ($LASTEXITCODE -ne 0) { throw 'Failed to generate input-10fps.gif with full FFmpeg.' }
& $ffmpeg -f lavfi -i 'testsrc2=size=320x180:rate=20:duration=2' -c:v libwebp_anim -loop 0 -y $webpPath
if ($LASTEXITCODE -ne 0) { throw 'Failed to generate input-20fps.webp with full FFmpeg.' }
& $ffmpeg -f lavfi -i 'testsrc2=size=320x180:rate=24:duration=2' -f lavfi -i 'testsrc2=size=320x180:rate=24:duration=2' -filter_complex '[0:v][1:v]hstack=inputs=2,format=yuv420p' -c:v libx264 -an -y $effectPath
if ($LASTEXITCODE -ne 0) { throw 'Failed to generate packed-alpha-24fps.mp4 with full FFmpeg.' }

[IO.File]::WriteAllText(
  $layoutPath,
  "{`"videoWidth`":640,`"videoHeight`":180,`"rgbFrame`":[0,0,320,180],`"alphaFrame`":[320,0,320,180],`"fps`":24,`"frames`":48}`n",
  [Text.UTF8Encoding]::new($false)
)

foreach ($fixture in @($gifPath, $webpPath, $effectPath, $layoutPath)) {
  $item = Get-Item -LiteralPath $fixture
  if ($item.Length -ge 1MB) {
    throw "Generated fixture $($item.Name) is $($item.Length) bytes; fixtures must remain below 1 MiB."
  }
  Write-Host "$($item.Name): $($item.Length) bytes"
}
