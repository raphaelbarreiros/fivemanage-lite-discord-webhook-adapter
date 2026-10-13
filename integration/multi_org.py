#!/usr/bin/env python3
"""Real Lite organization isolation through one adapter and one persistent queue."""
import json
import os
import secrets
import base64
from e2e import Adapter
from support import ROOT, Lite, local_config, compose, request, wait_for


def private_file(name, value):
    path=ROOT/'.local/secrets'/name
    if path.exists(): path.chmod(0o600)
    path.write_text(value+'\n');path.chmod(0o444)
    return str(path)


def main():
    os.umask(0o077)
    alpha,beta=Lite(),Lite()
    cfg=local_config(ROOT/f'.local/multi-org-queue-{os.getpid()}')
    cfg['sources']=[]
    incoming={}
    for name,org in [('alpha',alpha),('beta',beta)]:
        incoming[name]=secrets.token_hex(32)
        cfg['sources'].append({'id':name,'secret_file':private_file('multi_'+name+'_source',incoming[name]),'lite_token_file':private_file('multi_'+name+'_lite',org.token),'dataset':'synthetic','resource':name})
    adapter=Adapter('multi-org-process',cfg)
    payloads={}
    try:
        compose('stop','lite')
        adapter.start()
        for name,index in [('alpha',1),('beta',1),('alpha',2)]:
            marker=f'SYNTHETIC {name} {index}'
            # Client-supplied organization/destination/token values must not override routing.
            body=json.dumps({'content':marker,'organization_id':beta.org if name=='alpha' else alpha.org,'dataset':'untrusted','lite_token_file':'untrusted','resource':'untrusted'}).encode()
            payloads[marker]=body
            status,_,_=request(18081,'POST','/api/webhooks/'+name+'/'+incoming[name],body,{'Content-Type':'application/json'})
            assert status==204
        wait_for(lambda:any(row[4]>0 for row in adapter.pending()),label='durable failed attempt')
        before={row[0] for row in adapter.pending()}
        assert len(before)==3
        adapter.stop(kill=True)
        compose('start','--wait','lite')
        adapter.start()
        wait_for(adapter.drained,seconds=30,label='multi-organization drain after restart')
        ids=set()
        for name,org,expected in [('alpha',alpha,2),('beta',beta,1)]:
            rows=org.logs()
            assert len(rows)==expected,(name,len(rows))
            for row in rows:
                m=row['Metadata']
                assert m['adapter_source']==name and m['_resource']==name
                original=base64.b64decode(m['discord_payload_b64'])
                marker=json.loads(original)['content']
                assert marker.startswith('SYNTHETIC '+name+' ') and original==payloads[marker]
                ids.add(m['adapter_event_id'])
        assert ids==before
        for source in cfg['sources']:
            for key in ('secret_file','lite_token_file'):
                assert open(source[key]).read().strip().encode() not in adapter.log.read_bytes()
        evidence={'organizations':2,'adapter_instances':1,'sqlite_queues':1,'dataset_name_in_both':'synthetic','alpha_logs':2,'beta_logs':1,'payload_routing_override_rejected':True,'restart_preserved_ids':True,'roundtrip_exact':True,'credential_log_scan':'clean'}
        (ROOT/'artifacts').mkdir(exist_ok=True)
        (ROOT/'artifacts/multi-org.json').write_text(json.dumps(evidence,indent=2)+'\n')
        print(json.dumps(evidence,indent=2))
    finally:
        adapter.stop()
        compose('start','--wait','lite')

if __name__=='__main__': main()
