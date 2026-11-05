#!/usr/bin/env python3
"""Bounded independent Kata deployment. No host runtime socket or configuration writes."""
import argparse,hashlib,json,os,pathlib,secrets,subprocess,sys,tarfile,time,uuid,urllib.request,urllib.parse
HERE=pathlib.Path(__file__).resolve().parent
p=argparse.ArgumentParser();p.add_argument('action',choices=['deploy','bootstrap','resume','status','cleanup']);p.add_argument('--run-dir',required=True);p.add_argument('--kubeconfig',required=True);p.add_argument('--runtime-archive');p.add_argument('--fixture');p.add_argument('--binary');p.add_argument('--cluster-uid',required=True);p.add_argument('--node-name',required=True);p.add_argument('--node-uid',required=True);p.add_argument('--owner-id');p.add_argument('--fault',default='ignore-term',choices=['','ignore-term','no-raw']);a=p.parse_args();R=pathlib.Path(a.run_dir).resolve();R.mkdir(parents=True,exist_ok=True)
K=['kubectl','--kubeconfig',str(pathlib.Path(a.kubeconfig).resolve()),'--request-timeout=15s']
def save(name,d):
 f=R/(name+'.new');f.write_text(json.dumps(d,indent=2));os.replace(f,R/name)
def call(args,body=None):
 q=subprocess.run(K+args,input=body,text=True,capture_output=True,timeout=45)
 with (R/'cluster-commands.jsonl').open('a') as f:f.write(json.dumps({'args':args,'rc':q.returncode,'out':q.stdout,'err':q.stderr})+'\n')
 if q.returncode:raise RuntimeError(q.stderr)
 return json.loads(q.stdout) if q.stdout.strip() else None
def guard():
 ns=call(['get','ns','kube-system','-o','json']);assert ns['metadata']['uid']==a.cluster_uid
 n=call(['get','node',a.node_name,'-o','json']);assert n['metadata']['uid']==a.node_uid;assert any(c['type']=='Ready' and c['status']=='True' for c in n['status']['conditions']);return n

def control(route,data=None,raw=None):
 req=urllib.request.Request('http://127.0.0.1:38124'+route,data=raw if raw is not None else json.dumps(data or {}).encode(),headers={'Authorization':'Bearer '+(R/'executor-token').read_text()})
 with urllib.request.urlopen(req,timeout=60) as q:return json.load(q)
