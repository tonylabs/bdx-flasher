# BDX Flasher

A Go/Fyne application that flashes OpenBDX firmware onto a robot main board **chosen by the user**.
It is the multi-board counterpart of the BDX Orin Flasher, and the same executable has a GUI and a
terminal mode (`--cli`).

| Main board | How it is flashed |
|---|---|
| **Radxa ROCK 5B+** (RK3588, Armbian trixie) | Writes `bdx-system-rock5bplus-*.img.xz` to a microSD card, or to an NVMe SSD / eMMC module in a USB adapter. The data is read back and checked afterwards. **Or** writes it over USB to the onboard eMMC of a board in **Maskrom mode** (`rkdeveloptool`). |
| NVIDIA Jetson Orin Nano Super Dev Kit | Hands over to the installed **BDX Orin Flasher**, which does the recovery-mode USB flash. |
| NVIDIA Jetson Orin NX 8 GB / 16 GB | Same as the Orin Nano. |

## Using it

1. Pick the **main board**.
2. **ROCK 5B+**: the newest image in `~/Desktop/OpenBDX/out`, `~/Downloads` or `~/.cache/bdx-flasher` is
   preselected. You can choose another with **Browse…**. If a `.sha256` file sits next to the image,
   the image is checked against it before flashing.
3. Pick the **target drive**. Only removable/USB/SD drives are listed, and the list refreshes when media
   is plugged in. The disk this computer runs from is never offered. **Show internal drives** adds
   other internal disks.
4. Click **Flash**. The **Confirm flash** dialog offers **Clean install (reset system and /data)**, which is
   unchecked by default. You are then asked for your password (polkit) and the image is
   written and verified.

For Jetson boards the app shows the board's recovery-mode status (USB ID `0955:7523` / `7423` / `7323`),
lets you pick an optional local bundle, and opens the BDX Orin Flasher.

### Keeping /data on the ROCK 5B+

On first boot the ROCK image's `bdx-rock-prepare-data` grows the system partition and creates the
`DATA` partition (`/data`: duck's home, Wi-Fi, SSH host keys, the Caddy CA) after it. Writing a new
image replaces the partition table. So when the target already holds a BDX install, the flasher:

1. records the old system-partition size and the `DATA` partition (start, end, type, PARTUUID),
2. writes and verifies the image,
3. puts the old system-partition size and the identical `DATA` entry back, then runs `e2fsck` and
   `resize2fs` on the new system filesystem.

On first boot `bdx-rock-prepare-data` then finds `DATA` and leaves it alone, and `bdx-data-init`
mounts the existing filesystem. A layout it does not recognise (for example, extra partitions) is
refused before anything is written. You can still choose a clean install.

With **Clean install** (or `--erase-all`), `DATA` is dropped, and on first boot the board recreates
`DATA`, wipes its signatures, and `bdx-data-init` formats it fresh. This is not a secure erase.

### Maskrom mode (onboard eMMC over USB)

On the ROCK 5B+ page, choose **Board in Maskrom mode** under **Where to flash**. Then:

1. Install the tool: `sudo apt install rkdeveloptool`.
2. Remove the microSD card. Connect the board's USB OTG port to this computer (see Radxa's documentation
   for which port it is). Hold **MASKROM** while powering on, and release it after 2–3 seconds.
3. The page shows `✔ … detected` (USB `2207:350b`). Click **Flash** and confirm.

The flasher then:

1. checks the image against its `.sha256` file,
2. downloads Radxa's `rk3588_spl_loader_v1.15.113.bin` into `~/.cache/bdx-flasher`, accepting it only if
   its SHA-256 matches the one pinned in `boards.go` (`--loader FILE` overrides this),
3. decompresses the `.img.xz` into the cache, which needs about 9.6 GB free; the copy is deleted after a
   successful flash.

Then, as root, it:

1. runs `rkdeveloptool boot <loader>`, which skips the loader step if the board is already in Loader mode,
2. reads the storage size and refuses if there is no eMMC big enough (for example, when only SPI flash is
   fitted),
3. runs `rkdeveloptool write 0 <image>`,
4. reads back and compares the boot area (first 16 MiB) and 13 regions of 4 MiB spread over the image,
5. runs `rkdeveloptool reset`.

A full read-back over the board's USB 2.0 rockusb link would take as long as the write, so the eMMC is
only spot-checked, not fully verified.

A maskrom flash is **always a clean install**. The whole eMMC is rewritten, and on first boot
`bdx-rock-prepare-data` creates DATA again and `bdx-data-init` formats it. The image contains its own
idbloader and U-Boot (sectors 64 and 16384), so the board boots from eMMC once the microSD card is removed.
If the SPI flash contains a bootloader configured to prefer another device, that can still change what
boots first.

NVMe SSDs in the M.2 slot cannot be flashed through Maskrom. Use a USB adapter.

## Terminal mode

```bash
bdx-flasher --list-devices
bdx-flasher --cli                                    # asks for the board and drive
bdx-flasher --cli --board rock5bplus --device /dev/sdb
bdx-flasher --cli --board rock5bplus --image ~/Downloads/bdx-system-rock5bplus-1.3.1-10-armbian-trixie.img.xz \
            --device /dev/mmcblk0 --erase-all
bdx-flasher --cli --board rock5bplus --target maskrom  # onboard eMMC over USB
bdx-flasher --cli --board nano                       # runs bdx-orin-flasher --cli
```

The CLI asks you to type `flash <disk>`, or `erase and flash <disk>` for a clean install (`erase and flash emmc` for maskrom). `--yes`
skips that step. The write itself runs through `sudo`. These options are passed through to the
BDX Orin Flasher for Jetson boards: `--manifest-url`, `--local-bundle`, `--local-sha256`,
`--local-env`, `--force-download`, `--download-only`, `--cache-root`, `--work-root` and
`--erase-all`.

## How it works

The GUI runs as your user. The write is done by the same binary re-executed as root:
`pkexec bdx-flasher --helper write --image … --device …`. The privileged helper:

- refuses system, read-only and too-small disks (at least the image size + 2 GB),
- unmounts the target and opens it with `O_EXCL`,
- streams `xz -dc` into it with regular flushes,
- reads the whole written range back, bypassing the page cache, and compares the SHA-256,
- moves the backup GPT to the end of the media (`sgdisk -e`) and restores DATA when you are keeping it.

It reports progress to the GUI as tagged lines on stdout.

## Build

```bash
./build.sh      # -> dist/bdx-flasher
./package.sh    # -> dist/deb/bdx-flasher_1.0.0-1_amd64.deb
sudo apt install ./dist/deb/bdx-flasher_1.0.0-1_amd64.deb
```

`build.sh` uses the system Go and `libgl1-mesa-dev xorg-dev` headers when they are installed.
Otherwise it uses the user-local toolchain in `../work/toolchain`, which holds Go 1.26.2 and headers
extracted from the Ubuntu `-dev` packages.

## Tests

```bash
sudo ./tests/loop-test.sh   # writes the ROCK image to a 14 GB loop device 3 times
```

The test checks a clean flash, a simulated first boot (rootfs grown to 10 GiB and DATA created),
a reflash that keeps DATA (same PARTUUID, filesystem UUID, contents and grown rootfs), and a clean
reflash.

`RENDER_DIR=/some/dir go test -run TestRender .` renders the GUI offscreen to PNG files.
