import pathlib,json,subprocess,sys,copy
p=pathlib.Path;output=p('/output');result=p('/evidence/result.json');audit=json.loads(p('/evidence/network-audit.json').read_text());rows=[]
for name in ['missing-audit','malformed-audit','missing-record','wrong-token']:
 a=output/(name+'.json');reward=output/(name+'.reward');value=copy.deepcopy(audit)
 if name=='malformed-audit':a.write_text('{invalid')
 elif name=='missing-record':value['records']=value['records'][:2];a.write_text(json.dumps(value))
 elif name=='wrong-token':value['records'][0]['token']='0'*32;a.write_text(json.dumps(value))
 r=subprocess.run([sys.executable,'-I','-S','/input/grade.py',str(result),str(a),str(reward)],capture_output=True,text=True,timeout=10)
 row={'case':name,'exit_code':r.returncode,'reward':reward.read_text().strip(),'error':r.stderr.strip()};rows.append(row)
 assert r.returncode!=0 and row['reward']=='0',row
(output/'negative-results.json').write_text(json.dumps(rows,indent=2));print('PASS four independent-verifier negative cases')
