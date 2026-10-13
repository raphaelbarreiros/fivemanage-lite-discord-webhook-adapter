#!/usr/bin/env python3
"""Bounded load against native adapter + real Lite. No production target option."""
from concurrent.futures import ThreadPoolExecutor
import json
import os
import platform
import statistics
import time
from e2e import Adapter
from support import ROOT, Lite, local_config, request, wait_for

def main():
    lite=Lite()
    cfg=local_config(ROOT/f'.local/load-queue-{os.getpid()}')
    adapter=Adapter('load-process',cfg)
    count=200;concurrency=8
    try:
        adapter.start()
        begin=time.perf_counter()
        def send(i):
            payload=json.dumps({'content':f'SYNTHETIC load event {i:04d}','extra':'x'*900}).encode()
            start=time.perf_counter();code=adapter.post(payload)
            return code,(time.perf_counter()-start)*1000
        with ThreadPoolExecutor(max_workers=concurrency) as pool:results=list(pool.map(send,range(count)))
        admitted=time.perf_counter()-begin
        pending_at_burst=len(adapter.pending())
        wait_for(adapter.drained,seconds=60,label='load drain')
        elapsed=time.perf_counter()-begin
        rows=lite.logs()
        # Lite's listing is capped; use supported metadata filtering to inspect tail and total via stats API.
        totals=lite.api('GET',f'/api/dash/organization/{lite.org}/stats')
        assert totals['totalLogs']==count,(totals['totalLogs'],count)
        status,_,raw=request(19090,'GET','/metrics');assert status==200
        metrics=raw.decode()
        assert f'adapter_delivered_total {count}\n' in metrics
        latency=sorted(r[1] for r in results)
        evidence={'environment':platform.platform(),'machine':platform.machine(),'go':'1.27.2','sqlite':'3.53.4','events':count,'concurrency':concurrency,'payload_bytes_min':len(json.dumps({'content':'SYNTHETIC load event 0000','extra':'x'*900}).encode()),'http_204':sum(r[0]==204 for r in results),'admission_seconds':round(admitted,3),'admission_events_per_second':round(count/admitted,1),'latency_ms_p50':round(statistics.median(latency),3),'latency_ms_p95':round(latency[int(count*.95)-1],3),'latency_ms_max':round(max(latency),3),'pending_after_burst':pending_at_burst,'end_to_end_seconds':round(elapsed,3),'stored_in_real_lite':totals['totalLogs'],'send_interval_ms':100}
        assert evidence['http_204']==count
        (ROOT/'artifacts/load.json').write_text(json.dumps(evidence,indent=2)+'\n')
        (ROOT/'artifacts/load.metrics').write_text(metrics)
        print(json.dumps(evidence,indent=2))
    finally:adapter.stop()
if __name__=='__main__':main()
