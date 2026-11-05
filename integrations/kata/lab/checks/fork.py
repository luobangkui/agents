import os,signal,time,json,pathlib,errno
kids=[];err=None;peak=0
try:
 for _ in range(40):
  try:pid=os.fork()
  except OSError as e:err=e.errno;break
  if pid==0:time.sleep(10);os._exit(0)
  kids.append(pid)
 peak=int(pathlib.Path('/sys/fs/cgroup/pids.current').read_text())
 print(json.dumps({'spawned':len(kids),'peak':peak,'error_errno':err,'pids_events':pathlib.Path('/sys/fs/cgroup/pids.events').read_text()}),flush=True)
finally:
 for pid in kids:
  try:os.kill(pid,signal.SIGKILL)
  except ProcessLookupError:pass
 for pid in kids:os.waitpid(pid,0)
