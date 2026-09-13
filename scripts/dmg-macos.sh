#!/usr/bin/env bash
# 把 target/Kaguya.app 打包成可拖拽安装的 DMG（target/Kaguya-<version>.dmg）。
# 只组装与校验，不做签名；签名与公证见 scripts/sign-macos.sh。
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

app_name="Kaguya"
bundle="$root/target/${app_name}.app"
app_item="${app_name}.app"
version=$(cat VERSION)
dmg="$root/target/${app_name}-${version}.dmg"
volume_name="$app_name"
staging="$root/target/dmg-staging"
rw_image="$root/target/${app_name}-rw.dmg"
device=''

if [[ "$(go env GOHOSTOS)" != darwin ]]; then
  echo 'dmg-macos requires macOS' >&2
  exit 1
fi
if [[ ! -d "$bundle" ]]; then
  echo "missing $bundle; run make package-macos first" >&2
  exit 1
fi

cleanup() {
  if [[ -n "$device" ]]; then
    hdiutil detach "$device" >/dev/null 2>&1 || \
      hdiutil detach "$device" -force >/dev/null 2>&1 || true
  fi
  rm -rf "$staging" "$rw_image"
}
trap cleanup EXIT

# Finder 按卷名定位磁盘，先卸载同名旧卷，避免布局写进错误的卷。
if [[ -d "/Volumes/${volume_name}" ]]; then
  hdiutil detach "/Volumes/${volume_name}" >/dev/null 2>&1 || \
    hdiutil detach "/Volumes/${volume_name}" -force >/dev/null 2>&1 || {
      echo "cannot detach the mounted /Volumes/${volume_name}; eject it and retry" >&2
      exit 1
    }
fi

# 1) 暂存目录：应用包加指向 /Applications 的链接，构成拖拽安装布局。
rm -rf "$staging"
mkdir -p "$staging"
ditto "$bundle" "$staging/$app_item"
ln -s /Applications "$staging/Applications"

# 2) 读写镜像：图标位置由 Finder 写入 .DS_Store，必须先落到可写卷。
rm -f "$rw_image"
hdiutil create -volname "$volume_name" -fs HFS+ -srcfolder "$staging" \
  -format UDRW -ov "$rw_image" >/dev/null

# 挂载到 /Volumes/Kaguya：Finder 按卷名定位磁盘，自定义挂载点会改成目录名。
attach_output=$(hdiutil attach "$rw_image" -readwrite -noverify -noautoopen)
device=$(printf '%s\n' "$attach_output" | awk '/^\/dev\/disk/{print $1; exit}')
if [[ -z "$device" ]]; then
  echo 'failed to mount the read-write image' >&2
  exit 1
fi

# 3) Finder 写入窗口大小与图标位置；没有图形会话时保留默认布局并给出提示。
if ! osascript - "$volume_name" "$app_item" <<'APPLESCRIPT'
on run argv
  set volumeName to item 1 of argv
  set appItem to item 2 of argv
  with timeout of 60 seconds
    tell application "Finder"
      tell disk volumeName
        open
        set current view of container window to icon view
        set toolbar visible of container window to false
        set statusbar visible of container window to false
        set the bounds of container window to {400, 160, 1060, 580}
        set viewOptions to the icon view options of container window
        set arrangement of viewOptions to not arranged
        set icon size of viewOptions to 128
        set text size of viewOptions to 13
        set position of item appItem of container window to {165, 190}
        set position of item "Applications" of container window to {495, 190}
        update without registering applications
        delay 1
        close
      end tell
    end tell
  end timeout
end run
APPLESCRIPT
then
  echo 'warning: Finder layout failed; keeping the default icon layout' >&2
fi

hdiutil detach "$device" >/dev/null
device=''

# 4) 压缩成只读 DMG 并校验。
rm -f "$dmg"
hdiutil convert "$rw_image" -format UDZO -imagekey zlib-level=9 -ov -o "$dmg" >/dev/null
hdiutil verify "$dmg" >/dev/null

echo "bundle:  $bundle"
echo "dmg:     $dmg"
echo "size:    $(du -h "$dmg" | cut -f1)"
echo 'run scripts/sign-macos.sh notarize to sign and notarize the disk image'
