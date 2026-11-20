$ErrorActionPreference = 'Continue'
$vswhere = "${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe"
Write-Output "vswhere: $vswhere"
if (-not (Test-Path $vswhere)) {
  Write-Output "VSWHERE_MISSING"
  exit 1
}
$root = & $vswhere -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
Write-Output "installation: $root"
if (-not $root) {
  Write-Output "VCTOOLS_MISSING"
  exit 1
}
$vcvars = Join-Path $root 'VC\Auxiliary\Build\vcvars64.bat'
Write-Output "vcvars: $vcvars"
if (-not (Test-Path $vcvars)) {
  Write-Output "VCVARS_MISSING"
  exit 1
}
$dir = Join-Path $env:TEMP 'msvc-check'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$src = Join-Path $dir 'hello.c'
Set-Content -Path $src -Value '#include <stdio.h>' -Encoding ASCII
Add-Content -Path $src -Value 'int main(void){ printf("hello-from-msvc\n"); return 0; }' -Encoding ASCII
$bat = Join-Path $dir 'build.bat'
Set-Content -Path $bat -Value "@call `"$vcvars`"" -Encoding ASCII
Add-Content -Path $bat -Value "cl /nologo `"$src`" /Fe:`"$dir\hello.exe`"" -Encoding ASCII
Add-Content -Path $bat -Value "exit /b %ERRORLEVEL%" -Encoding ASCII
& cmd /c $bat
$clcode = $LASTEXITCODE
Write-Output "cl exit: $clcode"
if ($clcode -ne 0) { Write-Output "COMPILE_FAILED"; exit 1 }
$exe = Join-Path $dir 'hello.exe'
if (-not (Test-Path $exe)) { Write-Output "NO_EXE"; exit 1 }
& $exe
if ($LASTEXITCODE -ne 0) { Write-Output "RUN_FAILED"; exit 1 }
Write-Output "COMPILE_OK"
