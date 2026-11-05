import pathlib,os,sys,json
r=pathlib.Path('/sys/fs/cgroup');sid=sys.argv[1]
assert sid.startswith(('ta1-','tav-','ta-')) and '/' not in sid
assert (r/'memory.max').read_text().strip()=='8589934592'
assert pathlib.Path('/proc/self/cgroup').read_text().strip()=='0::/control'
found=[]
for p in pathlib.Path('/proc').glob('[0-9]*/cmdline'):
 try:
  args=p.read_bytes().split(b'\0')
  if args[0].endswith(b'containerd-shim-kata-v2') and b'-id' in args and args[args.index(b'-id')+1]==sid.encode():found.append(int(p.parent.name))
 except (OSError,IndexError):pass
assert len(found)==1,found
pid=found[0];before=pathlib.Path('/proc/'+str(pid)+'/cgroup').read_text().strip()
assert before=='0::/taskarena/'+sid,before
# virtiofsd is control-plane overhead; upstream cgroup cleanup precedes share-fs stop.
overhead=[]
for member in (r/('taskarena/'+sid)/'cgroup.procs').read_text().split():
 task=pathlib.Path('/proc')/member
 args=(task/'cmdline').read_bytes().split(b'\0');exe=(task/'exe').resolve().name
 if exe=='virtiofsd' or int(member)==pid:
  (r/'control/cgroup.procs').write_text(member);overhead.append({'pid':int(member),'exe':exe})
 else:
  assert exe=='qemu-system-x86_64' and ('sandbox-'+sid).encode() in args,(member,exe,args)

after=pathlib.Path('/proc/'+str(pid)+'/cgroup').read_text().strip();assert after=='0::/control'
print(json.dumps({'shim_pid':pid,'overhead_processes':overhead,'sandbox':sid,'before':before,'after':after,'scope':'same private executor cgroup; move shim and virtiofsd, keep QEMU under sandbox cgroup','reason':'FsManager cleanup precedes share-fs stop; control processes must be outside VM cgroup'}))
