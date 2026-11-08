#!/usr/bin/env python3
"""Fetch the pinned original OpenWrt userspace extensions; never installs firmware."""
import argparse
import hashlib
import io
import json
import pathlib
import tarfile
import urllib.request

ROOT=pathlib.Path(__file__).resolve().parents[1]


def extract(data,manifest):
    if hashlib.sha256(data).hexdigest()!=manifest['SHA256sum']:
        raise ValueError('OpenWrt package checksum mismatch')
    # Read just the named files. Never extract archive paths into the host FS.
    with tarfile.open(fileobj=io.BytesIO(data)) as outer:
        member=next(m for m in outer.getmembers() if m.name.lstrip('./')=='data.tar.gz')
        payload=outer.extractfile(member).read()
    result={}
    with tarfile.open(fileobj=io.BytesIO(payload)) as archive:
        for name,expected in manifest['extensions'].items():
            member=next(m for m in archive.getmembers() if m.name.lstrip('./')=='usr/lib/iptables/libxt_'+name+'.so')
            raw=archive.extractfile(member).read()
            if len(raw)!=expected['bytes'] or hashlib.sha256(raw).hexdigest()!=expected['sha256']:
                raise ValueError('Userspace extension checksum mismatch')
            result['libxt_'+name+'.so']=raw
    return result


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--package',type=pathlib.Path,help='Use an already downloaded pinned .ipk')
    p.add_argument('--output',type=pathlib.Path,default=ROOT/'build/extensions')
    args=p.parse_args();manifest=json.loads((ROOT/'release/xtables-release.json').read_text())
    if args.package:data=args.package.read_bytes()
    else:
        with urllib.request.urlopen(manifest['base']+manifest['Filename'],timeout=30) as r:data=r.read(128*1024)
    files=extract(data,manifest);args.output.mkdir(parents=True,exist_ok=True)
    for name,raw in files.items():(args.output/name).write_bytes(raw)
    print('Verified TPROXY + socket extensions:',args.output)


if __name__=='__main__':main()
