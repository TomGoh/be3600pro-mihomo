import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import json
ROOT=Path(__file__).resolve().parents[2]
class HealthTests(unittest.TestCase):
    def check(self, missing=''):
        with tempfile.TemporaryDirectory() as d:
            p=Path(d);(p/'enabled').write_text('1\n');tools=p/'tools';tools.mkdir()
            fake='#!'+os.sys.executable+'\nimport os,sys\nname=os.path.basename(sys.argv[0]);args=" ".join(sys.argv[1:]);missing=os.environ.get("MISSING","")\nif missing and missing in name+" "+args:sys.exit(1)\nif name=="ip":print("10066: from all fwmark 0x6d680000/0xffff0000 lookup 166" if "rule" in args else "local default dev lo scope host")\n'
            (tools/'flock').write_text((ROOT/'tests/fixtures/flock.py').read_text().replace('#!/usr/bin/env python3','#!'+os.sys.executable));(tools/'flock').chmod(0o700)
            for name in ['ip','iptables','ip6tables']:
                (tools/name).write_text(fake);(tools/name).chmod(0o700)
            result=subprocess.run(['sh',str(ROOT/'scripts/health.sh')],env=dict(os.environ,RUN=d,PATH=str(tools)+':'+os.environ['PATH'],MISSING=missing),capture_output=True,text=True,timeout=5)
            return result.returncode,json.loads(result.stdout)
    def test_complete_dual_stack_rules_pass(self):
        code,data=self.check();self.assertEqual(code,0);self.assertTrue(data['ipv4']);self.assertTrue(data['ipv6'])
    def test_missing_ipv6_dns_rule_cannot_report_healthy(self):
        code,data=self.check('ip6tables -t nat -C CDX_DNS6 -p udp')
        self.assertEqual(code,1);self.assertFalse(data['ipv6']);self.assertTrue(data['ipv4'])
    def test_missing_policy_route_cannot_report_healthy(self):
        code,data=self.check('ip -6 rule');self.assertEqual(code,1);self.assertFalse(data['ipv6'])

    def test_missing_transparent_socket_rule_is_unhealthy(self):
        code,data=self.check('ip6tables -t mangle -C CDX_MH6 -p tcp -m socket')
        self.assertEqual(code,1);self.assertFalse(data['ipv6'])
