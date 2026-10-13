#!/usr/bin/env python3
"""Verify the already bootstrapped container stack without changing credentials."""
import base64
import json
from support import ROOT, Lite, request, wait_for, webhook_path

def main():
    state=json.loads((ROOT/'.local/bootstrap.json').read_text())
    password=(ROOT/'.local/secrets/admin_password').read_text().strip()
    code,headers,_=request(18080,'POST','/api/dash/auth/login',{'username':'admin','password':password})
    assert code==303
    lite=Lite.__new__(Lite);lite.headers={'Cookie':headers['Set-Cookie'].split(';',1)[0]}
    lite.org=state['organization'];lite.dataset=state['dataset']
    before={r['Metadata'].get('adapter_event_id') for r in lite.logs()}
    fixture=(ROOT/'testdata/discord.json').read_bytes()
    code,_,_=request(18081,'POST',webhook_path(),fixture,{'Content-Type':'application/json'})
    assert code==204
    def delivered():
        return any(r['Metadata'].get('adapter_event_id') not in before and base64.b64decode(r['Metadata'].get('discord_payload_b64',''))==fixture for r in lite.logs())
    wait_for(delivered,label='container event queryable')
    wait_for(lambda: b'adapter_pending_messages 0\n' in request(19090,'GET','/metrics')[2],label='container queue drained')
    assert request(19090,'GET','/readyz')[0]==200
    status,_,raw=request(19090,'GET','/metrics');assert status==200
    assert b'adapter_pending_messages 0\n' in raw
    (ROOT/'artifacts/compose.metrics').write_bytes(raw)
    print('Container smoke: HTTP 204, real Lite API round-trip exact, queue empty, readiness 200.')
if __name__=='__main__':main()
