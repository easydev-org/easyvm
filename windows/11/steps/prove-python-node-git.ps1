$ErrorActionPreference = 'Continue'
$bad = 0
foreach ($t in @('python', 'node', 'git')) {
  $c = Get-Command "$t.exe" -ErrorAction SilentlyContinue
  if ($c) {
    Write-Output "ok      $t -> $($c.Source)"
  } else {
    Write-Output "MISSING $t"
    $bad = 1
  }
}
if ($bad -ne 0) {
  Write-Output "=== THE THREE THE macOS DISK ALSO HAS ARE NOT ALL HERE ==="
  exit 1
}
Write-Output "PYTHON_NODE_GIT_OK"
