$winget = $null
$dirs = @()
foreach ($scope in @('Machine', 'User')) {
  try { $dirs += ([Environment]::GetEnvironmentVariable('Path', $scope) -split ';') } catch { }
}
$dirs += @("$env:LOCALAPPDATA\Microsoft\WindowsApps", "$env:ProgramFiles\WindowsApps")
foreach ($d in $dirs) {
  if (-not $d) { continue }
  $cand = Join-Path $d 'winget.exe'
  if (Test-Path $cand) { $winget = $cand; break }
}
if (-not $winget) {
  # A WINDOWS 11 GUEST MAY NOT SHIP winget AT ALL, so its absence is reported rather than waited
  # on - the step after this one needs a different install path entirely.
  Write-Output 'WINGET_NOT_FOUND'
  exit 1
}
# THE QUERY IS THE READINESS TEST: `winget search` answers from the index, so a successful
# search is the index existing.
$deadline = (Get-Date).AddMinutes(10)
do {
  $out = & $winget search --id Microsoft.VisualStudio.2022.BuildTools --exact `
    --accept-source-agreements --disable-interactivity 2>&1 | Out-String
  if ($LASTEXITCODE -eq 0 -and $out -match 'BuildTools') {
    Write-Output 'WINGET_READY'
    exit 0
  }
  Write-Output ('waiting for the winget index: ' + ($out -replace "`r?`n", ' ').Trim())
  Start-Sleep -Seconds 15
} while ((Get-Date) -lt $deadline)
Write-Output 'WINGET_INDEX_NEVER_READY'
exit 1
