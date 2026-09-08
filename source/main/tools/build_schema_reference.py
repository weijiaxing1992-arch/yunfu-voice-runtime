#!/usr/bin/env python3
"""从 OpenAPI 生成完整中文字段字典；保留所有模型和嵌套约束，防止手写表格遗漏。"""
import argparse
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def describe_type(value):
    """组合类型保持原分支，不把 null 或引用吞掉。"""
    if '$ref' in value:
        return value['$ref'].rsplit('/', 1)[-1]
    for key in ('oneOf', 'anyOf', 'allOf'):
        if key in value:
            return key + '(' + ', '.join(describe_type(item) for item in value[key]) + ')'
    kind = value.get('type', '任意JSON')
    kind = '/'.join(kind) if isinstance(kind, list) else kind
    return kind + '<' + describe_type(value['items']) + '>' if 'items' in value else kind


def assemble():
    """生成的表格说明一层字段，完整 JSON 保留嵌套对象和条件校验。"""
    spec = json.loads((ROOT / 'docs/api/openapi.json').read_text())
    models = spec['components']['schemas']
    lines = ['# 完整接口字段字典', '', f"文档版本 {spec['info']['version']}；共 {len(models)} 个命名模型。此文件由 OpenAPI 生成。", '',
             '必需仅表示字段必须出现，允许 null 的字段仍可能没有观测值。数组、嵌套对象、组合分支与额外字段策略保留在各模型完整合同中；平台、UTF-8 字节数、地址及跨字段运行约束仍须遵循接口说明。', '']
    def cell(value):
        """转义表格分隔符和换行，文档渲染不执行来源内容。"""
        return str(value).replace('|', '\\|').replace('\n', ' ')
    for name, model in models.items():
        lines += [f'## `{name}`', '', model.get('description', '模型完整约束如下。'), '']
        if model.get('properties'):
            lines += ['| 字段 | 类型 | 必需 | 说明 |', '|---|---|---|---|']
            for key, field in model['properties'].items():
                note = field.get('description', '')
                constraints = {k:v for k,v in field.items() if k not in {'type','items','description','$ref','properties','oneOf','allOf','anyOf'}}
                if constraints:
                    note += '；' + json.dumps(constraints, ensure_ascii=False, separators=(',', ':'))
                lines.append(f"| `{cell(key)}` | `{cell(describe_type(field))}` | {'是' if key in model.get('required',[]) else '否'} | {cell(note) or '见完整模型'} |")
        lines += ['', '完整模型：', '', '```json', json.dumps(model, ensure_ascii=False, indent=2), '```', '']
    return '\n'.join(lines)


def main():
    """检查模式不写文件；修改机器模型后必须主动重新生成可阅读字典。"""
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--check',action='store_true')
    args=parser.parse_args();target=ROOT/'docs/api/schema-reference.md';result=assemble()
    if args.check:
        if not target.is_file() or target.read_text()!=result:
            raise SystemExit('字段字典落后于 OpenAPI，请运行 tools/build_schema_reference.py')
    else:
        target.write_text(result)
    print(json.dumps({'mode':'check' if args.check else 'generate','models':len(json.loads((ROOT/'docs/api/openapi.json').read_text())['components']['schemas'])}))


if __name__=='__main__':
    main()
