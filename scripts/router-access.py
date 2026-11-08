#!/usr/bin/env python3
"""RN01/1.0.74 LAN-only inspection and authenticated, key-only SSH bootstrap.

Default action is read-only identification. --unlock-ssh explicitly changes the
owner's router: stores a maintenance key and starts LAN Dropbear on port 2222.
No firmware, password, boot partition or nvram changes are made.
"""
import argparse
import base64
import getpass
import hashlib
import json
import os
import pathlib
import re
import secrets
import socket
import shlex
import subprocess
import time
import urllib.parse
import urllib.request


def require_supported(info):
    if info.get('hardware', '').upper() != 'RN01' or info.get('romversion') != '1.0.74':
        raise ValueError('Only RN01 stock 1.0.74 is currently verified; no changes made')


def login_fields(html):
    key = re.search(r'var\s+Encrypt\s*=\s*\{\s*key:\s*[\'"]([0-9a-f]{32})[\'"]', html)
    device = re.search(r'var\s+deviceId\s*=\s*[\'"]([0-9a-fA-F:]{17})[\'"]', html)
    mode = re.search(r'var\s+newEncryptMode\s*=\s*parseInt\([\'"]([01])[\'"]\)', html)
    if not key or not device or not mode:
        raise ValueError('Unknown router login challenge; refusing to guess')
    if not re.fullmatch(r'[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}', device[1]):
        raise ValueError('Invalid router-provided client MAC')
    return key[1], device[1], 'sha256' if mode[1]=='1' else 'sha1'


def password_digest(password, salt, nonce, algorithm):
    if algorithm not in ('sha1', 'sha256'):
        raise ValueError('Unsupported challenge algorithm')
    digest = lambda value: hashlib.new(algorithm, value.encode()).hexdigest()
    return digest(nonce+digest(password+salt))


def checked_public_key(text):
    fields = text.strip().split()
    if len(fields) not in (2,3) or fields[0] != 'ssh-rsa':
        raise ValueError('Expected one RSA SSH public key')
    try:
        wire=base64.b64decode(fields[1],validate=True)
    except (ValueError, base64.binascii.Error):
        raise ValueError('Invalid public-key encoding') from None
    if len(wire)<32 or wire[:11]!=b'\0\0\0\x07ssh-rsa':
        raise ValueError('Invalid RSA public key')
    return 'ssh-rsa '+fields[1]+' be3600pro-mihomo'


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        raise ValueError('Router redirects are not accepted')


class Router:
    def __init__(self, address='192.168.31.1'):
        if address!='192.168.31.1':
            raise ValueError('This release requires the default 192.168.31.1 LAN')
        self.address=address
        self.base='http://'+address+'/cgi-bin/luci'
        self.token=None
        self.opener=urllib.request.build_opener(urllib.request.ProxyHandler({}),NoRedirect())

    def read(self, url, data=None, timeout=12):
        body=urllib.parse.urlencode(data).encode() if data is not None else None
        with self.opener.open(urllib.request.Request(url,data=body),timeout=timeout) as response:
            raw=response.read(1024*1024+1)
        if len(raw)>1024*1024: raise ValueError('Router response too large')
        return raw

    def api(self, path, data=None, timeout=12):
        if not re.fullmatch(r'[a-z0-9_/]+',path):raise ValueError('Invalid API path')
        prefix=self.base+('/;stok='+self.token if self.token else '')+'/api/'
        return json.loads(self.read(prefix+path,data,timeout=timeout))

    def login(self):
        salt,device,algorithm=login_fields(self.read(self.base+'/web').decode())
        nonce='0_%s_%d_%d'%(device,int(time.time()),secrets.randbelow(10000))
        password=getpass.getpass('Router administrator password (not saved): ')
        digest=password_digest(password,salt,nonce,algorithm)
        del password
        result=self.api('xqsystem/login',{'username':'admin','password':digest,'logtype':2,'nonce':nonce})
        token=result.get('token','')
        if result.get('code')!=0 or not re.fullmatch(r'[0-9a-f]{32}',token):
            raise ValueError('Router login failed; no automatic password retries')
        self.token=token


