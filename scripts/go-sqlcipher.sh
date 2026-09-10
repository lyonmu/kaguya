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
crypto_lib="$(pkg-config --variable=libdir libcrypto)/libcrypto.a"
[[ -f "$crypto_lib" ]] || { echo 'Static OpenSSL libcrypto.a is required' >&2; exit 1; }
crypto_flags=$(pkg-config --libs --static libcrypto)
crypto_flags=${crypto_flags//-lcrypto/$crypto_lib}

# USE_LIBSQLITE3 disables mattn's plaintext amalgamation. Do NOT use the
# libsqlite3 Go build tag: it adds -lsqlite3 and may select the system SQLite.
# Explicit archives keep SQLCipher and OpenSSL out of runtime shared libraries.
export CGO_ENABLED=1
export CGO_CFLAGS="${CGO_CFLAGS:-} -DUSE_LIBSQLITE3 -I$prefix/include"
# SQLCipher's SQLite math functions require libm after the static archive.
export CGO_LDFLAGS="${CGO_LDFLAGS:-} $prefix/lib/libsqlite3.a $crypto_flags -lm"
exec go "$@"
