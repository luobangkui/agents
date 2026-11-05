import re
"""Last-resort reclaim of one journaled execution in the private runtime only."""
import pathlib,sys,json,os,signal,time
r=pathlib.Path('/sys/fs/cgroup');execution=sys.argv[1];assert re.fullmatch(r'(?:ta1-|tav-|ta-)[a-z0-9-]{8,60}',execution)
assert (r/'memory.max').read_text().strip()=='8589934592'
assert pathlib.Path('/proc/self/cgroup').read_text().strip()=='0::/control'
record=json.loads((pathlib.Path('/work/api-state')/execution/'record.json').read_text());assert record['spec']['execution_id']==execution and all(x.startswith(execution+'-') for x in record['ids'])
sandbox=execution+'-pause'
def match(p):
 try:
  args=p.read_bytes().split(b'\0');exe=(p.parent/'exe').resolve().name
 except (FileNotFoundError,ProcessLookupError):return False
 if exe=='qemu-system-x86_64':return ('sandbox-'+sandbox).encode() in args
 if exe=='containerd-shim-kata-v2':return b'-id' in args and args[args.index(b'-id')+1]==sandbox.encode()
 if exe=='virtiofsd':return ('/run/kata/'+sandbox+'/root/virtiofsd.sock').encode() in args
 if b'/work/kata-logger.py' in args:return any(('/work/api-state/'+execution+'/').encode() in x for x in args)
 return False
killed=[]
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 if not match(p):continue
 try:
  fd=os.pidfd_open(int(p.parent.name))
  try:
   if not match(p):raise RuntimeError('process identity changed')
   signal.pidfd_send_signal(fd,signal.SIGKILL);killed.append(int(p.parent.name))
  finally:os.close(fd)
 except ProcessLookupError:pass
end=time.monotonic()+8
while True:
 left=[str(p.parent) for p in pathlib.Path('/proc').glob('[0-9]*/cmdline') if match(p)]
 if not left:break
 if time.monotonic()>end:raise RuntimeError('runtime processes remain: '+str(left))
 time.sleep(.1)
base=r/'taskarena'
end=time.monotonic()+8
while base.exists() and any(p.read_text().strip() for cg in base.glob(execution+'*') for p in cg.rglob('cgroup.procs')):
 if time.monotonic()>end:raise RuntimeError('cgroup processes have not exited')
 time.sleep(.1)
for cg in sorted(base.glob(execution+'*'),reverse=True) if base.exists() else []:
 for child in sorted((x for x in cg.rglob('*') if x.is_dir()),key=lambda p:len(p.parts),reverse=True):
  assert not (child/'cgroup.procs').read_text().strip();child.rmdir()
 assert not (cg/'cgroup.procs').read_text().strip();cg.rmdir()
if base.exists() and not [p for p in base.iterdir() if p.is_dir()]:base.rmdir()
result={'execution_id':execution,'source':'private-runtime-reclaim','killed_pids':killed,'remaining_processes':left,'scope':'journaled execution only; pidfd identity; private cgroup'}
p=pathlib.Path('/work/api-state')/execution/'forced-cleanup.json';p.write_text(json.dumps(result,indent=2));print(json.dumps(result))
