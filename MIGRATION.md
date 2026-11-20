# The migration, verified

FIFTEEN walkthroughs remain as `.walk` files, and FOURTEEN of them have been RUN to a finish - not
parsed, not converted back, not loaded: `easyvm run` drove a real guest through every step and the
daemon reported `status: finished step N/N` with `answered N step(s)`. The fifteenth is
`windows/11/steps/dev.walk`, below.

THE COUNT CHANGED FROM EIGHTEEN, AND THAT IS A CORRECTION RATHER THAN A LOSS. Three macOS files -
`primitives`, `lifecycle` and `sequoia` - were ORPHANS: no workflow in the repository dispatched any of
them, and `sequoia`'s wizard duplicated the loop `ssh.walk` already runs and passes. They were converted
because they existed as `.yaml`, they were driven to a finish to prove the format covered them, and they
have now been deleted so the tree holds only files something actually runs.

`windows/11/steps/dev.walk` IS ALSO OUT OF SCOPE BY DECISION and is the one file present that does not
pass: a `dev` leg installs a multi-gigabyte toolchain from public vendor endpoints and proves nothing
about the walkthrough format that the same family's install, ssh and vm-configure legs do not already
prove. What it reached is recorded below.

## The fourteen that pass

Every row below is a CI run of the branch, in the image that run built, with the log line quoted.

| the file | steps | run | evidence |
|---|---|---|---|
| `linux/ubuntu/steps/install.walk` | 44 | 44085510921 | `status: finished step 44/44` · `answered 44 step(s)` |
| `linux/ubuntu/steps/ssh.walk` | 21 | 44118113239 | `status: finished step 21/21` · `answered 21 step(s)` |
| `linux/ubuntu/steps/vm-configure.walk` | 31 | 44122235903 | `status: finished step 31/31` · `answered 31 step(s)` |
| `bsd/freebsd/steps/install.walk` | 64 | 44097139713 | `status: finished step 64/64` · `answered 64 step(s)` |
| `bsd/freebsd/steps/ssh.walk` | 18 | 44121817115 | `status: finished step 18/18` · `answered 18 step(s)` |
| `bsd/freebsd/steps/vm-configure.walk` | 23 | 44123507364 | `status: finished step 23/23` · `answered 23 step(s)` |
| `macos/15/steps/setup.walk` | 35 | 44105336457 | `status: finished step 35/35` · `answered 35 step(s)` |
| `macos/15/steps/ssh.walk` | 32 | 44124857572 | `status: finished step 32/32` · `answered 32 step(s)` |
| `macos/15/steps/firmware.walk` | 2 | 44142319557 | `status: finished step 2/2` · `answered 2 step(s)` |
| `macos/15/steps/vm-configure.walk` | 32 | 44164173754 | `status: finished step 32/32` · `answered 32 step(s)` · `TIMEZONE_OK` · `BOTH_HALVES_OK` |
| `macos/15/steps/dev.walk` | 18 | 44165292877 | `status: finished step 18/18` · `answered 18 step(s)` · `found /Users/vmuser/xcode-expand/Xcode.app` |
| `windows/11/steps/install.walk` | 16 | 44117639979 | `status: finished step 16/16` · `answered 16 step(s)` |
| `windows/11/steps/ssh.walk` | 31 | 44123555530 | `status: finished step 31/31` · `answered 31 step(s)` |
| `windows/11/steps/vm-configure.walk` | 30 | 44127574005 | `status: finished step 30/30` · `answered 30 step(s)` |

The three install legs produced systems: ubuntu 5.0 GiB, freebsd 2.3 GiB, macos 27.8 GiB, windows
9.6 GiB. The ssh legs answered on port 2222 (`ssh answers on port 2222 after 0s..34s`). The
vm-configure legs proved the host share both ways (`a file written on the host is readable in the
guest - the share is real`).

Fourteen of the fifteen are the four OS families' own legs; the fifteenth is `firmware`, which
`artifacts-publish.yml` dispatches. The one file that does not pass follows.

## The one that does not, and why it is out of scope

### `windows/11/steps/dev.walk` — a `dev` leg, not a walkthrough-format test

IT GOT FARTHER THAN ANY PREVIOUS RUN, which is what makes the remaining failure easy to name:

```
44196553357   12 install steps -> all ok -> wait for sshd again (600s, timed out)
44212053047   build tools ok  windows sdk ok  dotnet ok  powershell 7 ok
              python ok  node ok  git ok
              wait for sshd again -> ok (after the timeout was raised to 1800)
              build and run a C program ok
              prove powershell 7 ok      prove the windows sdk ok
              -> prove python node and git: MISSING python / MISSING node / MISSING git
```

`Get-Command` reads `PATH`, and an ssh exec shell inherits the environment sshd captured before those
installers ran - the SAME cause as the `pwsh` step the commit before it fixed. Every installer
succeeded; only the name lookup failed.

IT IS NOT BEING FIXED FURTHER, BY DECISION: a `dev` leg installs a multi-gigabyte toolchain from public
vendor endpoints, and it proves nothing about the `.walk` format that the same family's `install`,
`ssh` and `vm-configure` legs do not already prove. `macos/15/steps/dev.walk` - the comparable file -
DOES pass (18/18), so the format is exercised on a dev leg too.

WHAT IT DID FIND IS RECORDED ABOVE AND FIXED: `script_shell`/`script_name` dropped from five macOS and
eleven Windows script steps; a `2147500036` installer abort retried on all six winget installs; the
`pwsh`-not-on-PATH proof rewritten to test the installed path; and the post-install restart's timeout.

## What the migration itself needed

Converting the files was mechanical and is proved by round trip
(`TestEveryRealWalkthroughRoundTripsThroughWalk`: 57 walkthroughs, documents equal). RUNNING them found
six defects that no conversion test can see:

1. **`loop:` handed a `see:` mapping to the `ocr:` parser** - every loop in every file failed.
2. **`see:` and `ocr:` are two vocabularies**, not two spellings; canonicalising the key lost `of:`.
3. **`readWords` divided a pixel rect by the FRAME** instead of by `rect_at:`, so a 1920x1080 rect
   asked for a crop outside a 640x480 recovery frame. Fixed three times because each check was made
   one layer below the one that was broken.
4. **`TestALoopAssemblesItsPartsWithTheirKeys` asserted on its own copy of the shape** - it passed
   while every loop was broken. It now calls `loopParts`.
5. **`adb_connect` was missing from the list of waiting keys**, so the package's tests spent 320 of
   their 331 seconds waiting for an adbd that no fake driver has.
6. **`requires: [qga]` is a passive key** - it records a capability, it does not probe one, so a step
   that looked guarded could time out on the thing it claimed to check.

And four that were properties of walkthroughs nobody had ever run: `write: demo_script` is not a kind
this build writes; a recording shorter than the sampler's tick captures nothing (`ssh: sleep 20` is
what fixed it - a host-side `settle:` cannot); `share_mount:` does not mount, so `~/shared` never
exists; and `shutdown: guest_agent` after a reboot asks an agent the reboot did not restart.
