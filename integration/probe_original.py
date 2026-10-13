#!/usr/bin/env python3
"""Run only against this repository's disposable ORIGINAL Lite image."""
import json
from support import Lite, ROOT, compose

def main():
    lite=Lite()
    normal=lite.submit(metadata={'spaces':'  a   b\n c  ','boolean':True,'nil':None,'nested':{'value':'x'},'large':9007199254740993123})
    assert normal[0]==200
    rows=lite.logs()
    assert len(rows)==1
    metadata=rows[0]['Metadata']
    assert metadata['spaces']=='a b c' and 'boolean' not in metadata and 'nil' not in metadata
    assert metadata['nested.value']=='x'
    invalid=lite.submit(dataset='does-not-exist')
    compose('stop','clickhouse')
    try:
        failure=lite.submit(message='SYNTHETIC must not be stored')
    finally:
        compose('start','--wait','clickhouse')
    rows_after=lite.logs()
    assert invalid[0]==200 and failure[0]==200
    assert len(rows_after)==1
    evidence={'base':'74ba491368d88d2747d9013cfd3fc4bcd78d1e34','normal_http':normal[0],'invalid_dataset_http':invalid[0],'storage_down_http':failure[0],'stored_before':len(rows),'stored_after':len(rows_after),'spaces_transformed':metadata['spaces'],'boolean_dropped':'boolean' not in metadata,'null_dropped':'nil' not in metadata,'large_number_after':metadata['large']}
    (ROOT/'artifacts').mkdir(exist_ok=True)
    (ROOT/'artifacts/original-lite.json').write_text(json.dumps(evidence,indent=2)+'\n')
    print(json.dumps(evidence,indent=2))
if __name__=='__main__':main()
