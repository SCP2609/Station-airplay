#!/usr/bin/env python3
"""Build the same multi-model .yap on Windows and Linux (Python 3.8+)."""
import gzip
import hashlib
import pathlib
import re
import sys
import tarfile
import io

root = pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0, str(root / 'scripts'))
from runtime_slim import RUNTIME_BINARIES, slim_bytes, upx_version  # noqa: E402
version = re.search(r'^version = "([^"]+)"', (root / 'plugin.toml').read_text(), re.M)[1]
required = ['bin/station-airplay', 'bin/avahi-namespace', 'bin/shairport-sync-ap2',
            'bin/shairport-sync-ap2-aarch64', 'bin/nqptp', 'bin/avahi-daemon',
            'bin/dbus-daemon', 'bin/dbus-send',
            'lib/libdbus-1.so.3', 'lib/aarch64/libplist-2.0.so.3']
for name in required:
    if not (root / name).is_file():
        raise SystemExit('Missing build input: ' + name)
slim = '--slim-runtime' in sys.argv[1:]
upx = None
if slim:
    try:
        upx = upx_version()
    except FileNotFoundError:
        raise SystemExit('--slim-runtime needs upx on PATH')
    print('UPX: ' + upx)
out = root / 'dist' / ('station-airplay-' + version + '.yap')
out.parent.mkdir(exist_ok=True)
with out.open('wb') as dst, gzip.GzipFile(fileobj=dst, mode='wb', filename='', mtime=0) as gz, tarfile.open(fileobj=gz, mode='w') as archive:
    # ponytail: ship runtime and notices; sources/build/docs stay in the repository.
    for name in ['plugin.toml', 'bin', 'lib', 'config', 'LICENSES', 'third_party/licenses']:
        path = root / name
        for file in ([path] if path.is_file() else sorted(path.rglob('*'))):
            if not file.is_file() or '__pycache__' in file.parts:
                continue
            rel = file.relative_to(root).as_posix()
            data = file.read_bytes()
            if slim and rel.startswith('bin/') and rel[4:] in RUNTIME_BINARIES:
                data, record = slim_bytes(file.name, data)
                print('UPX %-20s %8d -> %8d' % (rel[4:], record['before'], record['after']))
            info = archive.gettarinfo(str(file), rel)
            info.size = len(data)
            info.mtime = 0
            info.uid = info.gid = 0
            info.uname = info.gname = ''
            info.mode = 0o755 if data[:4] == b'\x7fELF' or file.suffix == '.sh' else 0o644
            archive.addfile(info, io.BytesIO(data))
print(str(out))
print('SHA256 ' + hashlib.sha256(out.read_bytes()).hexdigest())
