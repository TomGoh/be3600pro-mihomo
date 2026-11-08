#!/usr/bin/env python3
"""Plan/install RN01 services. Defaults to a local plan; --install is explicit.

Only a fresh service installation is supported. Existing configuration is never
overwritten. Proxy remains off until the owner enables it in the Android app.
"""
import argparse
import hashlib
import importlib.util
import io
import json
import os
import pathlib
import ssl
import shlex
import subprocess
import tarfile
import time
import uuid

ROOT=pathlib.Path(__file__).resolve().parents[1]
SCRIPTS=('routerctl.sh','core-release.sh','bypass-rules.sh','core.init','run-core.sh',
 'agent.init','run-agent.sh','watchdog.init','watchdog.sh','rollback.init',
 'rollback-worker.sh','bootstrap.init','bootstrap.sh','firewall-hook.sh')


def payload(prepared):
    release=json.loads((ROOT/'release/core-release.json').read_text())
    plugins=json.loads((ROOT/'release/xtables-release.json').read_text())
    files={name:(ROOT/'scripts'/name).read_bytes() for name in SCRIPTS}
    for name in ('config.json','core-curl.conf','initial-rules.tgz'):
        files[name]=(prepared/name).read_bytes()
    config=json.loads(files['config.json'])
    if config.get('external-controller')!='127.0.0.1:9090' or config.get('mixed-port')!=7890:
        raise ValueError('Use prepare-config.py; arbitrary controller/port settings are unsupported')
    with tarfile.open(fileobj=io.BytesIO(files['initial-rules.tgz'])) as archive:
        for m in archive.getmembers():
            if not m.isfile() or not m.name.startswith('rule-cache/') or '..' in pathlib.PurePosixPath(m.name).parts:
                raise ValueError('Unsafe initial rule cache archive')
    credentials=json.loads((prepared/'secrets.json').read_text())
    token=credentials['agent']
    import re
    if not re.fullmatch(r'[A-Za-z0-9_-]{32,128}',token):raise ValueError('Invalid management token')
    files['agent.token']=(token+'\n').encode()
    selections=json.loads((prepared/'selections.json').read_text()) if (prepared/'selections.json').exists() else {}
    files['state.json']=(json.dumps({'subscriptions':[],'selections':selections},ensure_ascii=False)+'\n').encode()
    files['router-agent.gz']=(ROOT/'build/agent/router-agent.gz').read_bytes()
    files['agent.sha256']=(hashlib.sha256(files['router-agent.gz']).hexdigest()+'\n').encode()
    files['initial-rules.sha256']=(hashlib.sha256(files['initial-rules.tgz']).hexdigest()+'\n').encode()
    files['desired-enabled']=b'0\n'
    files['agent-required']=b''
    files['core-update.bsdiff']=(ROOT/'release/core-update.bsdiff').read_bytes()
    if hashlib.sha256(files['core-update.bsdiff']).hexdigest()!=release['patchSha256']:
        raise ValueError('Core delta checksum mismatch')
    for name,info in plugins['extensions'].items():
        filename='libxt_'+name+'.so';raw=(ROOT/'build/extensions'/filename).read_bytes()
        if len(raw)!=info['bytes'] or hashlib.sha256(raw).hexdigest()!=info['sha256']:
            raise ValueError('Unverified xtables extension')
        files[filename]=raw
    core=(ROOT/'build/release/mihomo-router-armv7.gz').read_bytes()
    if len(core)!=release['archiveBytes'] or hashlib.sha256(core).hexdigest()!=release['archiveSha256']:
        raise ValueError('Core recovery archive checksum mismatch')
    return files,core,release


