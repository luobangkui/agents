import pathlib,subprocess,os,json,hashlib,re
root=pathlib.Path('/sys/fs/cgroup');w=pathlib.Path('/work')
# Called only after unshare --cgroup --mount, which roots the view at this executor's existing cgroup.
assert pathlib.Path('/proc/self/cgroup').read_text().strip()=='0::/'
assert pathlib.Path('/proc/1/cgroup').read_text().strip()=='0::/'
subprocess.run(['mount','--make-rprivate','/'],check=True)
subprocess.run(['mount','-t','tmpfs','-o','size=4G,mode=1777','tmpfs','/dev/shm'],check=True)
subprocess.run(['umount','/sys/fs/cgroup'],check=True)
subprocess.run(['mount','-t','cgroup2','none','/sys/fs/cgroup'],check=True)
procs=[int(x) for x in (root/'cgroup.procs').read_text().split()]
assert set(procs).issubset({1,os.getpid()}),procs
assert not [p for p in root.iterdir() if p.is_dir()]
leaf=root/'control';leaf.mkdir()
for pid in [1,os.getpid()]:(leaf/'cgroup.procs').write_text(str(pid))
assert not (root/'cgroup.procs').read_text().strip()
available=(root/'cgroup.controllers').read_text().split();wanted=[x for x in ['cpu','memory','pids','cpuset'] if x in available];assert 'pids' in wanted and 'memory' in wanted
(root/'cgroup.subtree_control').write_text(' '.join('+'+x for x in wanted))
(w/'delegation.json').write_text(json.dumps({'scope':'private executor cgroup, isolated cgroup and mount namespaces','root_before':procs,'control_pid':os.getpid(),'limits':{x:(root/x).read_text() for x in ['cpu.max','memory.max','pids.max']},'enabled':(root/'cgroup.subtree_control').read_text()},indent=2))
(w/'runtime-ns.pid').write_text(str(os.getpid()))
os.environ['PATH']='/work/artifacts:/opt/kata/bin:/usr/local/bin:/usr/bin:/bin'
os.execv('/hostbin/containerd',['/hostbin/containerd','--config','/work/containerd.toml'])
