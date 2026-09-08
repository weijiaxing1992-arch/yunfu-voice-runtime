#!/bin/sh
set -eu
_voice_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$_voice_dir"
case "$(uname -s):$(uname -m)" in
  Linux:aarch64) ;;
  *) echo "此包需要 linux-arm64，请使用匹配平台的运行包。" >&2; exit 2 ;;
esac
if [ -f "$_voice_dir/lib/librustswitch_g722.so" ]; then
  RUSTSWITCH_G722_LIBRARY="$_voice_dir/lib/librustswitch_g722.so"
  RUSTSWITCH_OPUS_LIBRARY="$_voice_dir/lib/libopus.so"
  export RUSTSWITCH_G722_LIBRARY RUSTSWITCH_OPUS_LIBRARY
fi
exec ./bin/rustswitch -config ./config/local.json "$@"
