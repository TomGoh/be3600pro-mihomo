#!/usr/bin/env python3
"""Check staged Git content without printing credentials. Never publishes files."""
import argparse
import getpass
import json
import pathlib
import re
import subprocess


def known_values(root):
    values=set()
    path=root/'private/secrets.json'
    if path.exists():
        values.update(v for v in json.loads(path.read_text()).values() if isinstance(v,str) and len(v)>=8)
    fields={'password','secret','token','stok','uuid','private-key','api-key'}
    def visit(value):
        if isinstance(value,dict):
            for key,item in value.items():
                if key.lower() in fields and isinstance(item,str) and len(item)>=8:values.add(item)
                visit(item)
        elif isinstance(value,list):
            for item in value:visit(item)
    for name in ('private/config.json','private/pairing.json','private/router-pairing.json',
                 'private/ssh/admin-session.json','work/router-session.json','work/router-ssh/admin-session.json'):
        path=root/name
        if path.exists():visit(json.loads(path.read_text()))
    return values


def scan(root,extra=None):
    secrets=known_values(root)
    if extra:secrets.add(extra)
    def git(*args):return subprocess.check_output(['git',*args],cwd=root)
    names=[v.decode() for v in git('diff','--cached','--name-only','-z').split(b'\0') if v]
    findings=[]
    for name in names:
        if not (root/name).exists():continue
        kind=None
        if name.lower().startswith(('private/','work/','build/','tools/','logs/')):
            kind='private/generated path'
        elif pathlib.Path(name).suffix.lower() in ('.jks','.keystore','.p12','.pfx'):
            kind='private signing material'
        elif int(git('cat-file','-s',':'+name))>5*1024*1024:
            kind='unexpected large source asset'
        else:
            raw=git('show',':'+name)
            if any(v.encode() in raw or v in name for v in secrets):kind='known private value'
            elif re.search(rb'-----BEGIN (?:RSA |EC |OPENSSH |ENCRYPTED )?PRIVATE KEY-----\r?\n',raw):kind='private key block'
        if kind:
            safe=name
            for v in secrets:safe=safe.replace(v,'[redacted]')
            findings.append({'path':safe,'reason':kind})
    return {'stagedFiles':len(names),'checkedPrivateValues':len(secrets),'findings':findings}


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--root',type=pathlib.Path,default=pathlib.Path(__file__).resolve().parents[1])
    p.add_argument('--check-extra-secret',action='store_true',help='Also check one hidden-input credential without storing it')
    args=p.parse_args();extra=None
    if args.check_extra_secret:
        extra=getpass.getpass('Additional credential to check (not saved): ')
        if len(extra)<8:p.error('Use at least 8 characters for an informative exact-value check')
    report=scan(args.root.resolve(),extra);print(json.dumps(report,ensure_ascii=False,indent=2))
    if report['findings']:raise SystemExit(1)


if __name__=='__main__':main()