def upload(src,dst):
 data=pathlib.Path(src).read_bytes();digest=hashlib.sha256(data).hexdigest()
 def piece(path,raw):
  last=None
  for attempt in range(3):
   try:
    result=control('/upload?'+urllib.parse.urlencode({'path':path}),raw=raw)
    assert result['bytes']==len(raw) and result['sha256']==hashlib.sha256(raw).hexdigest()
    return result
   except (OSError,ValueError,AssertionError) as error:last=error;time.sleep(1)
  raise RuntimeError('upload failed after bounded retries: '+path) from last
 if len(data)<=512*1024:return piece(dst,data)
 parts=[]
 for offset in range(0,len(data),512*1024):
  raw=data[offset:offset+512*1024];name=dst+'.part-'+digest[:16]+'-'+str(offset);piece(name,raw);parts.append([name,hashlib.sha256(raw).hexdigest()])
  if offset and offset%(16*1024*1024)==0:print('uploaded',offset//(1024*1024),'MiB of',pathlib.Path(src).name,flush=True)
 plan={'path':dst,'sha256':digest,'parts':parts}
 code="import pathlib,json,hashlib,os;d=json.loads("+repr(json.dumps(plan))+");p=pathlib.Path(d['path']);t=p.with_name(p.name+'.assembled');f=t.open('wb');h=hashlib.sha256()\nfor name,expected in d['parts']:\n b=pathlib.Path(name).read_bytes();assert hashlib.sha256(b).hexdigest()==expected;f.write(b);h.update(b)\nassert h.hexdigest()==d['sha256'];f.flush();os.fsync(f.fileno());f.close();os.replace(t,p)\nfor name,_ in d['parts']:pathlib.Path(name).unlink()\nprint(d['sha256'])"
 job='assemble-'+uuid.uuid4().hex[:12];control('/run',{'name':job,'argv':['python3','-c',code]});deadline=time.monotonic()+30
 while True:
  rc=control('/status')[job]['exit_code']
  if rc is not None:
   assert rc==0,control('/read',{'path':'/work/'+job+'.log'});return {'bytes':len(data),'sha256':digest}
  if time.monotonic()>deadline:raise RuntimeError('assemble timeout')
  time.sleep(.5)
def create(body,state):
 entry={'kind':body['kind'],'name':body['metadata']['name'],'uid':None};state['owned'].append(entry);save('deployment.json',state)
 endpoint='/api/v1/namespaces' if body['kind']=='Namespace' else '/apis/agents.kruise.io/v1alpha1/namespaces/'+body['metadata']['namespace']+'/sandboxes'
 got=call(['create','--raw',endpoint+'?fieldValidation=Strict','-f','-'],json.dumps(body));entry['uid']=got['metadata']['uid'];save('deployment.json',state);return got

guard()
if a.action=='deploy':
 assert a.owner_id, '--owner-id is required for a controller-managed supervisor'
 assert not (R/'deployment.json').exists(),'new run directory required'
 ns='ta-kata-p5-'+uuid.uuid4().hex[:8];s={'profile':'kata-private-pids32-v1','namespace':ns,'owned':[],'created_at':time.time()};save('deployment.json',s)
 create({'apiVersion':'v1','kind':'Namespace','metadata':{'name':ns,'labels':{'taskarena-kata-p5':ns}}},s)
 for name in ['executor-token','service-token']:
  with os.fdopen(os.open(R/name,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600),'w') as f:f.write(secrets.token_hex(32))
 volumes=[{'name':'work','emptyDir':{'sizeLimit':'16Gi'}}];mounts=[{'name':'work','mountPath':'/work'}]
 for name,path,dst,kind in [('kata','/opt/kata','/opt/kata','Directory'),('containerd','/usr/bin/containerd','/hostbin/containerd','File'),('ctr','/usr/bin/ctr','/hostbin/ctr','File')]:
  volumes.append({'name':name,'hostPath':{'path':path,'type':kind}});mounts.append({'name':name,'mountPath':dst,'readOnly':True})
 body={'apiVersion':'v1','kind':'Pod','metadata':{'name':'executor','namespace':ns,'labels':{'taskarena-kata-p5':ns}},'spec':{'restartPolicy':'Never','activeDeadlineSeconds':5400,'terminationGracePeriodSeconds':10,'automountServiceAccountToken':False,'nodeSelector':{'kubernetes.io/hostname':a.node_name},'tolerations':[{'key':'node-role.kubernetes.io/control-plane','operator':'Exists','effect':'NoSchedule'}],'containers':[{'name':'executor','image':'m.daocloud.io/docker.io/library/python@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337','imagePullPolicy':'IfNotPresent','command':['python3','-u','-c',(HERE/'executor_server.py').read_text()],'env':[{'name':'CONTROL_TOKEN_SHA256','value':hashlib.sha256((R/'executor-token').read_bytes()).hexdigest()}],'securityContext':{'privileged':True,'runAsUser':0},'resources':{'requests':{'cpu':'1','memory':'2Gi','ephemeral-storage':'2Gi'},'limits':{'cpu':'8','memory':'8Gi','ephemeral-storage':'18Gi'}},'volumeMounts':mounts}],'volumes':volumes}}
 body['spec']['containers'][0]['env'].append({'name':'SANDBOX_SERVICE_POD_UID','valueFrom':{'fieldRef':{'fieldPath':'metadata.uid'}}})
 body={'apiVersion':'agents.kruise.io/v1alpha1','kind':'Sandbox','metadata':{**body['metadata'],'labels':{**body['metadata']['labels'],'agents.kruise.io/sandbox-claimed':'true'},'annotations':{'agents.kruise.io/owner':a.owner_id,'agents.kruise.io/claim-timestamp':time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())}},'spec':{'template':{'metadata':{'labels':body['metadata']['labels']},'spec':body['spec']},'runtimes':[],'persistentContents':[]}}
 save('executor-desired.json',body);create(body,s);print(ns)
