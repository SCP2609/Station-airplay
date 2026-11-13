#!/usr/bin/env python3
"""Pack release runtime binaries with UPX without touching the repository.

The supervisor is packed by CI before packaging.  The receiver, the private
D-Bus/Avahi daemons and NQPTP are only packed for distribution archives, so the
checked-in `bin/` copies stay as built and keep their recorded SHA-256 values.
"""
import hashlib
import subprocess
import tempfile
from pathlib import Path

RUNTIME_BINARIES = (
    "shairport-sync-ap2",
    "dbus-daemon",
    "dbus-send",
    "avahi-daemon",
    "nqptp",
)
UPX_ARGS = ("--best", "--lzma")


def upx_version(executable="upx"):
    out = subprocess.check_output([executable, "--version"], text=True, timeout=30)
    return out.splitlines()[0].strip()


def slim_bytes(name, data, executable="upx"):
    """Return (packed_bytes, record). Raises if the input is not a packed ELF."""
    if data[:4] != b"\x7fELF":
        raise SystemExit("%s is not an ELF binary" % name)
    with tempfile.TemporaryDirectory(prefix="upx-") as directory:
        target = Path(directory) / name
        target.write_bytes(data)
        target.chmod(0o755)
        subprocess.run(
            [executable, *UPX_ARGS, "--quiet", str(target)],
            check=True,
            capture_output=True,
            timeout=600,
        )
        subprocess.run(
            [executable, "-t", str(target)],
            check=True,
            capture_output=True,
            timeout=120,
        )
        packed = target.read_bytes()
    record = {
        "file": name,
        "before": len(data),
        "after": len(packed),
        "sha256": hashlib.sha256(packed).hexdigest(),
    }
    return packed, record


def slim_file(path, executable="upx"):
    path = Path(path)
    packed, record = slim_bytes(path.name, path.read_bytes(), executable)
    path.write_bytes(packed)
    path.chmod(0o755)
    return record
