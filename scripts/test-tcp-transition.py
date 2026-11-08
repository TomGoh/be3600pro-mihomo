#!/usr/bin/env python3
"""Explicit RN01 live diagnostic: tiny echo streams across a scoped proxy switch.

Adds two documentation-only addresses to router loopback for at most 60 seconds.
Uses no external test server. Restores proxy-off and the original mode on exit.
"""
import argparse
import json
import pathlib
import platform
import shlex
import subprocess
import time
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--run-live', action='store_true', required=True)
    p.add_argument('--ssh-config', required=True)
    p.add_argument('--curl-config', required=True)
    p.add_argument('--mac', required=True)
    p.add_argument('--output', required=True)
    args = p.parse_args()
    import re
    if not re.fullmatch(r'[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}', args.mac):
        p.error('invalid test Mac Wi-Fi MAC')
    if platform.system()!='Darwin':p.error('This runner currently requires a macOS client')
    route=subprocess.check_output(['route','-n','get','default'],text=True)
    if not re.search(r'gateway:\s+192\.168\.31\.1\b',route):p.error('Default route must use the tested router')
    out = pathlib.Path(args.output); out.mkdir(parents=True, exist_ok=False)
    ssh = ['ssh', '-F', args.ssh_config, '-o', 'BatchMode=yes', '-o', 'WarnWeakCrypto=no', '-o', 'ConnectTimeout=4', 'be3600pro']

    def remote(command, check=True):
        return subprocess.run(ssh+[command], capture_output=True, text=True, check=check, timeout=25)

    def mode(value=None):
        command = ['curl', '--noproxy', '*', '-K', args.curl_config, '--max-time', '12', '-f']
        if value is not None:
            command += ['-X', 'PUT', '-H', 'Content-Type: application/json', '-d', json.dumps({'mode': value})]
        return json.loads(subprocess.check_output(command+['https://192.168.31.1:9443/api/mode'], text=True))['mode']

    if json.loads(remote('sh /data/codex-router/routerctl.sh status').stdout)['enabled']:
        raise SystemExit('Proxy must be off before this diagnostic')
    original = mode()
    if original not in ('rule', 'direct', 'global'):
        raise SystemExit('Unknown original mode')
    token = uuid.uuid4().hex
    cleanup = '''#!/bin/sh
set -u
RUN=/tmp/codex-mihomo
[ "$(cat "$RUN/tcp-test-token" 2>/dev/null)" = "$1" ] || exit 0
sh /data/codex-router/routerctl.sh disable
curl -f -K /data/codex-router/agent-curl.conf --max-time 8 -X PUT -H 'Content-Type: application/json' -d '{"mode":"MODE"}' https://192.168.31.1:9443/api/mode >/dev/null 2>&1 || true
ip addr del 198.51.100.77/32 dev lo 2>/dev/null || true
ip -6 addr del 2001:db8:cd00::77/128 dev lo 2>/dev/null || true
peer=$(cat "$RUN/tcp-test-peer.pid" 2>/dev/null || true)
case "$peer" in ''|*[!0-9]*) ;; *)
    [ "$(readlink /proc/$peer/exe 2>/dev/null)" != /tmp/cdx-tcp-peer ] || kill "$peer" 2>/dev/null || true ;;
esac
rm -f "$RUN/tcp-test-peer.pid"
rm -f "$RUN/tcp-test-token"
'''.replace('MODE', original)
    script = out/'cleanup.sh';script.write_text(cleanup)
    for source, dest in ((ROOT/'build/tcp-transition-armv7','/tmp/cdx-tcp-peer'),(script,'/tmp/cdx-tcp-cleanup.sh')):
        subprocess.run(['scp','-O','-F',args.ssh_config,'-o','WarnWeakCrypto=no',str(source),'be3600pro:'+dest], check=True, timeout=20)
    clients=[]; files=[]
    addresses=('198.51.100.77:18080','[2001:db8:cd00::77]:18080')
    def client(kind, index, address, seconds):
        file=(out/('%s%d.log'%(kind,index))).open('w');files.append(file)
        proc=subprocess.Popen([str(ROOT/'build/tcp-transition-mac'),'-address',address,'-seconds',str(seconds)],stdout=file,stderr=subprocess.STDOUT)
        clients.append((kind,index,proc))
    try:
        remote("! ip addr show dev lo | grep -q '198.51.100.77' && ! ip -6 addr show dev lo | grep -q '2001:db8:cd00::77'")
        remote("set -e; printf '%s' "+shlex.quote(token)+" > /tmp/codex-mihomo/tcp-test-token; chmod 700 /tmp/cdx-tcp-peer; (trap '' HUP; sleep 60; sh /tmp/cdx-tcp-cleanup.sh "+shlex.quote(token)+") >/tmp/cdx-tcp-cleanup.log 2>&1 </dev/null &")
        remote("ip addr add 198.51.100.77/32 dev lo; ip -6 addr add 2001:db8:cd00::77/128 dev lo nodad; (trap '' HUP; exec /tmp/cdx-tcp-peer -server -seconds 65) >/tmp/cdx-tcp-peer.log 2>&1 </dev/null & printf '%s\\n' $! > /tmp/codex-mihomo/tcp-test-peer.pid")
        time.sleep(1)
        remote('grep -q TCP_ECHO_READY /tmp/cdx-tcp-peer.log')
        for i,a in enumerate(addresses): client('existing',i,a,16)
        time.sleep(2)
        remote('sh /data/codex-router/routerctl.sh enable-pending '+shlex.quote(args.mac))
        mode('direct')
        for i,a in enumerate(addresses): client('new',i,a,5)
        time.sleep(7)
        remote('sh /data/codex-router/routerctl.sh disable')
        result=[]
        for kind,index,proc in clients:
            code=proc.wait(timeout=20);result.append({'kind':kind,'family':4 if index==0 else 6,'exitCode':code})
        (out/'result.json').write_text(json.dumps(result,indent=2)+'\n')
        print(json.dumps(result))
    finally:
        for _,_,proc in clients:
            if proc.poll() is None:proc.terminate()
        for file in files:file.close()
        cleanup_result=remote('sh /tmp/cdx-tcp-cleanup.sh '+shlex.quote(token),check=False)
        if cleanup_result.returncode:
            raise RuntimeError('SSH cleanup failed; the independent 60-second cleanup timer remains armed')


if __name__=='__main__':
    main()