elif a.action in ['bootstrap','resume']:
 s=json.loads((R/'deployment.json').read_text());ns=s['namespace'];deadline=time.monotonic()+90
 while True:
  pod=call(['get','pod','executor','-n',ns,'--ignore-not-found','-o','json'])
  if not pod:
   if time.monotonic()>deadline:raise RuntimeError('controller did not create supervisor')
   time.sleep(1);continue
  assert any(o.get('controller') and o['kind']=='Sandbox' and o['uid']==s['owned'][-1]['uid'] for o in pod['metadata'].get('ownerReferences',[]));assert 'pod_uid' not in s or pod['metadata']['uid']==s['pod_uid']
  if pod['status']['phase']=='Running':
   s['pod_uid']=pod['metadata']['uid'];s['sandbox_uid']=s['owned'][-1]['uid'];s['execution_id']='ta-'+s['sandbox_uid'].replace('-','');save('deployment.json',s);break
  if pod['status']['phase'] in ['Failed','Succeeded'] or time.monotonic()>deadline:raise RuntimeError('executor not running: '+json.dumps(pod['status']))
  time.sleep(1)
 archive=pathlib.Path(a.runtime_archive)
 if a.action=='resume':
  jobs=control('/status')
  assert jobs.get('prepare',{}).get('exit_code')==0, 'resume requires completed preparation; otherwise inspect and reclaim'
  assert 'runtime' in jobs and jobs['runtime']['exit_code'] is None, 'resume will not restart an exited runtime'
  expected={'/work/runtime-artifacts-p5.tar.gz':hashlib.sha256(archive.read_bytes()).hexdigest(),'/work/kata-service-test-api':hashlib.sha256(pathlib.Path(a.binary).read_bytes()).hexdigest()}
  assert expected['/work/runtime-artifacts-p5.tar.gz']=='3461193e6d35f8c76f1409403fd7f882be9c8d3790a50b49ddd294e0f3a4f50a'
  for name in ['prepare_runtime.py','runtime_bootstrap.py','move_shim.py','kata-logger.py','reclaim_owned.py','service_dispatch.py']:
   expected['/work/'+name]=hashlib.sha256((HERE/name).read_bytes()).hexdigest()
  fixture=pathlib.Path(a.fixture)/'task'
  for f in fixture.rglob('*'):
   if f.is_file() and '__pycache__' not in f.parts:expected['/work/fixture/task/'+f.relative_to(fixture).as_posix()]=hashlib.sha256(f.read_bytes()).hexdigest()
  code='import pathlib,json,hashlib;expected=json.loads('+repr(json.dumps(expected))+');actual={p:hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest() for p in expected};assert actual==expected;print(json.dumps(actual))'
  job='resume-verify-'+uuid.uuid4().hex[:8];control('/run',{'name':job,'argv':['python3','-c',code]});deadline=time.monotonic()+30
  while control('/status')[job]['exit_code'] is None:
   if time.monotonic()>deadline:raise RuntimeError('resume verification timeout')
   time.sleep(.5)
  assert control('/status')[job]['exit_code']==0,control('/read',{'path':'/work/'+job+'.log'})
  save('resume-verification.json',{'job':job,'expected_sha256':expected,'result':'matched','runtime_restarted':False})
 else:
  try:
   jobs=control('/status')
   assert not any(name in jobs for name in ['prepare','runtime','service-api']), 'bootstrap already progressed; inspect or reclaim this run before retry'
  except (urllib.error.URLError,ConnectionError):
   log=(R/'forward.log').open('wb');proc=subprocess.Popen(K+['port-forward','-n',ns,'pod/executor','38124:38123','38126:38125','--address=127.0.0.1'],stdout=log,stderr=log,start_new_session=True);save('forward.json',{'pid':proc.pid});time.sleep(2)
  archive=pathlib.Path(a.runtime_archive);assert hashlib.sha256(archive.read_bytes()).hexdigest()=='3461193e6d35f8c76f1409403fd7f882be9c8d3790a50b49ddd294e0f3a4f50a'
  upload(archive,'/work/runtime-artifacts-p5.tar.gz');upload(a.binary,'/work/kata-service-test-api');upload(R/'service-token','/work/service-token')
  for name in ['prepare_runtime.py','runtime_bootstrap.py','move_shim.py','kata-logger.py','reclaim_owned.py','service_dispatch.py']:upload(HERE/name,'/work/'+name)
  fixture=pathlib.Path(a.fixture);bundle=R/'fixture.tar.gz'
  with tarfile.open(bundle,'w:gz') as t:
   t.add(fixture/'task',arcname='fixture/task',filter=lambda info: None if '__pycache__' in info.name else info)
   t.add(HERE/'checks',arcname='checks')
  upload(bundle,'/work/fixture-checks.tar.gz')
  control('/run',{'name':'prepare','argv':['python3','/work/prepare_runtime.py']})
  deadline=time.monotonic()+60
  while True:
   result=control('/status')['prepare']['exit_code']
   if result is not None:
    assert result==0,control('/read',{'path':'/work/prepare.log'});break
   if time.monotonic()>deadline:raise RuntimeError('prepare timeout')
   time.sleep(1)
  print(control('/read',{'path':'/work/prepare.log'}))
  print(control('/run',{'name':'runtime','argv':['unshare','--cgroup','--mount','python3','/work/runtime_bootstrap.py']}))
  time.sleep(2)
 image='m.daocloud.io/docker.io/library/python@sha256:e697c81897302a4c4333bade000d98038fbb21eb064f8ee1fd42d02ebcd9d337'
 cache=['/hostbin/ctr','--address','/work/containerd/state/containerd.sock','--namespace','ta-kata-cache','images']
 for job,argv in [('warm-image',cache+['pull','--local','--platform','linux/amd64','--snapshotter','native',image]),('export-image',cache+['export','--platform','linux/amd64','/work/base-image.oci.tar',image])]:
  jobs=control('/status')
  if job not in jobs:control('/run',{'name':job,'argv':argv})
  deadline=time.monotonic()+180
  while True:
   rc=control('/status')[job]['exit_code']
   if rc is not None:
    assert rc==0,control('/read',{'path':'/work/'+job+'.log'});break
   if time.monotonic()>deadline:raise RuntimeError(job+' timeout')
   time.sleep(2)
 jobs=control('/status');running=[name for name,value in jobs.items() if name.startswith('service-api') and value['exit_code'] is None]
 if running:assert len(running)==1, 'multiple service API processes'
 else:
  assert not any(name.startswith('service-api') for name in jobs), 'service API exited; inspect before recovery'
  print(control('/run',{'name':'service-api','argv':['/work/kata-service-test-api']}))
 binding={'sandboxUID':s['sandbox_uid'],'podUID':s['pod_uid'],'executionID':s['execution_id']}
 piece_data=json.dumps(binding).encode();control('/upload?'+urllib.parse.urlencode({'path':'/work/sandbox-binding.json'}),raw=piece_data)
 control('/upload?'+urllib.parse.urlencode({'path':'/work/runtime-archive-sha256'}),raw=hashlib.sha256(archive.read_bytes()).hexdigest().encode())
 entries=[{'namespace':ns,**binding,'controlContainer':'executor','controlToken':(R/'executor-token').read_text()}]
 profile=R/'api-bindings.json'
 if profile.exists():assert json.loads(profile.read_text())==entries, 'immutable API binding changed'
 else:
  with os.fdopen(os.open(profile,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600),'w') as f:json.dump(entries,f)
 code="import json,pathlib,time,urllib.request\nfor attempt in range(20):\n try:\n  req=urllib.request.Request('http://127.0.0.1:38125/healthz',headers={'Authorization':'Bearer '+pathlib.Path('/work/service-token').read_text().strip()});urllib.request.urlopen(req,timeout=2).close();break\n except OSError:\n  if attempt==19:raise\n  time.sleep(.25)\n"+"req=urllib.request.Request('http://127.0.0.1:38125/v1/runtime/create',data="+repr(json.dumps({'execution_id':s['execution_id'],'fault':a.fault}).encode())+",headers={'Authorization':'Bearer '+pathlib.Path('/work/service-token').read_text().strip(),'Content-Type':'application/json'});print(urllib.request.urlopen(req,timeout=65).read().decode())"
 job='prepare-workload-'+uuid.uuid4().hex[:8];control('/run',{'name':job,'argv':['python','-c',code]});deadline=time.monotonic()+75
 while control('/status')[job]['exit_code'] is None:
  if time.monotonic()>deadline:raise RuntimeError('strict workload preparation timeout')
  time.sleep(1)
 assert control('/status')[job]['exit_code']==0,control('/read',{'path':'/work/'+job+'.log'})
 s['runtime_archive_sha256']=hashlib.sha256(archive.read_bytes()).hexdigest();s['service_binary_sha256']=hashlib.sha256(pathlib.Path(a.binary).read_bytes()).hexdigest();save('deployment.json',s)
