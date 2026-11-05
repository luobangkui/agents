#!/usr/local/bin/python3
"""containerd binary logger ABI; survives API process restart, bounded output."""
import os,pathlib,selectors,sys
assert sys.argv[1]=='output' and len(sys.argv)==3
p=pathlib.Path(sys.argv[2]);root=pathlib.Path('/work/api-state');assert p.resolve().is_relative_to(root) and p.name in ('pause.log','main.log','observer.log')
fd=os.open(p,os.O_WRONLY|os.O_CREAT|os.O_APPEND|os.O_NOFOLLOW,0o600)
selector=selectors.DefaultSelector()
for stream in (3,4):selector.register(stream,selectors.EVENT_READ)
os.close(5)
try:
 while selector.get_map():
  for key,_ in selector.select():
   data=os.read(key.fd,65536)
   if not data:selector.unregister(key.fd);os.close(key.fd);continue
   remaining=1024*1024-os.fstat(fd).st_size
   if remaining>0:os.write(fd,data[:remaining])
 os.fsync(fd)
finally:os.close(fd);selector.close()
