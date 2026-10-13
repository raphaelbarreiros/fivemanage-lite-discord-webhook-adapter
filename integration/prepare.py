#!/usr/bin/env python3
"""Create ONLY disposable local secrets and fetch the pinned upstream source."""
import os
from pathlib import Path
import secrets
import subprocess

ROOT = Path(__file__).resolve().parents[1]
BASE = '74ba491368d88d2747d9013cfd3fc4bcd78d1e34'

def main():
    os.umask(0o077)
    directory = ROOT / '.local/secrets'
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    for name in ('postgres_password', 'clickhouse_password', 'admin_password', 'hmac_secret', 'source_token'):
        path = directory / name
        if not path.exists():
            with path.open('x') as f:
                f.write(secrets.token_hex(32) + '\n')
        # Parent is 0700; individual read-only mounts must be readable by UID 10001.
        path.chmod(0o444)
    source = ROOT / '.cache/lite'
    if not source.exists():
        subprocess.run(['git', 'clone', 'https://github.com/fivemanage/lite.git', str(source)], check=True)
        subprocess.run(['git', '-C', str(source), 'checkout', '--detach', BASE], check=True)
    actual = subprocess.check_output(['git', '-C', str(source), 'rev-parse', 'HEAD'], text=True).strip()
    if actual != BASE:
        raise SystemExit('Existing Lite checkout differs; preserved. Use a separate clean checkout at documented commit.')
    if subprocess.run(['git', '-C', str(source), 'diff', '--quiet', 'HEAD', '--']).returncode:
        raise SystemExit('Existing Lite source has tracked changes; preserved. A clean pinned base is required.')
    print('Local synthetic secrets ready (values not printed); Lite base verified.')

if __name__ == '__main__':
    main()