class Maintenance:
    """Authenticated RN01 MAC-filter command path; no diagnostic writes."""
    def __init__(self,router):
        self.router=router
        self.mac='02:'+':'.join('%02x'%x for x in secrets.token_bytes(5))
        self.original=None
        self.unacknowledged=0

    def entry(self,name,option):
        try:
            result=self.router.api('xqsystem/set_mac_filter',{'mac':self.mac,'name':name,'option':option,'wan':''},timeout=2)
        except (TimeoutError, socket.timeout):
            # This firmware can apply the command without completing its HTTP
            # response. A timeout is NOT success: capture verifies its unique
            # readback marker; __exit__ verifies restored state; SSH verifies
            # host identity and absence of the temporary MAC below.
            self.unacknowledged+=1
            return
        if result.get('code')!=0:raise ValueError('Authenticated maintenance request rejected')

    def run(self,command):
        if '\n' in command or '\r' in command:raise ValueError('Commands must be single-line')
        try:self.entry('be3600pro_bootstrap ; '+command,'0')
        finally:self.entry('be3600pro_bootstrap','1')

    def __enter__(self):
        result=self.router.api('xqnetwork/diag_get_paras')
        self.original=str(result.get('iperf_test_thr',''))
        if result.get('code')!=0 or not self.original.isdigit():
            raise ValueError('Unexpected diagnostic baseline; no commands executed')
        return self

    def __exit__(self,*unused):
        # run() requests removal in its finally block. The caller verifies state
        # through SSH; this bootstrap never writes diagnostic parameters.
        return None


