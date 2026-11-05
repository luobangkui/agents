import json,os,pathlib,socket
p=pathlib.Path
r={'uid':os.getuid(),'ns':{x:os.stat('/proc/self/ns/'+x).st_ino for x in ['net','pid','mnt']},'capabilities':{l.split(':')[0]:l.split(':',1)[1].strip() for l in p('/proc/self/status').read_text().splitlines() if l.startswith(('Cap','NoNewPrivs'))},'pid1_cmdline':p('/proc/1/cmdline').read_bytes().decode().replace('\0',' '),'pids_max':p('/sys/fs/cgroup/pids.max').read_text().strip()}
r['vsock_probes']=[]
for cid in [1,guest_cid]:
 try:
  s=socket.socket(socket.AF_VSOCK,socket.SOCK_STREAM);s.settimeout(1);s.connect((cid,1024));reachable=True;s.close();error=''
 except OSError as e:reachable=False;error=str(e)
 r['vsock_probes'].append({'cid':cid,'port':1024,'reachable':reachable,'error':error})
print(json.dumps(r))
assert all(not x['reachable'] for x in r['vsock_probes']),'workload can connect to guest agent control socket'
