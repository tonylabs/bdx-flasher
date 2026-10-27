#!/usr/bin/env bash
# Build dist/bdx-flasher.  Uses the system Go and X11/GL headers when present,
# otherwise the user-local toolchain in ../work/toolchain (see README).
set -euo pipefail
cd "$(dirname "$0")"
T="${BDX_TOOLCHAIN:-$(pwd)/../work/toolchain}"
if ! command -v go >/dev/null && [[ -x "$T/go/bin/go" ]]; then
  export PATH="$T/go/bin:$PATH" GOPATH="$T/gopath" GOCACHE="$T/gocache"
fi
if [[ ! -e /usr/include/GL/gl.h && -d "$T/sysroot" ]]; then
  export CGO_CFLAGS="-I$T/sysroot/usr/include" CGO_LDFLAGS="-L$T/sysroot/usr/lib/x86_64-linux-gnu" \
    PKG_CONFIG_PATH="$T/sysroot/usr/lib/x86_64-linux-gnu/pkgconfig:$T/sysroot/usr/share/pkgconfig"
fi
mkdir -p dist
go build -trimpath -ldflags "-s -w" -o dist/bdx-flasher .
echo "built dist/bdx-flasher ($(./dist/bdx-flasher --version))"
