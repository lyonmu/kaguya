#!/usr/bin/env bash
set -euo pipefail

# Build the official source, not a Go fork's bundled SQLite amalgamation.
version=4.19.0
sha256=7075f96cbabe45b4ecfc2e6b1745a625f856f695b0827a5506ce9ed85b906aa0
root=$(cd "$(dirname "$0")/.." && pwd)
prefix="$root/target/sqlcipher"
work="$root/target/sqlcipher-build"

for tool in curl tar make cc openssl; do
  command -v "$tool" >/dev/null || { echo "SQLCipher build requires $tool" >&2; exit 1; }
done
# Keep the C objects on the same macOS support floor as the application.
if [[ "$(uname -s)" == Darwin ]]; then
  export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-14.0}"
fi
# Static crypto selection mirrors scripts/go-sqlcipher.sh: an explicit
# OPENSSL_STATIC_LIB wins, then the pinned build from scripts/build-openssl.sh,
# and finally the system installation reported by pkg-config.
crypto_lib=${OPENSSL_STATIC_LIB:-}
if [[ -n "$crypto_lib" ]]; then
  [[ -f "$crypto_lib" ]] || { echo "OPENSSL_STATIC_LIB not found: $crypto_lib" >&2; exit 1; }
  crypto_prefix="$(cd "$(dirname "$crypto_lib")/.." && pwd)"
  crypto_version=$(awk '/OPENSSL_VERSION_STR/{gsub(/"/,"",$NF); print $NF}' "$crypto_prefix/include/openssl/opensslv.h" 2>/dev/null || true)
  crypto_cflags="-I$crypto_prefix/include"
  crypto_ldflags="$crypto_lib"
else
  command -v pkg-config >/dev/null || { echo 'SQLCipher build requires pkg-config' >&2; exit 1; }
  pinned_openssl="$root/target/openssl"
  if [[ -f "$pinned_openssl/lib/pkgconfig/libcrypto.pc" ]]; then
    export PKG_CONFIG_PATH="$pinned_openssl/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
  fi
  pkg-config --exists libcrypto || { echo 'SQLCipher build requires OpenSSL development files (pkg-config libcrypto)' >&2; exit 1; }
  crypto_lib="$(pkg-config --variable=libdir libcrypto)/libcrypto.a"
  [[ -f "$crypto_lib" ]] || { echo 'SQLCipher build requires static OpenSSL libcrypto.a' >&2; exit 1; }
  crypto_version="$(pkg-config --modversion libcrypto)"
  crypto_cflags="$(pkg-config --cflags libcrypto)"
  crypto_ldflags="$(pkg-config --libs libcrypto)"
fi

# FTS5 is off by default upstream; the application relies on it for full-text
# search, so enable it as an autosetup feature flag (adds -DSQLITE_ENABLE_FTS5).
configure_flags=(--enable-fts5)

# Rebuild when the engine, target host, C compiler, deployment target, crypto
# installation, or configure feature flags change.
stamp="$version $(uname -sm) ${CC:-cc} ${MACOSX_DEPLOYMENT_TARGET:-default} $crypto_version $crypto_lib ${configure_flags[*]}"
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
./configure --prefix="$prefix" --disable-shared --disable-tcl --with-tempstore=yes "${configure_flags[@]}" \
  CFLAGS="-O2 -DSQLITE_HAS_CODEC -DSQLITE_EXTRA_INIT=sqlcipher_extra_init -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown $crypto_cflags" \
  LDFLAGS="$crypto_ldflags"
# Configure flags are not part of make's dependency graph, so drop objects from an
# earlier build to avoid installing a library compiled without the current flags.
rm -f *.o libsqlite3.a
make
make install
# Preserve upstream licensing alongside the build artifacts for redistributors.
cp LICENSE.md "$prefix/SQLCipher-LICENSE.md"
printf '%s\n' "$stamp" > "$prefix/build-stamp"
