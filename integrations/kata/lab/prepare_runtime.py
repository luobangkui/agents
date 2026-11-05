import pathlib,tarfile,re,hashlib,json,os
w=pathlib.Path('/work')
assert hashlib.sha256((w/'runtime-artifacts-p5.tar.gz').read_bytes()).hexdigest()=='3461193e6d35f8c76f1409403fd7f882be9c8d3790a50b49ddd294e0f3a4f50a'
for name in ['runtime-artifacts-p5.tar.gz','fixture-checks.tar.gz']:
 with tarfile.open(w/name) as t:
  for m in t.getmembers():assert not m.name.startswith('/') and '..' not in pathlib.PurePosixPath(m.name).parts
  t.extractall(w,filter='data')
for n in ['containerd-shim-kata-v2','kata-agent','kata-agent-ctl']:(w/'artifacts'/n).chmod(0o755)
(w/'kata-service-test-api').chmod(0o755)
(w/'service-token').chmod(0o600)
(w/'kata-logger.py').chmod(0o755)
src=pathlib.Path('/opt/kata/share/defaults/kata-containers/runtime-rs/configuration-qemu-runtime-rs.toml');s=src.read_text()
for k,v in {'kernel':'"/opt/kata/share/kata-containers/vmlinux.container"','image':'"/work/artifacts/kata-fixed.img"','internetworking_model':'"none"','disable_new_netns':'true','static_sandbox_resource_mgmt':'false','default_memory':'1024','default_maxmemory':'2048','default_vcpus':'1','default_maxvcpus':'4','enable_debug':'true'}.items():
 s,n=re.subn('(?m)^'+k+r'\s*=.*$',k+' = '+v,s);assert n>0,(k,n)
assert not re.search(r'(?m)^kernel_verity_params\s*=\s*"[^"\s]',s)
(w/'kata.toml').write_text(s)
(w/'base-config-sha256.json').write_text(json.dumps({'path':str(src),'sha256':hashlib.sha256(src.read_bytes()).hexdigest()}))
(w/'containerd.toml').write_text('''version = 3
root = "/work/containerd/root"
state = "/work/containerd/state"
disabled_plugins = ["io.containerd.cri.v1.images", "io.containerd.cri.v1.runtime"]
[grpc]
  address = "/work/containerd/state/containerd.sock"
[debug]
  level = "debug"
''')
print('prepared independent runtime config and artifacts')
