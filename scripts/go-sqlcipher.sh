#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
prefix="$root/target/sqlcipher"
if [[ "${CGO_ENABLED:-1}" != 1 ]]; then
  echo 'SQLCipher requires CGO_ENABLED=1' >&2
  exit 1
fi
if [[ "${GOOS:-$(go env GOHOSTOS)}" != "$(go env GOHOSTOS)" || "${GOARCH:-$(go env GOHOSTARCH)}" != "$(go env GOHOSTARCH)" ]]; then
  echo 'The SQLCipher build supports native targets only; build on the target platform' >&2
  exit 1
fi
[[ -f "$prefix/lib/libsqlite3.a" ]] || { echo 'Run make native to build SQLCipher first' >&2; exit 1; }
# Static crypto: prefer OPENSSL_STATIC_LIB, then the pinned build from
# scripts/build-openssl.sh, and fall back to the system OpenSSL via pkg-config.
crypto_lib=""
if [[ -n "${OPENSSL_STATIC_LIB:-}" ]]; then
  crypto_lib="$OPENSSL_STATIC_LIB"
elif [[ -f "$root/target/openssl/lib/libcrypto.a" ]]; then
  crypto_lib="$root/target/openssl/lib/libcrypto.a"
fi
if [[ -n "$crypto_lib" ]]; then
  [[ -f "$crypto_lib" ]] || { echo "Static OpenSSL libcrypto.a not found at $crypto_lib" >&2; exit 1; }
  crypto_flags="$crypto_lib"
else
  crypto_lib="$(pkg-config --variable=libdir libcrypto)/libcrypto.a"
  [[ -f "$crypto_lib" ]] || { echo 'Static OpenSSL libcrypto.a is required' >&2; exit 1; }
  crypto_flags=$(pkg-config --libs --static libcrypto)
  crypto_flags=${crypto_flags//-lcrypto/$crypto_lib}
fi

# USE_LIBSQLITE3 disables mattn's plaintext amalgamation. Do NOT use the
# libsqlite3 Go build tag: it adds -lsqlite3 and may select the system SQLite.
# Explicit archives keep SQLCipher and OpenSSL out of runtime shared libraries.
export CGO_ENABLED=1
# Go's build cache hashes CGO_CFLAGS but not the archives referenced by
# CGO_LDFLAGS, so a rebuilt SQLCipher/OpenSSL would otherwise reuse a stale link.
# Fold a fingerprint of the linked archives into CGO_CFLAGS to invalidate it.
archive_fingerprint=$(cksum "$prefix/lib/libsqlite3.a" "$crypto_lib" | awk '{printf "%s_%s_", $1, $2}')
export CGO_CFLAGS="${CGO_CFLAGS:-} -DUSE_LIBSQLITE3 -DKAGUYA_STATIC_ARCHIVES=$archive_fingerprint -I$prefix/include"
# SQLCipher's SQLite math functions require libm after the static archive.
export CGO_LDFLAGS="${CGO_LDFLAGS:-} $prefix/lib/libsqlite3.a $crypto_flags -lm"
# Go records CGO_LDFLAGS in each cgo package, so the final link repeats these
# archives. Apple ld safely ignores them; silence only its duplicate warning.
if [[ "$(go env GOHOSTOS)" == darwin ]]; then
  export CGO_LDFLAGS="$CGO_LDFLAGS -Wl,-no_warn_duplicate_libraries"
  # Keep every link (including test binaries) on the macOS support floor. Without
  # this clang falls back to the SDK default and warns about the pinned C deps.
  export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-14.0}"
  export CGO_CFLAGS="$CGO_CFLAGS -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
  export CGO_LDFLAGS="$CGO_LDFLAGS -mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET"
fi
exec go "$@"
