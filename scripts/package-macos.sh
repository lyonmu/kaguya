#!/usr/bin/env bash
# 把已编译的 kaguya 二进制组装成 macOS 应用包（target/Kaguya.app）。
# 只组装与校验，不做签名；签名与公证见 scripts/sign-macos.sh。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

name=$(basename "$root")
app_name="Kaguya"
bundle="target/${app_name}.app"
binary="target/${name}"
version=$(cat VERSION)
build_number=$(git rev-list --count HEAD 2>/dev/null || echo 0)
deployment_target=${MACOS_DEPLOYMENT_TARGET:-14.0}

if [[ "$(go env GOHOSTOS)" != darwin ]]; then
  echo 'package-macos requires macOS' >&2
  exit 1
fi
if [[ ! -x "$binary" ]]; then
  echo "missing $binary; run make backend first" >&2
  exit 1
fi
if [[ ! -f build/darwin/Info.plist ]]; then
  echo 'missing build/darwin/Info.plist' >&2
  exit 1
fi

rm -rf "$bundle"
mkdir -p "$bundle/Contents/MacOS" "$bundle/Contents/Resources"

cp "$binary" "$bundle/Contents/MacOS/kaguya"
cp build/darwin/Info.plist "$bundle/Contents/Info.plist"
cp LICENSE "$bundle/Contents/Resources/LICENSE"

/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString ${version}" "$bundle/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion ${build_number}" "$bundle/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :LSMinimumSystemVersion ${deployment_target}" "$bundle/Contents/Info.plist"

# 图标从仓库内原图生成；缺失 sips/iconutil 时保留无图标包而不是中断发布。
if command -v sips >/dev/null 2>&1 && command -v iconutil >/dev/null 2>&1 && [[ -f images/kaguya.png ]]; then
  iconset="target/AppIcon.iconset"
  rm -rf "$iconset"
  mkdir -p "$iconset"
  for size in 16 32 64 128 256 512; do
    sips -z "$size" "$size" images/kaguya.png --out "$iconset/icon_${size}x${size}.png" >/dev/null
    double=$((size * 2))
    sips -z "$double" "$double" images/kaguya.png --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
  done
  iconutil -c icns "$iconset" -o "$bundle/Contents/Resources/AppIcon.icns"
  rm -rf "$iconset"
  /usr/libexec/PlistBuddy -c "Add :CFBundleIconFile string AppIcon" "$bundle/Contents/Info.plist" 2>/dev/null || \
    /usr/libexec/PlistBuddy -c "Set :CFBundleIconFile AppIcon" "$bundle/Contents/Info.plist"
else
  echo 'skipping app icon: sips/iconutil or images/kaguya.png unavailable' >&2
fi

# 冒烟校验：架构、C 静态依赖与最低系统版本。
echo "bundle:  $bundle"
echo "arch:    $(lipo -archs "$bundle/Contents/MacOS/kaguya" 2>/dev/null || file -b "$bundle/Contents/MacOS/kaguya")"

# 链接的 C 静态库必须与部署目标一致，否则二进制声称支持的系统上可能调用更新的 API。
max_minos() { otool -l "$1" 2>/dev/null | awk '/minos/{print $2}' | sort -V | tail -1; }
is_newer() { [[ "$1" != "$2" ]] && [[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" == "$1" ]]; }
crypto_archive=${OPENSSL_STATIC_LIB:-}
if [[ -z "$crypto_archive" && -f "$root/target/openssl/lib/libcrypto.a" ]]; then
  crypto_archive="$root/target/openssl/lib/libcrypto.a"
fi
for archive in "$root/target/sqlcipher/lib/libsqlite3.a" "$crypto_archive"; do
  [[ -f "$archive" ]] || continue
  archive_minos=$(max_minos "$archive")
  echo "c-dep:   $(basename "$archive") minos=${archive_minos:-unknown}"
  if [[ -n "$archive_minos" ]] && is_newer "$archive_minos" "$deployment_target"; then
    echo "$archive was built for macOS $archive_minos; rebuild the C dependencies with MACOSX_DEPLOYMENT_TARGET=$deployment_target" >&2
    exit 1
  fi
done

minos=$(otool -l "$bundle/Contents/MacOS/kaguya" | awk '/LC_BUILD_VERSION/{f=1} f&&/minos/{print $2; exit}')
echo "target:  $minos (expected $deployment_target)"
if [[ -n "$minos" ]] && is_newer "$minos" "$deployment_target"; then
  echo 'Mach-O minimum system is newer than the deployment target; rebuild the C dependencies' >&2
  exit 1
fi
if otool -L "$bundle/Contents/MacOS/kaguya" | grep -E '/opt/homebrew|/usr/local/(opt|Cellar)' >/dev/null; then
  echo 'bundle links against a development-machine library' >&2
  exit 1
fi

echo 'package-macos finished; run scripts/sign-macos.sh to sign and notarize'
