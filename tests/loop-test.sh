#!/usr/bin/env bash
# End-to-end test of the ROCK image writer on a loop device (run as root):
#   1. clean flash, 2. simulate first boot (grow rootfs + create DATA),
#   3. reflash keeping /data, 4. clean reflash resetting /data.
set -euo pipefail

here="$(cd "$(dirname "$0")/.." && pwd)"
BIN="${BIN:-$here/dist/bdx-flasher}"
IMG="${1:-$(ls -t "$here"/../out/bdx-system-rock5bplus-*.img.xz | head -n1)}"
[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }

work="$(mktemp -d)"
loop=""
cleanup() {
  umount "$work/mnt" 2>/dev/null || true
  [[ -n "$loop" ]] && losetup -d "$loop" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

truncate -s 14G "$work/disk.raw"
loop="$(losetup -fP --show "$work/disk.raw")"
mkdir -p "$work/mnt"
export BDX_FLASHER_ALLOW_LOOP=1
ok() { printf '\e[32mPASS\e[0m %s\n' "$*"; }
die() { printf '\e[31mFAIL\e[0m %s\n' "$*"; exit 1; }
flash() { "$BIN" --helper write --image "$IMG" --device "$loop" "$@" | grep -v '^@@PROGRESS'; }
nparts() { sfdisk --json "$loop" | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["partitiontable"]["partitions"]))'; }

echo "== 1. clean flash of $(basename "$IMG") to $loop"
flash --erase-all
[[ "$(nparts)" == 1 ]] || die "expected one partition after a clean flash"
e2fsck -fn "${loop}p1" >/dev/null || die "rootfs does not fsck clean"
ok "clean flash"

echo "== 2. simulate first boot: rootfs to 10 GiB, DATA on the rest"
root_start="$(cat /sys/class/block/${loop##*/}p1/start)"
echo ",$((10 * 2097152))" | sfdisk --quiet -N 1 "$loop"
last="$(sgdisk -E "$loop" | tail -n1)"
sgdisk -n "0:$((root_start + 10 * 2097152)):$last" -c 0:DATA -t 0:8300 "$loop" >/dev/null
partx -u "$loop"; partx -a "$loop" 2>/dev/null || true; udevadm settle
resize2fs "${loop}p1" >/dev/null 2>&1
mkfs.ext4 -q -F -L DATA "${loop}p2"
mount "${loop}p2" "$work/mnt"; echo "duck was here" > "$work/mnt/sentinel"; umount "$work/mnt"
data_uuid="$(sfdisk --part-uuid "$loop" 2)"
fs_uuid="$(blkid -s UUID -o value "${loop}p2")"
ok "DATA created (partuuid $data_uuid)"

echo "== 3. reflash keeping /data"
flash
[[ "$(nparts)" == 2 ]] || die "DATA partition missing after keep-data flash"
[[ "$(sfdisk --part-uuid "$loop" 2)" == "$data_uuid" ]] || die "DATA PARTUUID changed"
[[ "$(sfdisk --part-label "$loop" 2)" == DATA ]] || die "DATA label lost"
[[ "$(blkid -s UUID -o value "${loop}p2")" == "$fs_uuid" ]] || die "DATA filesystem changed"
mount -o ro "${loop}p2" "$work/mnt"; grep -q "duck was here" "$work/mnt/sentinel" || die "sentinel lost"; umount "$work/mnt"
part_bytes=$(( $(cat /sys/class/block/${loop##*/}p1/size) * 512 ))
fs_bytes=$(dumpe2fs -h "${loop}p1" 2>/dev/null | awk -F: '/^Block count/{c=$2} /^Block size/{s=$2} END{print c*s}')
[[ "$part_bytes" == "$((10 << 30))" ]] || die "rootfs partition is $part_bytes bytes, expected 10 GiB"
[[ "$fs_bytes" == "$part_bytes" ]] || die "rootfs filesystem ($fs_bytes) not grown to the partition ($part_bytes)"
e2fsck -fn "${loop}p1" >/dev/null || die "rootfs does not fsck clean"
ok "keep-data reflash: DATA, its contents and the 10 GiB rootfs survived"

echo "== 4. clean reflash"
flash --erase-all
[[ "$(nparts)" == 1 ]] || die "DATA partition still present after a clean flash"
ok "clean reflash removed DATA"

echo "All tests passed."
