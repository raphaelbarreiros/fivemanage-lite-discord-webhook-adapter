#!/usr/bin/env python3
"""Scan source and produced text evidence against actual disposable secret values."""
from pathlib import Path
import re
from support import ROOT

EXCLUDED={'.git','.cache','.local','licenses','__pycache__'}

def main():
    values=[p.read_bytes().strip() for p in (ROOT/'.local/secrets').iterdir() if p.is_file()]
    files=[]
    for path in ROOT.rglob('*'):
        if path.is_file() and not EXCLUDED.intersection(path.relative_to(ROOT).parts):files.append(path)
    files+=list((ROOT/'.local').glob('**/*.log'))
    leaks=[]
    for path in files:
        data=path.read_bytes()
        if any(v and v in data for v in values):leaks.append(str(path.relative_to(ROOT)))
        if re.search(rb'https?://(?:discord(?:app)?\.com)/api/webhooks/[0-9]+/[A-Za-z0-9_-]{20,}',data):leaks.append(str(path.relative_to(ROOT)))
    if leaks:raise SystemExit('Potential secret leak in files (values suppressed): '+', '.join(sorted(set(leaks))))
    print(f'Exact disposable-secret and Discord URL scan: clean; {len(files)} source/evidence/log files checked. Not a guarantee against unknown secrets.')
if __name__=='__main__':main()
