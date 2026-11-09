"""Execute real shell rule builder with recording tools; no host firewall/network access."""
import ipaddress
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[1]
MAC='02:11:22:33:44:55'
OTHER='02:aa:bb:cc:dd:ee'

class GameBypass(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name);self.policy=self.root/'policy';self.policy.write_text(MAC+'\n')
        self.log=self.root/'rules.jsonl';self.recorder=self.root/'iptables-recorder'
        self.recorder.write_text('#!'+os.sys.executable+'\nimport json,os,sys\nwith open(os.environ["RULES_LOG"],"a") as f:f.write(json.dumps(sys.argv[1:])+"\\n")\n')
        self.recorder.chmod(0o700)
        source=(ROOT/'scripts/routerctl.sh').read_text()
        body=source[source.index('install_family() {'):].split('\n}\n',1)[0]+'\n}\n'
        self.builder=self.root/'builder.sh';self.builder.write_text(body)
    def build(self,suffix):
        env=dict(os.environ,RULES_LOG=str(self.log),BYPASS_POLICY=str(self.policy),MARK='0x6d680000/0xffff0000',LAN='br-lan')
        subprocess.run(['/bin/sh','-c','. "$1"; . "$2"; bypass_validate_file "$BYPASS_POLICY" && install_family "$3" "$4" ""','test',str(ROOT/'scripts/bypass-rules.sh'),str(self.builder),str(self.recorder),str(suffix)],env=env,check=True)
        tables={}
        for line in self.log.read_text().splitlines():
            a=json.loads(line);table=a[a.index('-t')+1];op=next(x for x in ('-N','-A','-I') if x in a);i=a.index(op);chain=a[i+1]
            rules=tables.setdefault(table,{}).setdefault(chain,[])
            if op=='-A':rules.append(a[i+2:])
            if op=='-I':rules.insert(0,a[i+2:])
        return tables
    def verdict(self,tables,table,mac,proto,port,ip,syn=True,transparent_socket=False):
        def evaluate(chain):
            for a in tables[table].get(chain,[]):
                if '--mac-source' in a and a[a.index('--mac-source')+1].lower()!=mac:continue
                if '-p' in a and a[a.index('-p')+1]!=proto:continue
                if '--syn' in a and not syn:continue
                if 'socket' in a and not transparent_socket:continue
                if '--match-set' in a and not (ip in ('192.0.2.10','2001:db8::10') and port in (9044,53)):continue
                if '--dport' in a and int(a[a.index('--dport')+1])!=port:continue
                if '-d' in a and ipaddress.ip_address(ip) not in ipaddress.ip_network(a[a.index('-d')+1]):continue
                target=a[a.index('-j')+1]
                if target=='RETURN':return None
                if target in ('TPROXY','REDIRECT'):return target
                result=evaluate(target)
                if result:return result
            return None
        return evaluate('PREROUTING') or 'PASS'
    def test_protected_device_skips_game_tcp_and_dns_in_both_families(self):
        for suffix,ip in [(4,'8.8.8.8'),(6,'2001:4860:4860::8888')]:
            if self.log.exists():self.log.unlink()
            tables=self.build(suffix)
            for proto,port in [('udp',27051),('tcp',443)]:
                self.assertEqual(self.verdict(tables,'mangle',MAC,proto,port,ip),'PASS','protected packet still intercepted')
                self.assertEqual(self.verdict(tables,'mangle',OTHER,proto,port,ip),'TPROXY','other device unexpectedly bypassed')
            for proto in ['udp','tcp']:
                self.assertEqual(self.verdict(tables,'nat',MAC,proto,53,ip),'PASS','protected DNS still redirected')
                self.assertEqual(self.verdict(tables,'nat',OTHER,proto,53,ip),'REDIRECT')
    def test_invalid_policy_is_rejected_before_rule_commands(self):
        self.policy.write_text(MAC+'\nnot-a-mac; touch BAD\n')
        with self.assertRaises(subprocess.CalledProcessError):self.build(4)
        self.assertFalse(self.log.exists(),'invalid policy reached firewall tools')
    def test_tcp_activation_preserves_existing_native_connections(self):
        for suffix,ip in [(4,'8.8.8.8'),(6,'2001:4860:4860::8888')]:
            if self.log.exists():self.log.unlink()
            tables=self.build(suffix)
            self.assertEqual(self.verdict(tables,'mangle',OTHER,'tcp',443,ip,syn=False),'PASS')
            self.assertEqual(self.verdict(tables,'mangle',OTHER,'tcp',443,ip,syn=True),'TPROXY')
            self.assertEqual(self.verdict(tables,'mangle',OTHER,'tcp',443,ip,syn=False,transparent_socket=True),'TPROXY')
    def test_upstream_relay_pair_is_native_without_bypassing_the_device(self):
        for suffix,ip in [(4,'192.0.2.10'),(6,'2001:db8::10')]:
            if self.log.exists():self.log.unlink()
            tables=self.build(suffix)
            for proto in ('tcp','udp'):
                self.assertEqual(self.verdict(tables,'mangle',OTHER,proto,9044,ip),'PASS')
                self.assertEqual(self.verdict(tables,'mangle',OTHER,proto,443,ip),'TPROXY')
                self.assertEqual(self.verdict(tables,'nat',OTHER,proto,53,ip),'PASS')
    def test_emergency_disable_works_without_policy_helper(self):
        data=self.root/'data';run=self.root/'run';tools=self.root/'tools'
        data.mkdir();run.mkdir();tools.mkdir()
        for name in ['enabled','scope','bypass-active']:(run/name).write_text('1\n')
        (data/'lifecycle.sh').write_text((ROOT/'scripts/lifecycle.sh').read_text())
        (tools/'flock').write_text((ROOT/'tests/fixtures/flock.py').read_text().replace('#!/usr/bin/env python3','#!'+os.sys.executable));(tools/'flock').chmod(0o700)
        (data/'desired-enabled').write_text('1\n')
        code=(ROOT/'scripts/routerctl.sh').read_text().replace('DATA=/data/codex-router','DATA='+str(data)).replace('RUN=/tmp/codex-mihomo','RUN='+str(run))
        script=self.root/'routerctl.sh';script.write_text(code)
        fake='#!/bin/sh\ncase " $* " in *" -C "*) exit 1 ;; esac\nexit 0\n'
        for name in ['iptables','ip6tables','ip','ipset']:
            p=tools/name;p.write_text(fake);p.chmod(0o700)
        env=dict(os.environ,PATH=str(tools)+':'+os.environ['PATH'])
        subprocess.run(['/bin/sh',str(script),'disable'],env=env,check=True)
        self.assertEqual((data/'desired-enabled').read_text(),'0\n')
        self.assertFalse((run/'enabled').exists())
        self.assertFalse((run/'bypass-active').exists())

if __name__=='__main__':unittest.main()