def credentials_token(raw):
    return raw.decode().strip()


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--prepared',type=pathlib.Path,default=ROOT/'private')
    p.add_argument('--ssh-config',type=pathlib.Path,default=ROOT/'private/ssh/config')
    p.add_argument('--install',action='store_true')
    args=p.parse_args();files,core,release=payload(args.prepared)
    needed=(sum(len(v) for v in files.values())+1024*1024+1023)//1024
    print(json.dumps({'target':'RN01 stock 1.0.74, default 192.168.31.1 LAN',
      'persistentFiles':len(files),'minimumFreeFlashKiB':needed,'coreArchiveBytesInRAM':len(core),
      'proxyAfterInstall':'off','action':'install' if args.install else 'plan only'},indent=2))
    if not args.install:return
    spec=importlib.util.spec_from_file_location('access',ROOT/'scripts/router-access.py')
    access=importlib.util.module_from_spec(spec);spec.loader.exec_module(access)
    access.require_supported(access.Router().api('xqsystem/init_info'))
    ssh=['ssh','-F',str(args.ssh_config),'-o','BatchMode=yes','-o','ConnectTimeout=5','be3600pro']
    effective=subprocess.check_output(ssh[:-1]+['-G','be3600pro'],text=True)
    options=dict(line.split(' ',1) for line in effective.splitlines() if ' ' in line)
    if options.get('hostname')!='192.168.31.1' or options.get('port')!='2222' or options.get('user')!='root':
        raise SystemExit('SSH alias must point to root@192.168.31.1:2222; refusing a different target')
    def remote(command,timeout=90,check=True):
        return subprocess.run(ssh+[command],capture_output=True,check=check,timeout=timeout)
    guard="set -e; [ ! -e /data/codex-router/config.json ] && [ ! -e /data/codex-router/agent.token ] && [ ! -e /tmp/mihomo ] || { echo EXISTING_INSTALLATION_REFUSED; exit 1; }; [ \"$(uname -m)\" = armv7l ]; test $(df -Pk /data | awk 'NR==2 {print $4}') -ge "+str(needed)
    checked=remote(guard,timeout=12,check=False)
    if checked.returncode:
        raise SystemExit('Preflight refused: existing service/runtime, unsupported CPU, or insufficient flash. Nothing uploaded.')
    router_time=int(remote('date +%s',timeout=8).stdout.strip())
    if abs(router_time-time.time())>86400:
        raise SystemExit('Let stock firmware synchronize time before creating the paired TLS identity')
    stamp=time.strftime('%Y%m%d-%H%M%S');local=ROOT/'private/install'/stamp
    local.mkdir(parents=True,mode=0o700);local.chmod(0o700)
    backup=remote('tar -czf - /etc/config/firewall /etc/config/dhcp',timeout=20).stdout
    (local/'stock-config-backup.tgz').write_bytes(backup);(local/'stock-config-backup.tgz').chmod(0o600)
    for name,data in files.items():
        path=local/name;path.write_bytes(data);path.chmod(0o600)
    (local/'core.gz').write_bytes(core);(local/'core.gz').chmod(0o600)
    stage='/tmp/cdx-install-'+uuid.uuid4().hex
    remote('mkdir -m 700 '+stage)
    installed=False
    try:
        for name in [*files,'core.gz']:
            subprocess.run(['scp','-O','-F',str(args.ssh_config),str(local/name),'be3600pro:'+stage+'/'+name],check=True,timeout=40)
        checks=' && '.join('[ "$(sha256sum '+stage+'/'+name+" | cut -d ' ' -f 1)\" = "+hashlib.sha256(data).hexdigest()+' ]' for name,data in {**files,'core.gz':core}.items())
        remote('set -e; '+checks,timeout=15)
        commands=['set -e','umask 077','D=/data/codex-router','R=/tmp/codex-mihomo','mkdir -p "$D" "$R"',
          *['cp '+stage+'/'+name+' "$D/'+name+'"' for name in files],
          'gzip -dc '+stage+'/core.gz > /tmp/mihomo','chmod 700 /tmp/mihomo',
          '[ "$(sha256sum /tmp/mihomo | cut -d " " -f 1)" = '+release['binarySha256']+' ]',
          'gzip -dc "$D/router-agent.gz" > "$R/router-agent"','chmod 700 "$R/router-agent"',
          'sh "$D/routerctl.sh" acl','sh /etc/rc.common "$D/agent.init" start',
          'sh /etc/rc.common "$D/watchdog.init" start','sh "$D/routerctl.sh" core-start',
          'uci set firewall.codex_mihomo=include','uci set firewall.codex_mihomo.type=script',
          'uci set firewall.codex_mihomo.path=/data/codex-router/firewall-hook.sh',
          'uci set firewall.codex_mihomo.family=any','uci set firewall.codex_mihomo.reload=1',
          'uci set firewall.codex_mihomo.enabled=1','uci commit firewall']
        remote('; '.join(commands),timeout=100)
        pair=remote('cat /data/codex-router/pairing.json',timeout=10).stdout
        info=json.loads(pair)
        if info.get('routerUrl')!='https://192.168.31.1:9443':raise ValueError('Unexpected pairing address')
        certificate=remote('cat /data/codex-router/server.crt',timeout=10).stdout
        der=ssl.PEM_cert_to_DER_cert(certificate.decode())
        if hashlib.sha256(der).hexdigest()!=info.get('certificateSha256'):
            raise ValueError('Pairing certificate fingerprint mismatch')
        paired=ROOT/'private/router-pairing.json';paired.write_bytes(pair);paired.chmod(0o600)
        trusted=ROOT/'private/server.crt';trusted.write_bytes(certificate);trusted.chmod(0o600)
        if info.get('token')!=credentials_token(files['agent.token']):raise ValueError('Pairing token differs from prepared credentials')
        host_curl=ROOT/'private/agent-curl.conf'
        host_curl.write_text('silent\nshow-error\ncacert = '+json.dumps(str(trusted),ensure_ascii=False)+'\nheader = '+json.dumps('Authorization: Bearer '+info['token'])+'\n')
        host_curl.chmod(0o600)
        installed=True
        print('Installed with proxy OFF. Import private/router-pairing.json in the Android app. Keep this credential private.')
        print('Stock config backup:',local/'stock-config-backup.tgz')
    finally:
        if not installed:
            remote('sh /data/codex-router/routerctl.sh disable; for name in core agent watchdog bootstrap; do sh /etc/rc.common /data/codex-router/$name.init stop; done; uci -q delete firewall.codex_mihomo; uci commit firewall',check=False)
            print('Installation incomplete: proxy disabled; private service files and backup retained for repair.')
        remote('rm -rf '+shlex.quote(stage),check=False)


if __name__=='__main__':main()
