#!/usr/bin/env python3
"""Transcribe one audio file with NVIDIA Parakeet TDT 0.6B v2 (ONNX, int8).

Usage: transcribe-parakeet.py <audio file>

Prints the transcript to stdout and exits 0; prints the reason to stderr and
exits 1 on failure. herdr-tg runs this through the `transcribe` executable in
its config directory (see docs/behaviour.md, "Voice notes").

Reads anything libsndfile decodes, including the OGG/Opus Telegram uses for
voice notes, so no ffmpeg is needed. Needs `onnx-asr[cpu]` and `soundfile`.

The model directory defaults to the one the Handy app downloads on macOS; set
PARAKEET_MODEL_DIR to use another copy of the same onnx-asr model layout
(encoder-model.int8.onnx, decoder_joint-model.int8.onnx, nemo128.onnx,
vocab.txt).
"""

import os
import sys

DEFAULT_MODEL_DIR = os.path.expanduser(
    "~/Library/Application Support/com.pais.handy/models/parakeet-tdt-0.6b-v2-int8"
)


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: transcribe-parakeet.py <audio file>", file=sys.stderr)
        return 1
    model_dir = os.environ.get("PARAKEET_MODEL_DIR", DEFAULT_MODEL_DIR)
    if not os.path.isdir(model_dir):
        print(f"model directory not found: {model_dir}", file=sys.stderr)
        return 1
    try:
        import numpy as np
        import onnx_asr
        import soundfile as sf
    except ImportError as err:
        print(f"missing python package: {err.name}", file=sys.stderr)
        return 1

    try:
        audio, rate = sf.read(sys.argv[1], dtype="float32", always_2d=True)
    except Exception as err:  # libsndfile raises its own error types
        print(f"cannot read audio: {err}", file=sys.stderr)
        return 1
    audio = audio.mean(axis=1)
    if rate not in (8_000, 11_025, 16_000, 22_050, 24_000, 32_000, 44_100, 48_000):
        # Linear resample to 16 kHz; onnx-asr resamples the listed rates itself.
        positions = np.arange(0, len(audio), rate / 16_000)
        audio = np.interp(positions, np.arange(len(audio)), audio).astype(np.float32)
        rate = 16_000

    # CPU beats CoreML here: CoreML takes only part of the graph and adds seconds
    # of compile time on every run.
    model = onnx_asr.load_model(
        "nemo-parakeet-tdt-0.6b-v2", model_dir, quantization="int8", providers=["CPUExecutionProvider"]
    )
    text = model.recognize(audio, sample_rate=rate)
    print(" ".join(text.split()))
    return 0


if __name__ == "__main__":
    sys.exit(main())
