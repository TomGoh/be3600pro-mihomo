import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest

ROOT=pathlib.Path(__file__).resolve().parents[1]


class PrepareConfigTest(unittest.TestCase):
    def test_export_keeps_user_rules_and_generates_private_credentials_once(self):
        with tempfile.TemporaryDirectory() as t:
            root=pathlib.Path(t);src=root/'export.json';out=root/'private'
            rules=['DOMAIN-SUFFIX,example.invalid,DIRECT','MATCH,DIRECT']
            src.write_text(json.dumps({'proxies':[{'name':'fixture','type':'ss','server':'example.invalid','port':443,'cipher':'aes-128-gcm','password':'fixture'}],'proxy-groups':[],'rules':rules}))
            cmd=[sys.executable,str(ROOT/'scripts/prepare-config.py'),'--config',str(src),'--output-dir',str(out)]
            subprocess.run(cmd,check=True,capture_output=True)
            config=json.loads((out/'config.json').read_text());credentials=(out/'secrets.json').read_bytes()
            self.assertEqual(config['rules'],rules)
            self.assertNotEqual(config['secret'],'fixture')
            self.assertEqual(config['external-controller'],'127.0.0.1:9090')
            self.assertEqual(os.stat(out/'config.json').st_mode&0o777,0o600)
            subprocess.run(cmd,check=True,capture_output=True)
            self.assertEqual(credentials,(out/'secrets.json').read_bytes())

    def test_rule_cache_records_known_empty_sources_for_startup_gate(self):
        with tempfile.TemporaryDirectory() as t:
            root=pathlib.Path(t);src=root/'export.json';out=root/'private';cache=root/'cache';cache.mkdir()
            (cache/'r.yaml').write_text('payload:\n  - DOMAIN-SUFFIX,example.invalid\n');(cache/'empty.yaml').write_text('payload: []\n')
            src.write_text(json.dumps({'proxies':[{'name':'fixture','type':'ss','server':'example.invalid','port':443,'cipher':'aes-128-gcm','password':'fixture'}],'rule-providers':{'r':{'type':'http','behavior':'classical','path':'r.yaml','url':'https://example.invalid/r'},'empty':{'type':'http','behavior':'classical','path':'empty.yaml','url':'https://example.invalid/e'}},'rules':['RULE-SET,r,DIRECT','MATCH,DIRECT']}))
            subprocess.run([sys.executable,str(ROOT/'scripts/prepare-config.py'),'--config',str(src),'--rule-cache',str(cache),'--output-dir',str(out)],check=True,capture_output=True)
            manifest=json.loads((out/'routing-readiness.json').read_text())
            self.assertEqual(manifest,{'schema':1,'providers':{'r':1,'empty':0}})

    def test_invalid_cache_never_becomes_known_empty_or_logs_its_content(self):
        for body in ['', '# comment only\n', '{}', 'unrelated: []', 'payload: null', 'payload: [https://canary-secret.invalid/?token=canary-private']:
            with self.subTest(body=body),tempfile.TemporaryDirectory() as t:
                root=pathlib.Path(t);src=root/'export.json';cache=root/'cache';cache.mkdir();(cache/'r.yaml').write_text(body);out=root/'private'
                src.write_text(json.dumps({'proxies':[{'name':'fixture'}],'rule-providers':{'r':{'path':'r.yaml','behavior':'classical','type':'http','url':'https://example.invalid'}}}))
                r=subprocess.run([sys.executable,str(ROOT/'scripts/prepare-config.py'),'--config',str(src),'--rule-cache',str(cache),'--output-dir',str(out)],capture_output=True,text=True)
                self.assertNotEqual(r.returncode,0);self.assertNotIn('canary-private',r.stdout+r.stderr);self.assertNotIn('Traceback',r.stderr);self.assertFalse((out/'routing-readiness.json').exists())

    def test_empty_profile_is_rejected_before_creating_router_configuration(self):
        with tempfile.TemporaryDirectory() as t:
            root=pathlib.Path(t);src=root/'empty.json';src.write_text('{}')
            result=subprocess.run([sys.executable,str(ROOT/'scripts/prepare-config.py'),'--config',str(src),'--output-dir',str(root/'private')],capture_output=True)
            self.assertNotEqual(result.returncode,0)
            self.assertFalse((root/'private/config.json').exists())


if __name__=='__main__':unittest.main()
