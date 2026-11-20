$ErrorActionPreference = 'Continue'
$dotnet = Join-Path $env:ProgramFiles 'dotnet\dotnet.exe'
Write-Output "dotnet: $dotnet"
Write-Output "exists: $(Test-Path $dotnet)"
if (-not (Test-Path $dotnet)) {
  $onPath = Get-Command dotnet -ErrorAction SilentlyContinue
  Write-Output "on path: $($onPath.Source)"
  if (-not $onPath) { Write-Output "DOTNET_MISSING"; exit 1 }
  $dotnet = $onPath.Source
}
$sdks = & $dotnet --list-sdks 2>&1
Write-Output "sdks:"
$sdks | ForEach-Object { Write-Output "  $_" }
if (-not $sdks) { Write-Output "NO_SDK"; exit 1 }
$dir = Join-Path $env:TEMP 'dotnet-check'
if (Test-Path $dir) { Remove-Item -Recurse -Force $dir }
New-Item -ItemType Directory -Force -Path $dir | Out-Null
& $dotnet new console --output $dir --force 2>&1 | ForEach-Object { Write-Output "  $_" }
$newcode = $LASTEXITCODE
Write-Output "new exit: $newcode"
if ($newcode -ne 0) { Write-Output "TEMPLATE_FAILED"; exit 1 }
& $dotnet run --project $dir 2>&1 | ForEach-Object { Write-Output "  $_" }
$runcode = $LASTEXITCODE
Write-Output "run exit: $runcode"
if ($runcode -ne 0) { Write-Output "BUILD_FAILED"; exit 1 }
Write-Output "DOTNET_OK"
