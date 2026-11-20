# WINGET IS AN ALIAS UNDER %LOCALAPPDATA%\Microsoft\WindowsApps AND IS NOT ON THE MACHINE
# PATH, so an `ssh exec` shell - which gets the environment sshd captured, not the user's
# profile - cannot name it. This is the same lookup windows/11/probe.ps1 does for `pwsh`,
# for the same reason. Resolved in THIS process so no state has to survive between steps.
$winget = $null
$dirs = @()
foreach ($scope in @('Machine', 'User')) {
  try { $dirs += ([Environment]::GetEnvironmentVariable('Path', $scope) -split ';') } catch { }
}
$dirs += (Join-Path $env:LOCALAPPDATA 'Microsoft\WindowsApps')
foreach ($dir in ($dirs | Where-Object { $_ })) {
  $cand = Join-Path $dir 'winget.exe'
  if (Test-Path $cand) { $winget = $cand; break }
}
if (-not $winget) {
  Write-Output 'WINGET_NOT_FOUND'
  exit 1
}
# THE INSTALL IS RETRIED, BECAUSE 2147500036 IS THE NETWORK AND IT MOVES BETWEEN STEPS.
#
# MEASURED ACROSS FIVE RUNS: the failing installer CHANGED each time - dotnet, then python,
# then dotnet again - while its neighbours in the same run succeeded. 2147500036 is
# 0x80004004, E_ABORT: winget download was cut off. This stage pulls several large
# installers from the public internet, and retrying is what a downloader does about that.
#
# THREE ATTEMPTS, TEN SECONDS APART, and only for that code.
$attempt = 0
do {
  $attempt++
  & $winget install --id Git.Git --exact --source winget --scope machine `
    --accept-source-agreements --accept-package-agreements --disable-interactivity
  $rcNow = $LASTEXITCODE
  Write-Output ("WINGET_ATTEMPT=" + $attempt + " EXIT=" + $rcNow)
  if (($rcNow -band 0xFFFFFFFF) -eq 0x80004004) {
    if ($attempt -lt 3) { Start-Sleep -Seconds 10 }
  } else {
    break
  }
} while ($attempt -lt 3)
$rc = $rcNow
# THE EXIT CODE IS READ ON THE LINE IMMEDIATELY AFTER THE NATIVE CALL, and that placement
# is itself a fix. The first version read it after a twenty-line comment block and printed
# nothing at all:
#
#     script install-the-windows-sdk.ps1 (powershell) -> 43
#     Found an existing package already installed. ...
#
# with no `ALREADY_PRESENT`, so the `if` never saw the 43 that the exit status carried.
# `$LASTEXITCODE` belongs to the last native command; read it adjacent or do not trust it.
$null = $LASTEXITCODE
#
# "ALREADY INSTALLED" IS A SUCCESS, AND WINGET REPORTS IT AS EXIT CODE 43.
#
# Run 42966695727: the BuildTools install took 13m42s and exited 0 - the VCTools workload
# brought the Windows SDK with it, which is what this file says it does ("The Windows SDK
# and CMake come with the workload, which is why they are not installed separately here").
# The next step then asked winget for the SDK and got:
#
#     Found an existing package already installed. Trying to upgrade ...
#     No available upgrade found.
#     No newer package versions are available from the configured sources.
#     -> 43
#
# 43 is APPINSTALLER_CLI_ERROR_UPDATE_NOT_APPLICABLE: the package is present and there is
# nothing to do. Treating that as a failure makes this stage impossible to pass on a disk
# that already has the toolchain - which is the state THIS STAGE ITSELF creates.
#
# `windows/11/devtools.py` holds this judgement in Python already. The YAML had no
# equivalent, so the rule lived in one place and its duplicates did not.
#
# ANY OTHER NON-ZERO IS STILL A FAILURE, named rather than swallowed:
# THE VALUE IS PRINTED UNCONDITIONALLY, BECAUSE THE FIRST TWO ATTEMPTS AT THIS CHECK
# COULD NOT BE READ. The script exited 43 - so the 43 reached `exit` - and `ALREADY_PRESENT`
# never appeared, which admits two explanations and the log could not tell them apart:
# the branch did not run, or its output was cut off by the forty-line cap `lib/steps.py`
# puts on a script's output. One line removes the ambiguity, and it is cheap enough to
# keep: the next reader should not have to reconstruct this from an exit code.
Write-Output ("WINGET_EXIT=" + $rc)
# COMPARED ON THE LOW BYTE, BECAUSE $LASTEXITCODE IS NOT THE NUMBER THE SHELL REPORTS.
#
# Three attempts failed here on `$rc -eq 43`. The printed value, added this time, is what
# finally said why:
#
#     WINGET_EXIT=-1978335189
#
# -1978335189 is 0x8A15002B: winget's HRESULT for APPINSTALLER_CLI_ERROR_UPDATE_NOT_APPLICABLE,
# as PowerShell carries it (signed 32-bit). `-1978335189 & 0xFF` is 0x2B, which is 43 - the
# number a POSIX shell would have reported, and the number `lib/steps.py` logs as `-> 43`.
#
# So the exit status in the log and the value in the variable are the same fact spelled two
# ways, and the branch compared one against the other. `0x8A15002B` AND `43` are not equal
# and never were.
#
# THE MASK IS THE RULE, NOT A WORKAROUND FOR THIS ONE CODE: `$LASTEXITCODE` in PowerShell is
# an int32 carrying an HRESULT when the program returns one, and every winget error would
# have hit the same trap.
$low = $rc -band 0xFF
if ($low -eq 43) {
  Write-Output 'ALREADY_PRESENT (winget 0x8A15002B: no upgrade applicable)'
  exit 0
}
exit $rc
