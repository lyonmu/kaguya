#!/usr/bin/env bash
set -euo pipefail

# Build a pinned OpenSSL LTS with the same deployment target as the application.
# The static libcrypto.a is what SQLCipher links against, so building it here
# keeps the release artifact reproducible instead of depending on whatever
# system or Homebrew OpenSSL happens to be installed.
version=3.5.8
sha256=a8f84a39918ec6415ce765d9b429d313ba97b8143169c172e734b9514464f5b2
root=$(cd "$(dirname "$0")/.." && pwd)
prefix="$root/target/openssl"
work="$root/target/openssl-build"
deployment_target=${MACOSX_DEPLOYMENT_TARGET:-14.0}

# Non-macOS targets keep using the system OpenSSL through pkg-config.
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'skipping the pinned OpenSSL build on non-macOS hosts' >&2
  exit 0
fi
# An explicit library keeps release environments in control of their own build.
if [[ -n "${OPENSSL_STATIC_LIB:-}" ]]; then
  echo "using OPENSSL_STATIC_LIB=$OPENSSL_STATIC_LIB instead of building OpenSSL" >&2
  exit 0
fi

for tool in curl tar make perl cc; do
  command -v "$tool" >/dev/null || { echo "OpenSSL build requires $tool" >&2; exit 1; }
done

case "$(uname -m)" in
  arm64) target=darwin64-arm64-cc ;;
  x86_64) target=darwin64-x86_64-cc ;;
  *) echo "unsupported macOS architecture: $(uname -m)" >&2; exit 1 ;;
esac

# Rebuild when the version, architecture, compiler or deployment target changes.
stamp="$version $target ${CC:-cc} min=$deployment_target"
if [[ -f "$prefix/build-stamp" && -f "$prefix/lib/libcrypto.a" && -f "$prefix/include/openssl/evp.h" ]] && [[ "$(<"$prefix/build-stamp")" == "$stamp" ]]; then
  exit 0
fi

mkdir -p "$work"
archive="$work/openssl-$version.tar.gz"
if [[ ! -f "$archive" ]]; then
  curl --fail --location --retry 3 "https://github.com/openssl/openssl/releases/download/openssl-$version/openssl-$version.tar.gz" -o "$archive.download"
  mv "$archive.download" "$archive"
fi
actual=$(shasum -a 256 "$archive" | awk '{print $1}')
if [[ "$actual" != "$sha256" ]]; then
  echo 'OpenSSL source checksum mismatch; refusing to build' >&2
  exit 1
fi

rm -rf "$work/openssl-$version"
tar -xzf "$archive" -C "$work"
cd "$work/openssl-$version"
export MACOSX_DEPLOYMENT_TARGET="$deployment_target"
./Configure "$target" no-shared \
  --prefix="$prefix" --openssldir="$prefix/ssl" \
  "-mmacosx-version-min=$deployment_target"
make -j"$(sysctl -n hw.ncpu)" build_sw
make install_sw
# Preserve upstream licensing alongside the build artifacts for redistributors.
cp LICENSE.txt "$prefix/OPENSSL-LICENSE.txt"
printf '%s\n' "$stamp" > "$prefix/build-stamp"
