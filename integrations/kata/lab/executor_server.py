import os,json,pathlib,hashlib,hmac,subprocess,threading,urllib.parse,time
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
ROOT=pathlib.Path('/work');ROOT.mkdir(exist_ok=True)
TOKEN_HASH=os.environ['CONTROL_TOKEN_SHA256'];jobs={}
def scoped(s):
 p=pathlib.Path(s).resolve()
 if not p.is_relative_to(ROOT):raise ValueError('path outside private work volume')
 return p
class H(BaseHTTPRequestHandler):
 def log_message(self,*a):pass
 def do_POST(self):
  try:
   token=self.headers.get('Authorization','').removeprefix('Bearer ')
   if not hmac.compare_digest(hashlib.sha256(token.encode()).hexdigest(),TOKEN_HASH):raise PermissionError('unauthorized')
   raw=self.rfile.read(int(self.headers.get('Content-Length','0')));route=urllib.parse.urlparse(self.path)
   if route.path=='/upload':
    p=scoped(urllib.parse.parse_qs(route.query)['path'][0]);p.parent.mkdir(parents=True,exist_ok=True);p.write_bytes(raw);result={'bytes':len(raw),'sha256':hashlib.sha256(raw).hexdigest()}
   else:
    data=json.loads(raw or b'{}')
    if route.path=='/sandbox-service':
     import sys
     sys.path.insert(0,'/work')
     from service_dispatch import dispatch
     result=dispatch(data)
    elif route.path=='/run':
     label=data['name'];assert label.replace('-','').isalnum() and label not in jobs
     log=open(ROOT/(label+'.log'),'wb');env=os.environ.copy();env.update(data.get('env',{}))
     p=subprocess.Popen(data['argv'],cwd=str(scoped(data.get('cwd','/work'))),env=env,stdout=log,stderr=log,start_new_session=True);jobs[label]=(p,log);result={'pid':p.pid,'name':label}
    elif route.path=='/status':result={n:{'pid':p.pid,'exit_code':p.poll()} for n,(p,f) in jobs.items()}
    elif route.path=='/read':
     p=scoped(data['path']);b=p.read_bytes();b=b[-int(data.get('limit',8192)):];result={'text':b.decode(errors='replace')}
    elif route.path=='/delegate':
     # The runtime receives only the cgroup subtree already assigned to this Pod.
     root=pathlib.Path('/sys/fs/cgroup');assert pathlib.Path('/proc/self/cgroup').read_text().strip()=='0::/'
     processes=[int(x) for x in (root/'cgroup.procs').read_text().split()]
     assert os.getpid()==1 and set(processes)=={1},processes
     assert not [p for p in root.iterdir() if p.is_dir()],'unexpected cgroup child: refuse delegation'
     subprocess.run(['mount','-o','remount,rw','/sys/fs/cgroup'],check=True)
     leaf=root/'control';leaf.mkdir();(leaf/'cgroup.procs').write_text('1')
     available=(root/'cgroup.controllers').read_text().split();wanted=[x for x in ['cpu','memory','pids','cpuset'] if x in available];assert 'pids' in wanted and 'memory' in wanted
     (root/'cgroup.subtree_control').write_text(' '.join('+'+x for x in wanted))
     result={'parent_before':processes,'control_cgroup':pathlib.Path('/proc/self/cgroup').read_text(),'enabled':(root/'cgroup.subtree_control').read_text(),'scope':'private executor container cgroup only'};(ROOT/'delegation.json').write_text(json.dumps(result))
    else:raise ValueError('unknown operation')
   body=json.dumps(result).encode();self.send_response(200)
  except Exception as e:body=json.dumps({'error':str(e),'type':type(e).__name__}).encode();self.send_response(400)
  self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
ThreadingHTTPServer(('127.0.0.1',38123),H).serve_forever()
