#!/usr/bin/env bash
set -euo pipefail

# Build the official source, not a Go fork's bundled SQLite amalgamation.
version=4.19.0
sha256=7075f96cbabe45b4ecfc2e6b1745a625f856f695b0827a5506ce9ed85b906aa0
root=$(cd "$(dirname "$0")/.." && pwd)
prefix="$root/target/sqlcipher"
work="$root/target/sqlcipher-build"

for tool in curl tar make cc pkg-config openssl; do
  command -v "$tool" >/dev/null || { echo "SQLCipher build requires $tool" >&2; exit 1; }
done
pkg-config --exists libcrypto || { echo 'SQLCipher build requires OpenSSL development files (pkg-config libcrypto)' >&2; exit 1; }
crypto_lib="$(pkg-config --variable=libdir libcrypto)/libcrypto.a"
[[ -f "$crypto_lib" ]] || { echo 'SQLCipher build requires static OpenSSL libcrypto.a' >&2; exit 1; }

# Rebuild when the engine, target host, C compiler, or crypto installation changes.
stamp="$version $(uname -sm) ${CC:-cc} $(pkg-config --modversion libcrypto) $crypto_lib"
if [[ -f "$prefix/build-stamp" && -f "$prefix/lib/libsqlite3.a" && -f "$prefix/include/sqlite3.h" ]] && [[ "$(<"$prefix/build-stamp")" == "$stamp" ]]; then
  exit 0
fi
mkdir -p "$work"
archive="$work/sqlcipher-$version.tar.gz"
if [[ ! -f "$archive" ]]; then
  curl --fail --location --retry 3 "https://codeload.github.com/sqlcipher/sqlcipher/tar.gz/refs/tags/v$version" -o "$archive.download"
  mv "$archive.download" "$archive"
fi
actual=$(openssl dgst -sha256 "$archive")
if [[ "${actual##* }" != "$sha256" ]]; then
  echo 'SQLCipher source checksum mismatch; refusing to build' >&2
  exit 1
fi
tar -xzf "$archive" -C "$work"
cd "$work/sqlcipher-$version"
./configure --prefix="$prefix" --disable-shared --disable-tcl --with-tempstore=yes \
  CFLAGS="-O2 -DSQLITE_HAS_CODEC -DSQLITE_EXTRA_INIT=sqlcipher_extra_init -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown $(pkg-config --cflags libcrypto)" \
  LDFLAGS="$(pkg-config --libs libcrypto)"
make
make install
# Preserve upstream licensing alongside the build artifacts for redistributors.
cp LICENSE.md "$prefix/SQLCipher-LICENSE.md"
printf '%s\n' "$stamp" > "$prefix/build-stamp"
