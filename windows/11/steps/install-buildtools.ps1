# WINGET IS AN ALIAS UNDER %LOCALAPPDATA%\Microsoft\WindowsApps AND IS NOT ON THE MACHINE
# PATH, so an `ssh exec` shell - which gets the environment sshd captured, not the user's
# profile - cannot name it. Resolved here, per step, because every step is a new process.
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
# THE PINNED VERSION AND THE WORKLOAD ARE WHY THIS IS A SCRIPT AND NOT A ONE-LINER: the
# `--override` string carries spaces, quotes and a comma-free workload list, and by the
# time it has been through ssh and cmd the quotes are gone. windows/11/devtools.py holds
# the measurements that chose 17.14.44 over 17.9.7 and over the aka.ms bootstrapper.
# `--quiet`, `--wait` AND `--norestart` ARE NOT ALLOWED BESIDE `--override`, and the file
# already said so eight hundred lines up: "a package with an --override also cannot take
# --accept-package-agreements - override replaces winget's own switch set". Run 42965954028
# proved it for three more of them:
#
#     Argument name was not recognized for the current command: '--quiet'
#
# The override string is passed to the Visual Studio bootstrapper itself, so the switches
# that would have controlled winget are carried INSIDE it instead:
#
#     --quiet --wait --norestart
#
# and `--disable-interactivity` is winget's own and stays outside, because it is what stops
# a first-run agreement prompt from waiting for a console that does not exist.
& $winget install --id Microsoft.VisualStudio.2022.BuildTools --exact --source winget `
  --scope machine --version 17.14.44 --accept-source-agreements --disable-interactivity `
  --override "--quiet --wait --norestart --add Microsoft.VisualStudio.Workload.VCTools --includeRecommended" `
  # THE EXIT CODE IS READ ON THE LINE IMMEDIATELY AFTER THE NATIVE CALL. `$LASTEXITCODE`
# belongs to the last native command, and a version of this step read it after a twenty-line
# comment block and printed nothing at all.
$rc = $LASTEXITCODE
Write-Output ("WINGET_EXIT=" + $rc)

# "ALREADY INSTALLED" IS A SUCCESS, IN THE SAME RULE THE SIX STEPS BELOW ALREADY USE.
#
# Measured on windows-dev, step 5 of 18, and it is the only install step that lacked this:
#
#     wait for winget to have an index -> 0, WINGET_READY
#     install the build tools -> 2316632107
#     Found an existing package already installed. Trying to upgrade the installed package...
#     No available upgrade found.
#     No newer package versions are available from the configured sources.
#
# 2316632107 IS 0x8A15002B, WHICH IS NOT "NO SUCH PACKAGE" - IT IS
# APPINSTALLER_CLI_ERROR_UPDATE_NOT_APPLICABLE: the package is present and there is nothing to
# upgrade to. The disk this stage starts from already has the build tools, so `winget install`
# takes its upgrade path and reports that there is nothing to do.
#
# AN EARLIER FIX GUESSED THE INDEX WAS NOT SYNCED and added a wait for `winget search` to answer.
# IT NOW ANSWERS - `WINGET_READY` - AND THE SAME CODE CAME BACK, which is what makes this the
# explanation rather than another hypothesis: the index was never the problem.
#
# THE MASK IS THE RULE. `$LASTEXITCODE` is an int32 carrying an HRESULT: -1978335189 & 0xFF is
# 0x2B, 43, which is the number a POSIX shell reports and the number `lib/steps.py` logs as
# `-> 43`. Comparing `$rc -eq 43` never matched, three attempts in a row, on the other steps.
$low = $rc -band 0xFF
if ($low -eq 43) {
  Write-Output 'ALREADY_PRESENT (winget 0x8A15002B: no upgrade applicable)'
  exit 0
}
if ($rc -ne 0) {
  Write-Output ("WINGET_INSTALL_FAILED " + $rc)
  exit $rc
}
Write-Output 'BUILD_TOOLS_INSTALLED'
exit 0
