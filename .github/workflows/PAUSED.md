# Paused pending a version bump

These three operating systems have had their upstream version changed. The catalogue, the asset
alias and the walkthrough all name the NEW version, so the old tag no longer exists - but the
walkthrough for the new one has never been measured on a screen.

DISPATCHING THEM NOW WOULD FAIL FOR A REASON THAT IS NOT A BUG: `easyvm check` passes, the ISO
downloads, the guest boots, and every coordinate was measured against a different installer.

| OS | was | now | why it is paused |
|---|---|---|---|
| `fedora-workstation` | 43 | **45** | Anaconda's sidebar and hub layout change between major releases; nothing here has been measured on 45 |
| `opensuse-tumbleweed` | tumbleweed (KDE Live) | **Leap 16.1** | Leap 16.1 SHIPS NO LIVE DESKTOP. Only Agama, an OEM image and an Immutable self-install image - so the YaST2 walkthrough is not merely stale, it describes a product that no longer exists |
| `zorin` | 18 Core r3 | **18.1 Core** | Ubiquity panes move between point releases; 18.1 has not been recorded |
| `ghostbsd` | 24.10.1 | **26.3-R15.1p6** | four releases behind, and the installer between FreeBSD 14 and 15 is not the same program |

## Resuming one

1. Dispatch `artifacts-publish.yml` with `which: <its iso- tag>` and confirm the ISO lands in the
   package.
2. Dispatch that OS's workflow ONCE and let the install leg run. Its `-video` artifact is the
   measurement.
3. Extract frames at the times the log names and adjust the coordinates in
   `<os>/steps/install.yaml`. For Leap the whole walkthrough has to be rewritten for Agama.

## The other twelve

`ubuntu`, `kubuntu`, `mint`, `mx`, `popos`, `nixos`, `manjaro`, `cachyos`, `endeavouros`,
`windows`, `macos` are all on their latest stable releases and are not affected.
