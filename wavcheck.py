#!/usr/bin/env python3
"""
wavcheck.py — main tones and level of a PCM WAV recording, no dependencies.

    python3 wavcheck.py rtp_from_A_1.wav rtp_from_B_1.wav

For each file prints the sample rate, overall RMS level (dBFS) and the
strongest tones (Hz, dB relative to the strongest one), computed with a DFT
over 1 s taken from the middle of the recording.
"""
import cmath
import math
import struct
import sys
import wave


def read_pcm16(path):
    with wave.open(path, "rb") as w:
        if w.getsampwidth() != 2:
            raise SystemExit(f"{path}: only 16-bit PCM WAV is supported "
                             f"(sample width {w.getsampwidth()} bytes)")
        rate, ch, n = w.getframerate(), w.getnchannels(), w.getnframes()
        raw = w.readframes(n)
    samples = struct.unpack("<%dh" % (len(raw) // 2), raw)
    if ch > 1:
        samples = samples[::ch]          # first channel only
    return rate, samples


def analyse(path, top=3):
    rate, s = read_pcm16(path)
    if not s:
        print(f"{path}: empty")
        return
    rms = math.sqrt(sum(x * x for x in s) / len(s))
    rms_db = 20 * math.log10(rms / 32768) if rms > 0 else -120.0

    # 1 s from the middle, Hann window
    n = min(rate, len(s))
    start = (len(s) - n) // 2
    seg = [s[start + i] * (0.5 - 0.5 * math.cos(2 * math.pi * i / n))
           for i in range(n)]

    # DFT magnitudes every 5 Hz from 50 Hz to Nyquist
    step = 5
    spectrum = []
    for f in range(50, rate // 2, step):
        w = cmath.exp(-2j * math.pi * f / rate)
        acc, z = 0j, 1 + 0j
        for x in seg:
            acc += x * z
            z *= w
        spectrum.append((abs(acc), f))

    # local maxima, strongest first, at least 50 Hz apart
    peaks = []
    for mag, f in sorted(spectrum, reverse=True):
        if all(abs(f - pf) > 50 for _, pf in peaks):
            peaks.append((mag, f))
        if len(peaks) == top:
            break

    ref = peaks[0][0] or 1
    tones = ", ".join(f"{f} Hz ({20 * math.log10(m / ref):+.0f} dB)"
                      for m, f in peaks)
    print(f"{path}: {rate} Hz, {len(s) / rate:.1f} s, RMS {rms_db:.1f} dBFS; "
          f"tones: {tones}")


if __name__ == "__main__":
    if len(sys.argv) < 2:
        raise SystemExit(__doc__)
    for p in sys.argv[1:]:
        analyse(p)