elif a.action=='status':print(json.dumps(control('/status'),indent=2))
else:
 s=json.loads((R/'deployment.json').read_text());ns=s['namespace']
 for item in reversed(s['owned']):
  extra=[] if item['kind']=='Namespace' else ['-n',ns];got=call(['get',item['kind'],item['name'],'--ignore-not-found','-o','json']+extra)
  if not got:continue
  assert got['metadata']['labels'].get('taskarena-kata-p5')==ns and got['metadata']['uid']==item['uid']
  endpoint='/api/v1/namespaces/'+ns if item['kind']=='Namespace' else '/apis/agents.kruise.io/v1alpha1/namespaces/'+ns+'/sandboxes/'+item['name']
  call(['delete','--raw',endpoint,'-f','-'],json.dumps({'apiVersion':'v1','kind':'DeleteOptions','preconditions':{'uid':item['uid']},'gracePeriodSeconds':10}))
 deadline=time.monotonic()+90
 while call(['get','namespace',ns,'--ignore-not-found','-o','json']):
  if time.monotonic()>deadline:raise RuntimeError('namespace cleanup pending')
  time.sleep(2)
 save('cluster-cleanup.json',{'namespace':ns,'remaining':[],'node_ready':guard()['metadata']['name']})
 print('namespace and executor reclaimed')
