#!/usr/bin/env python3
"""Reconstruct the pinned UDP-fixed core on the host, never on router flash."""
import argparse
import gzip
import hashlib
import io
import json
import pathlib
import urllib.request

ROOT=pathlib.Path(__file__).resolve().parents[1]


def checked(data,digest,label):
    if hashlib.sha256(data).hexdigest()!=digest:
        raise ValueError(label+' SHA-256 mismatch')
    return data


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--base-binary',type=pathlib.Path,help='Optional already verified official ARMv7 binary')
    p.add_argument('--output',type=pathlib.Path,default=ROOT/'build/release')
    args=p.parse_args();m=json.loads((ROOT/'release/core-release.json').read_text())
    delta=checked((ROOT/'release/core-update.bsdiff').read_bytes(),m['patchSha256'],'Binary delta')
    if args.base_binary:
        base=args.base_binary.read_bytes()
    else:
        request=urllib.request.Request(m['baseArchiveUrl'],headers={'Accept':'application/octet-stream','User-Agent':'BE3600Pro-Mihomo'})
        with urllib.request.urlopen(request,timeout=90) as r:archive=r.read(32*1024*1024+1)
        checked(archive,m['baseArchiveSha256'],'Official archive')
        base=gzip.decompress(archive)
    checked(base,m['baseBinarySha256'],'Official binary')
    import bsdiff4
    binary=checked(bsdiff4.patch(base,delta),m['binarySha256'],'Restored binary')
    buffer=io.BytesIO()
    with gzip.GzipFile(filename='',mode='wb',fileobj=buffer,compresslevel=9,mtime=0) as zipper:
        zipper.write(binary)
    archive=checked(buffer.getvalue(),m['archiveSha256'],'Deterministic recovery archive')
    args.output.mkdir(parents=True,exist_ok=True)
    target=args.output/'mihomo-linux-armv7';target.write_bytes(binary);target.chmod(0o700)
    (args.output/'mihomo-router-armv7.gz').write_bytes(archive)
    print('Verified official base + binary delta + final binary + recovery archive:',args.output)


if __name__=='__main__':main()
