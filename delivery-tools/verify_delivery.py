#!/usr/bin/env python3
"""离线核对交付清单中的文件大小及SHA-256；完整性通过不代表功能或容量通过。"""
from pathlib import Path
import hashlib,json,sys
root=Path(__file__).resolve().parents[1]
manifest=json.loads((root/'manifest.json').read_text())
errors=[];seen=set()
for item in manifest['files']:
    rel=item['path'];p=(root/rel).resolve()
    if rel in seen or not p.is_relative_to(root):errors.append({'path':rel,'error':'duplicate_or_outside_package'});continue
    seen.add(rel)
    if not p.is_file():errors.append({'path':rel,'error':'missing'});continue
    h=hashlib.sha256()
    with p.open('rb') as f:
        for chunk in iter(lambda:f.read(1024*1024),b''):h.update(chunk)
    if p.stat().st_size!=item['bytes'] or h.hexdigest()!=item['sha256']:errors.append({'path':rel,'error':'hash_or_size_mismatch'})
print(json.dumps({'passed':not errors,'checked_files':len(seen),'errors':errors,'scope':'仅文件完整性，不授予发布、功能兼容或容量验收。'},ensure_ascii=False,indent=2))
sys.exit(1 if errors else 0)
