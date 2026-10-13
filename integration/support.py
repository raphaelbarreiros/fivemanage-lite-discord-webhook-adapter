"""Synthetic local-only integration helpers. Never print credentials or request URLs."""
import datetime
import email.utils
import http.client
import json
import os
from pathlib import Path
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
COMPOSE = ['docker', 'compose', '-p', 'fml-adapter-dev', '-f', str(ROOT/'compose.yaml')]

def compose(*args):
    result = subprocess.run(COMPOSE + list(args), cwd=ROOT, capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError('Disposable compose command failed: ' + ' '.join(args[:2]))
    return result.stdout

def request(port, method, path, body=None, headers=None, timeout=20):
    conn = http.client.HTTPConnection('127.0.0.1', port, timeout=timeout)
    headers = dict(headers or {})
    if isinstance(body, (dict, list)):
        body = json.dumps(body, ensure_ascii=False).encode()
        headers.setdefault('Content-Type', 'application/json')
    try:
        conn.request(method, path, body=body, headers=headers)
        response = conn.getresponse()
        return response.status, dict(response.getheaders()), response.read()
    finally:
        conn.close()

class Lite:
    def __init__(self):
        password = (ROOT/'.local/secrets/admin_password').read_text().strip()
        code, headers, _ = request(18080, 'POST', '/api/dash/auth/login', {'username':'admin','password':password})
        assert code == 303, ('login status',code)
        self.headers = {'Cookie': headers['Set-Cookie'].split(';',1)[0]}
        self.org = self.api('POST', '/api/dash/organization', {'name':'SYNTHETIC adapter test'})['id']
        self.dataset = self.api('POST', f'/api/dash/{self.org}/dataset', {'id':'synthetic-placeholder','name':'synthetic','description':'Synthetic local test only','retentionDays':1,'organizationId':self.org})['id']
        self.token = self.api('POST', f'/api/dash/{self.org}/token', {'identifier':'synthetic-test','type':'logs'})['token']
        tokenfile = ROOT/'.local/secrets/lite_token'
        if tokenfile.exists(): tokenfile.chmod(0o600)
        tokenfile.write_text(self.token+'\n');tokenfile.chmod(0o444)
    def api(self, method, path, body=None):
        code, _, raw = request(18080,method,path,body,self.headers)
        assert code == 200, ('Lite admin status', code)
        return json.loads(raw)['data']
    def submit(self, message='SYNTHETIC probe', dataset='synthetic', metadata=None):
        return request(18080,'POST','/api/logs',[{'level':'info','message':message,'resource':'synthetic','metadata':metadata or {}}],{'Authorization':self.token,'X-Fivemanage-Dataset':dataset})
    def logs(self):
        now = datetime.datetime.now(datetime.timezone.utc)
        return self.api('POST',f'/api/dash/{self.org}/dataset/{self.dataset}/logs',{'fromDate':email.utils.format_datetime(now-datetime.timedelta(days=2),usegmt=True),'toDate':email.utils.format_datetime(now+datetime.timedelta(days=1),usegmt=True)})['data']

def wait_for(predicate, seconds=30, label='condition'):
    until=time.monotonic()+seconds
    while time.monotonic()<until:
        if predicate(): return
        time.sleep(0.1)
    raise AssertionError('Timed out: '+label)

def local_config(data_dir, port=18081, admin=19090):
    return {'listen':f'127.0.0.1:{port}','admin_listen':f'127.0.0.1:{admin}','data_dir':str(data_dir),'lite_url':'http://127.0.0.1:18080','allow_http':True,'retry_min':'1s','retry_max':'4s','http_timeout':'3s','queue':{'maintenance_interval':'1s'},'sources':[{'id':'synthetic','secret_file':str(ROOT/'.local/secrets/source_token'),'lite_token_file':str(ROOT/'.local/secrets/lite_token'),'dataset':'synthetic','resource':'synthetic-server','rule':{'event_type':'inventory_change','fields':{'player_character_id':'Personagem','player_discord_id':'Discord','player_session_id':'Sessão','quantity':'Quantidade'}}}]}

def webhook_path():
    token=(ROOT/'.local/secrets/source_token').read_text().strip()
    return '/api/webhooks/synthetic/'+token
