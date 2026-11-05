import os,json,urllib.request,pathlib
def dispatch(data):
 job=data;assert os.environ.get('SANDBOX_SERVICE_POD_UID')==job['podUID'],'supervisor Pod replaced'
 eid=job['id'];assert eid.startswith('ta-') and eid[3:].replace('-','').isalnum()
 archive=pathlib.Path('/work/runtime-archive-sha256').read_text().strip();assert archive=='3461193e6d35f8c76f1409403fd7f882be9c8d3790a50b49ddd294e0f3a4f50a','runtime artifact mismatch'
 identity=json.loads(pathlib.Path('/work/sandbox-binding.json').read_text());assert identity=={'sandboxUID':job['sandboxUID'],'podUID':job['podUID'],'executionID':eid},'runtime belongs to another Sandbox'
 token=pathlib.Path('/work/service-token').read_text().strip()
 def call(route,data):
  req=urllib.request.Request('http://127.0.0.1:38125'+route,data=json.dumps(data).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
  with urllib.request.urlopen(req,timeout=40) as response:return json.load(response)
 state=call('/v1/runtime/inspect',{'id':eid})['data'];assert not state['reclaimed'] and state['phase']=='Running','runtime unavailable';assert state['target']['execution_id']==eid
 if job['kind']=='describe':return state
 else:
  assert state['target']==job['expected'],'runtime incarnation replaced'
  action=job['action'];assert action['service'] in ['main','observer'] and action['kind'] in ['exec','download','stop']
  if action['kind']=='stop' and state.get('stops',{}).get(action['service']) is not None:
   result=call('/v1/runtime/stop-receipt',{'target':state['target'],'service':action['service']})['data']
  else:result=call('/v1/service-operations',{'target':state['target'],'action':action})['result']
  return {'state':state,'result':result}
