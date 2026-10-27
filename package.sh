#!/usr/bin/env bash
# Build dist/deb/bdx-flasher_<version>-1_amd64.deb
set -euo pipefail
cd "$(dirname "$0")"
./build.sh
ver="$(./dist/bdx-flasher --version | awk '{print $NF}')"
pkg="dist/deb/bdx-flasher_${ver}-1_amd64"
rm -rf "$pkg"; mkdir -p "$pkg/DEBIAN"
install -Dm755 dist/bdx-flasher "$pkg/usr/bin/bdx-flasher"
install -Dm644 packaging/org.openbdx.bdx-flasher.desktop "$pkg/usr/share/applications/org.openbdx.bdx-flasher.desktop"
install -Dm644 packaging/org.openbdx.bdx-flasher.policy "$pkg/usr/share/polkit-1/actions/org.openbdx.bdx-flasher.policy"
install -Dm644 packaging/org.openbdx.bdx-flasher.metainfo.xml "$pkg/usr/share/metainfo/org.openbdx.bdx-flasher.metainfo.xml"
install -Dm644 README.md "$pkg/usr/share/doc/bdx-flasher/README.md"
for s in 64 128 256 512; do
  install -d "$pkg/usr/share/icons/hicolor/${s}x${s}/apps"
  python3 - "$s" "$pkg/usr/share/icons/hicolor/${s}x${s}/apps/org.openbdx.bdx-flasher.png" <<'PY' 2>/dev/null ||
import sys
from PIL import Image
s = int(sys.argv[1]); Image.open("assets/icon.png").resize((s, s), Image.LANCZOS).save(sys.argv[2])
PY
  install -m644 assets/icon.png "$pkg/usr/share/icons/hicolor/${s}x${s}/apps/org.openbdx.bdx-flasher.png"
done
install -Dm644 assets/icon.png "$pkg/usr/share/pixmaps/org.openbdx.bdx-flasher.png"
cat > "$pkg/DEBIAN/control" <<CTRL
Package: bdx-flasher
Version: ${ver}-1
Section: utils
Priority: optional
Architecture: amd64
Maintainer: OpenBDX <hello@openbdx.org>
Installed-Size: $(du -sk "$pkg/usr" | cut -f1)
Depends: libc6, libgl1, libx11-6, libxcursor1, libxfixes3, libxi6, libxinerama1, libxrandr2, libxxf86vm1, xz-utils, util-linux, fdisk, gdisk, e2fsprogs, udev, pkexec | policykit-1, sudo, hicolor-icon-theme
Recommends: bdx-orin-flasher, rkdeveloptool
Description: BDX Flasher (multi-board)
 Flashes OpenBDX firmware onto a robot main board chosen by the user:
 Radxa ROCK 5B+ (image written to microSD or a USB-attached NVMe/eMMC drive,
 keeping /data on reflash, or to the onboard eMMC in Maskrom mode), or NVIDIA
 Jetson Orin boards (handed to the BDX Orin Flasher).
CTRL
mkdir -p dist/deb
dpkg-deb --root-owner-group --build "$pkg" "$pkg.deb" >/dev/null
rm -rf "$pkg"
echo "built $pkg.deb"
