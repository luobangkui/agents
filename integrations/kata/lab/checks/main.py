import os,pathlib,signal,socket,time
signal.signal(signal.SIGTERM,signal.SIG_IGN)
child=os.fork()
if child==0:
 s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('127.0.0.1',39093));s.listen(4);s.settimeout(1)
 while True:
  try:
   c,_=s.accept();c.sendall(b'child-alive');c.close()
  except socket.timeout:pass
pathlib.Path('/app/child.pid').write_text(str(child))
print('main ignores TERM',flush=True)
while True:time.sleep(1)
