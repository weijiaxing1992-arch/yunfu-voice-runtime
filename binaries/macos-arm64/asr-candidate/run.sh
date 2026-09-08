#!/bin/sh
set -eu
_voice_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$_voice_dir"
case "$(uname -s):$(uname -m)" in
  Darwin:arm64) ;;
  *) echo "此包需要 macos-arm64，请使用匹配平台的运行包。" >&2; exit 2 ;;
esac
if [ -f "$_voice_dir/lib/librustswitch_g722.dylib" ]; then
  RUSTSWITCH_G722_LIBRARY="$_voice_dir/lib/librustswitch_g722.dylib"
  RUSTSWITCH_OPUS_LIBRARY="$_voice_dir/lib/libopus.dylib"
  export RUSTSWITCH_G722_LIBRARY RUSTSWITCH_OPUS_LIBRARY
fi
exec ./bin/rustswitch -config ./config/local.json "$@"
