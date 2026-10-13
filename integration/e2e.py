#!/usr/bin/env python3
"""Exercise real adapter processes and the disposable patched Lite. Synthetic data only."""
import base64
import json
import os
import signal
import sqlite3
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from support import ROOT, Lite, compose, request, wait_for, local_config, webhook_path

class Adapter:
    def __init__(self, name, cfg):
        self.path=ROOT/'.local'/name
        self.path.mkdir(parents=True,exist_ok=True)
        self.cfg=cfg
        self.config=self.path/'config.json'
        self.config.write_text(json.dumps(cfg));self.config.chmod(0o600)
        self.log=self.path/'adapter.log'
        self.process=None
    def start(self):
        self.output=self.log.open('ab')
        self.process=subprocess.Popen([str(ROOT/'.local/adapter'),'-config',str(self.config)],cwd=ROOT,stdout=self.output,stderr=subprocess.STDOUT)
        def healthy():
            if self.process.poll() is not None: raise AssertionError('adapter exited before healthcheck; inspect redacted local log')
            try:return request(19090,'GET','/healthz')[0]==200
            except OSError:return False
        wait_for(healthy,label='adapter healthy')
    def stop(self, kill=False):
        if self.process and self.process.poll() is None:
            self.process.send_signal(signal.SIGKILL if kill else signal.SIGTERM)
            self.process.wait(timeout=25)
        if hasattr(self,'output'):self.output.close()
    def pending(self):
        with sqlite3.connect(str(self.cfg['data_dir'])+'/pending.db',timeout=2) as db:
            return db.execute('SELECT id,source,payload,received_ms,attempts,next_ms FROM pending').fetchall()
    def post(self,raw):return request(18081,'POST',webhook_path(),raw,{'Content-Type':'application/json'})[0]
    def drained(self):return not self.pending()

class GateProxy(BaseHTTPRequestHandler):
    mode='pass'
    arrived=threading.Event()
    release=threading.Event()
    def do_POST(self):
        raw=self.rfile.read(int(self.headers['Content-Length']))
        mode=type(self).mode
        if mode=='before':
            type(self).arrived.set();type(self).release.wait(15)
            self.close_connection=True
            return
        result=request(18080,'POST','/api/logs',raw,{'Content-Type':'application/json','Authorization':self.headers['Authorization'],'X-Fivemanage-Dataset':self.headers['X-Fivemanage-Dataset']})
        if mode=='after':
            assert result[0]==200
            type(self).arrived.set();type(self).release.wait(15)
        try:
            self.send_response(result[0])
            if 'X-Fivemanage-Ingest' in result[1]:self.send_header('X-Fivemanage-Ingest',result[1]['X-Fivemanage-Ingest'])
            self.end_headers();self.wfile.write(result[2])
        except (BrokenPipeError,ConnectionResetError):pass
    def log_message(self,*args):pass


