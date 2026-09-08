"""校验文件完整性，不启动程序。"""
from pathlib import Path
import json,hashlib,sys
base=Path(__file__).resolve().parent
d=json.loads((base/'manifest.json').read_text());errors=[]
for f in d['files']:
 p=(base/f['path']).resolve()
 if not p.is_relative_to(base) or not p.is_file():errors.append(f['path']);continue
 h=hashlib.sha256()
 with p.open('rb') as stream:
  for part in iter(lambda:stream.read(1024*1024),b''):h.update(part)
 if h.hexdigest()!=f['sha256'] or p.stat().st_size!=f['bytes']:errors.append(f['path'])
print(json.dumps({'passed':not errors,'files':len(d['files']),'errors':errors},ensure_ascii=False))
sys.exit(bool(errors))
