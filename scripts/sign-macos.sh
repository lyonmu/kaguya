#!/usr/bin/env bash
# Developer ID 签名、公证与验证。
#
#   scripts/sign-macos.sh sign      使用 CODESIGN_IDENTITY 签名并验证
#   scripts/sign-macos.sh notarize  签名并公证应用包与 DMG，staple 后做 Gatekeeper 验证
#
# 身份与 keychain profile 属于发行环境输入；缺失时明确失败，不生成 ad-hoc 包。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

app_name="Kaguya"
bundle="target/${app_name}.app"
zip="target/${app_name}.zip"
version=$(cat VERSION)
dmg="target/${app_name}-${version}.dmg"
entitlements="build/darwin/entitlements.plist"

mode=${1:-}
if [[ "$mode" != sign && "$mode" != notarize ]]; then
  echo 'usage: scripts/sign-macos.sh sign|notarize' >&2
  exit 2
fi

if [[ "$(go env GOHOSTOS)" != darwin ]]; then
  echo 'sign-macos requires macOS' >&2
  exit 1
fi
if [[ ! -d "$bundle" ]]; then
  echo "missing $bundle; run make package-macos first" >&2
  exit 1
fi
if [[ -z "${CODESIGN_IDENTITY:-}" ]]; then
  echo 'CODESIGN_IDENTITY is required (for example "Developer ID Application: Name (TEAMID)")' >&2
  exit 1
fi

# 从内到外签名：本包没有嵌套代码，直接签主可执行文件再签 bundle。
codesign --force --options runtime --timestamp \
  --entitlements "$entitlements" \
  --sign "$CODESIGN_IDENTITY" "$bundle/Contents/MacOS/kaguya"
codesign --force --options runtime --timestamp \
  --entitlements "$entitlements" \
  --sign "$CODESIGN_IDENTITY" "$bundle"

codesign --verify --deep --strict --verbose=2 "$bundle"

if [[ "$mode" == sign ]]; then
  echo 'signed; run scripts/sign-macos.sh notarize to submit for notarization'
  exit 0
fi

if [[ -z "${NOTARY_PROFILE:-}" ]]; then
  echo 'NOTARY_PROFILE is required (a notarytool keychain profile)' >&2
  exit 1
fi

rm -f "$zip"
ditto -c -k --keepParent "$bundle" "$zip"
xcrun notarytool submit "$zip" --keychain-profile "$NOTARY_PROFILE" --wait
xcrun stapler staple "$bundle"
xcrun stapler validate "$bundle"

# 公证后重新打包，分发 ZIP 里包含已 staple 的 bundle。
rm -f "$zip"
ditto -c -k --keepParent "$bundle" "$zip"

# 分发 DMG 必须单独签名与公证：装入已 staple 的应用包后，再签名、提交、staple。
bash scripts/dmg-macos.sh
codesign --force --timestamp --sign "$CODESIGN_IDENTITY" "$dmg"
xcrun notarytool submit "$dmg" --keychain-profile "$NOTARY_PROFILE" --wait
xcrun stapler staple "$dmg"
xcrun stapler validate "$dmg"

spctl --assess --type execute --verbose=4 "$bundle"
codesign --verify --deep --strict --verbose=2 "$bundle"
spctl --assess --type open --context context:primary-signature --verbose=4 "$dmg"
echo "notarized app: $bundle"
echo "notarized zip: $zip"
echo "notarized dmg: $dmg"