def main():
    os.umask(0o077)
    lite=Lite();fixture=(ROOT/'testdata/discord.json').read_bytes()
    cfg=local_config(ROOT/f'.local/e2e-queue-{os.getpid()}')
    adapter=Adapter('e2e-process',cfg)
    evidence={}
    try:
        normal=lite.submit('SYNTHETIC patched normal')
        assert normal[0]==200 and normal[1].get('X-Fivemanage-Ingest')=='clickhouse-sync-v1'
        invalid=lite.submit(dataset='invalid-synthetic')
        assert invalid[0]==400 and 'X-Fivemanage-Ingest' not in invalid[1]
        evidence['patched_normal_http']=normal[0];evidence['patched_invalid_dataset_http']=invalid[0]
        adapter.start()
        assert adapter.post(fixture)==204
        wait_for(adapter.drained,label='fixture delivered')
        logs=lite.logs();event=next(row for row in logs if 'discord_payload_b64' in row['Metadata'])
        metadata=event['Metadata']
        assert base64.b64decode(metadata['discord_payload_b64'])==fixture
        assert metadata['adapter_source']=='synthetic' and metadata['_resource']=='synthetic-server'
        assert metadata['player_character_id']=='9007199254740993123' and metadata['player_discord_id']=='18446744073709551615'
        assert metadata['quantity']=='12' and metadata['player_session_id']=='42'
        assert 'Quantidade: 12 Quantidade: 99' in event['Body']
        assert metadata['discord_embed_0_timestamp']=='2026-01-02T03:04:05-03:00'
        evidence['payload_roundtrip_bytes']=len(fixture)
        evidence['source_resource_rule_precision_order_verified']=True
        compose('stop','clickhouse')
        try:
            storage=lite.submit('SYNTHETIC storage unavailable')
            assert storage[0]==503 and 'X-Fivemanage-Ingest' not in storage[1]
            evidence['patched_storage_down_http']=storage[0]
            assert adapter.post(b'{"content":"SYNTHETIC storage retry"}')==204
            wait_for(lambda:bool(adapter.pending()) and adapter.pending()[0][4]>=1,label='persisted retry')
            pending_id=adapter.pending()[0][0]
            time.sleep(1.3)
            attempts=adapter.pending()[0][4]
            assert attempts<=3, 'retry loop too aggressive'
            adapter.stop(kill=True)
            adapter.start()
            assert adapter.pending()[0][0]==pending_id
        finally:compose('start','--wait','clickhouse')
        wait_for(adapter.drained,seconds=20,label='storage recovered')
        assert any(r['Metadata'].get('adapter_event_id')==pending_id for r in lite.logs())
        evidence['storage_failure_restart_recovery']=True
        compose('stop','lite')
        try:
            assert adapter.post(b'{"content":"SYNTHETIC Lite offline"}')==204
            wait_for(lambda:bool(adapter.pending()) and adapter.pending()[0][4]>0,label='Lite offline pending')
            adapter.stop(kill=True)
        finally:compose('start','--wait','lite')
        adapter.start();wait_for(adapter.drained,seconds=20,label='Lite recovered')
        evidence['lite_unavailable_recovery']=True
        adapter.stop()
        proxy=ThreadingHTTPServer(('127.0.0.1',18082),GateProxy)
        thread=threading.Thread(target=proxy.serve_forever,daemon=True);thread.start()
        cfg['lite_url']='http://127.0.0.1:18082'
        adapter=Adapter('e2e-process',cfg)
        try:
            for mode in ('before','after'):
                GateProxy.mode=mode;GateProxy.arrived.clear();GateProxy.release.clear()
                adapter.start()
                assert adapter.post(json.dumps({'content':'SYNTHETIC crash '+mode}).encode())==204
                assert GateProxy.arrived.wait(10),'proxy did not reach fault window'
                rows=adapter.pending();assert len(rows)==1
                event_id=rows[0][0]
                adapter.stop(kill=True)
                GateProxy.mode='pass';GateProxy.release.set()
                adapter.start();wait_for(adapter.drained,seconds=20,label='fault recovery '+mode)
                remote=[r for r in lite.logs() if r['Metadata'].get('adapter_event_id')==event_id]
                assert len(remote)==(2 if mode=='after' else 1),(mode,len(remote))
                evidence['kill_'+mode+'_send_remote_copies']=len(remote)
                adapter.stop()
        finally:GateProxy.release.set();proxy.shutdown();proxy.server_close();thread.join()
        cfg['lite_url']='http://127.0.0.1:18080';adapter=Adapter('e2e-process',cfg);adapter.start()
        assert adapter.post(b'{"content":"SYNTHETIC shutdown"}')==204
        adapter.stop();adapter.start();wait_for(adapter.drained,label='graceful shutdown recovery');adapter.stop()
        evidence['graceful_shutdown_preserves_accepted']=True
        # Exact secret values and original payload must never appear in process output.
        output=adapter.log.read_bytes()
        for file in (ROOT/'.local/secrets').iterdir():
            assert file.read_bytes().strip() not in output,'secret leaked'
        assert fixture not in output and b'/api/webhooks/' not in output
        evidence['adapter_log_secret_scan']='clean'
        (ROOT/'artifacts').mkdir(exist_ok=True)
        (ROOT/'artifacts/e2e.json').write_text(json.dumps(evidence,indent=2)+'\n')
        print(json.dumps(evidence,indent=2))
    finally:adapter.stop()
if __name__=='__main__':main()
