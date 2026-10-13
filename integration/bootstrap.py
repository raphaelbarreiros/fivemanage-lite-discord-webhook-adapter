#!/usr/bin/env python3
"""Provision a synthetic organization/dataset/token through Lite's HTTP API."""
import json
from support import ROOT, Lite, local_config

def main():
    lite=Lite()
    cfg=local_config('/data')
    cfg['listen']=':8080';cfg['admin_listen']=':9090'
    cfg['lite_url']='http://lite:8080'
    cfg['sources'][0]['lite_token_file']='/run/secrets/lite_token'
    cfg['sources'][0]['secret_file']='/run/secrets/source_token'
    path=ROOT/'.local/compose.config.json'
    if path.exists(): path.chmod(0o600)
    path.write_text(json.dumps(cfg,indent=2)+'\n');path.chmod(0o444)
    (ROOT/'.local/bootstrap.json').write_text(json.dumps({'organization':lite.org,'dataset':lite.dataset}))
    print('Synthetic Lite dataset/token provisioned; Compose config written. Credentials not printed.')
if __name__=='__main__':main()
