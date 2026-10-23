#!/bin/sh
# Sets up local voice note transcription for herdr-tg (macOS, Linux).
#
# Creates a Python virtualenv with onnx-asr and soundfile under the plugin
# state dir, then writes the `transcribe` program herdr-tg looks for in the
# plugin config dir. That program runs scripts/transcribe-parakeet.py from
# this checkout with the Parakeet TDT 0.6B v2 model; set PARAKEET_MODEL_DIR
# when the model is not where the Handy app keeps it. Safe to run again.
#
# Usage: sh scripts/setup-voice.sh
# Env:   PYTHON (default: newest python3.13/3.12/3.11/3.10 on PATH)
#        HERDR_PLUGIN_CONFIG_DIR, HERDR_PLUGIN_STATE_DIR (default: herdr's)

set -eu

PLUGIN_ID=permgps.telegram-agents
ROOT=$(cd "$(dirname "$0")/.." && pwd)
CONFIG_DIR=${HERDR_PLUGIN_CONFIG_DIR:-$HOME/.config/herdr/plugins/config/$PLUGIN_ID}
STATE_DIR=${HERDR_PLUGIN_STATE_DIR:-$HOME/.local/state/herdr/plugins/$PLUGIN_ID}
VENV=$STATE_DIR/voice/venv
PROGRAM=$CONFIG_DIR/transcribe

if [ -z "${PYTHON:-}" ]; then
	# onnxruntime publishes wheels for these versions; 3.14 can lag.
	for candidate in python3.13 python3.12 python3.11 python3.10; do
		if command -v "$candidate" >/dev/null 2>&1; then
			PYTHON=$candidate
			break
		fi
	done
fi
if [ -z "${PYTHON:-}" ]; then
	echo "setup-voice: no python3.10-3.13 on PATH; set PYTHON" >&2
	exit 1
fi

echo "setup-voice: virtualenv $VENV ($PYTHON)"
mkdir -p "$(dirname "$VENV")" "$CONFIG_DIR"
"$PYTHON" -m venv "$VENV"
"$VENV/bin/pip" install --quiet --disable-pip-version-check --upgrade 'onnx-asr[cpu]' soundfile

echo "setup-voice: writing $PROGRAM"
cat >"$PROGRAM" <<EOF
#!/bin/sh
# Written by $ROOT/scripts/setup-voice.sh: herdr-tg runs this with the path
# of a voice note and posts what it prints.
exec "$VENV/bin/python" -I "$ROOT/scripts/transcribe-parakeet.py" "\$@"
EOF
chmod 0755 "$PROGRAM"

echo "setup-voice: ok. Test with: $PROGRAM <audio file>"
