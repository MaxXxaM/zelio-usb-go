#!/usr/bin/env python3
"""Extract Zelio ASCII requests from a Windows USBPcap capture.

The SR2USB01 USB bulk OUT endpoint is 0x02. USBPcap records each USB packet
separately, while one Zelio request can span several packets.
"""

import argparse
from datetime import datetime, timezone
from pathlib import Path
import struct


def packets(path):
    capture = Path(path).read_bytes()
    offset = 0
    while offset + 12 <= len(capture):
        kind, length = struct.unpack_from("<II", capture, offset)
        if length < 12 or offset + length > len(capture):
            raise ValueError(f"invalid pcapng block at {offset}")
        if kind == 6:  # Enhanced Packet Block
            _, high, low, size, _ = struct.unpack_from("<IIIII", capture, offset + 8)
            packet = capture[offset + 28 : offset + 28 + size]
            if len(packet) >= 27:
                header_size = struct.unpack_from("<H", packet)[0]
                if 27 <= header_size <= len(packet):
                    yield (high << 32 | low) / 1_000_000, packet[21], packet[header_size:]
        offset += length


def requests(path):
    pending = bytearray()
    started = None
    for timestamp, endpoint, payload in packets(path):
        if endpoint != 0x02 or not payload:
            continue
        for byte in payload:
            if byte == ord(":"):
                pending.clear()
                started = timestamp
            elif not pending:
                continue
            pending.append(byte)
            if len(pending) > 1024:
                pending.clear()
            if not pending.endswith(b"\r\n"):
                continue
            frame = bytes(pending)
            pending.clear()
            try:
                raw = bytes.fromhex(frame[1:-2].decode("ascii"))
            except (ValueError, UnicodeDecodeError):
                continue
            if raw and sum(raw) % 256 == 0:
                yield started, raw


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("capture", help="USBPcap .pcapng file")
    parser.add_argument("--writes-only", action="store_true")
    parser.add_argument("--region", type=lambda value: int(value, 0), help="register region, e.g. 0x79")
    args = parser.parse_args()
    for timestamp, raw in requests(args.capture):
        if args.writes_only and (len(raw) < 7 or raw[1] != 0x10):
            continue
        if args.region is not None and (len(raw) < 5 or raw[4] != args.region):
            continue
        clock = datetime.fromtimestamp(timestamp, timezone.utc).isoformat(timespec="milliseconds")
        print(clock, raw.hex(" ").upper())


if __name__ == "__main__":
    main()