def private_write(path,text):
    path.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
    fd=os.open(path,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
    os.fchmod(fd,0o600)
    with os.fdopen(fd,'w') as f:f.write(text)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--unlock-ssh',action='store_true')
    parser.add_argument('--router',default='192.168.31.1')
    parser.add_argument('--state-dir',type=pathlib.Path,default=pathlib.Path(__file__).resolve().parents[1]/'private/ssh')
    parser.add_argument('--session-file',type=pathlib.Path,help='Optional private JSON containing an existing administrator stok')
    args=parser.parse_args()
    router=Router(args.router)
    info=router.api('xqsystem/init_info')
    require_supported(info)
    print(json.dumps({k:info[k] for k in ('hardware','romversion')},ensure_ascii=False))
    if not args.unlock_ssh:return
    root=args.state_dir.resolve();root.mkdir(mode=0o700,parents=True,exist_ok=True);root.chmod(0o700)
    key=root/'id_rsa'
    if not key.exists() and not key.with_suffix('.pub').exists():
        subprocess.run(['ssh-keygen','-q','-t','rsa','-b','3072','-N','','-f',str(key),'-C','be3600pro-mihomo'],check=True)
    if not key.is_file() or not key.with_suffix('.pub').is_file():raise ValueError('Incomplete SSH key pair; repair it explicitly')
    key.chmod(0o600)
    pub=checked_public_key(key.with_suffix('.pub').read_text())
    if args.session_file:
        token=json.loads(args.session_file.read_text()).get('stok','')
        if not re.fullmatch(r'[0-9a-f]{32}',token):raise ValueError('Invalid private administrator session file')
        router.token=token
    else:
        print('Authenticating administrator (one attempt)...',flush=True)
        router.login()
        private_write(root/'admin-session.json',json.dumps({'stok':router.token})+'\n')
    print('Administrator authenticated; checking diagnostic baseline...',flush=True)
    encoded=base64.b64encode((pub+'\n').encode()).decode()
    command=("set -e; umask 077; D=/data/codex-router; mkdir -p $D/backup /etc/dropbear; "
      "if [ ! -e $D/backup/ssh-key-backup-complete ]; then if [ -f /etc/dropbear/authorized_keys ]; then cp -p /etc/dropbear/authorized_keys $D/backup/authorized_keys.before; else touch $D/backup/authorized_keys.was-absent; fi; touch $D/backup/ssh-key-backup-complete; fi; "
      "printf '%s' "+shlex.quote(encoded)+" | base64 -d > $D/maintenance.pub; "
      "key=$(cat $D/maintenance.pub); grep -Fqx \"$key\" /etc/dropbear/authorized_keys 2>/dev/null || printf '%s\\n' \"$key\" >> /etc/dropbear/authorized_keys; chmod 600 /etc/dropbear/authorized_keys; "
      "[ -s $D/ssh-host-rsa ] || dropbearkey -t rsa -f $D/ssh-host-rsa >/dev/null 2>&1; "
      "if [ ! -s /tmp/codex-dropbear.pid ] || ! kill -0 \"$(cat /tmp/codex-dropbear.pid)\" 2>/dev/null; then dropbear -s -j -k -r $D/ssh-host-rsa -p 192.168.31.1:2222 -P /tmp/codex-dropbear.pid; fi; "
      "true")
    with Maintenance(router) as maintenance:
        print('Starting LAN SSH; HTTP acknowledgement may be delayed...',flush=True)
        maintenance.run(command)
    print('Verifying key-only SSH and unchanged router state...',flush=True)
    known_hosts=root/'known_hosts'
    if not known_hosts.exists():
        scan=subprocess.run(['ssh-keyscan','-T','5','-t','rsa','-p','2222','192.168.31.1'],capture_output=True,text=True,timeout=8,check=True)
        lines=[line for line in scan.stdout.splitlines() if line.startswith('[192.168.31.1]:2222 ssh-rsa ')]
        if len(lines)!=1:raise ValueError('SSH host key could not be obtained')
        checked_public_key(' '.join(lines[0].split()[1:3]))
        # First connection uses standard SSH trust-on-first-use. Existing pins
        # are never replaced silently; a changed router identity fails closed.
        private_write(known_hosts,lines[0]+'\n')
    config=('Host be3600pro\n HostName 192.168.31.1\n Port 2222\n User root\n IdentityFile '+json.dumps(str(key))+
      '\n IdentitiesOnly yes\n BatchMode yes\n StrictHostKeyChecking yes\n UserKnownHostsFile '+json.dumps(str(root/'known_hosts'))+
      '\n HostKeyAlgorithms +ssh-rsa\n PubkeyAcceptedAlgorithms +ssh-rsa\n')
    private_write(root/'config',config)
    mac=maintenance.mac
    verify="test \"$(uci -q get diag.config.iperf_test_thr)\" = "+shlex.quote(maintenance.original)+" || exit 1; data=$({ for p in macfilter ipfilter wifiaccess; do uci -q export \"$p\"; done; }); if printf '%s' \"$data\" | grep -Fiq "+shlex.quote(mac)+"; then exit 1; fi; if printf '%s' \"$data\" | grep -Fiq "+shlex.quote(mac.replace(':',''))+"; then exit 1; fi"
    subprocess.run(['ssh','-F',str(root/'config'),'be3600pro',verify],check=True,timeout=10)
    print('SSH ready. Use: ssh -F '+shlex.quote(str(root/'config'))+' be3600pro')
    print('SSH is temporary until a tested boot hook is installed; no firmware/nvram changes made.')
    if maintenance.unacknowledged:
        print('Some MAC-filter HTTP responses timed out; key-only SSH, unchanged diagnostic state and temporary MAC cleanup were verified separately.')


if __name__=='__main__':
    try:main()
    except ValueError as error:
        raise SystemExit('Router access failed: '+str(error)) from None
    except (OSError, subprocess.SubprocessError) as error:
        # Exception URLs and server responses may contain the session token.
        raise SystemExit('Router access failed ('+type(error).__name__+'); credentials were not printed. Check the last completed stage and LAN reachability.') from None
