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

    def test_empty_profile_is_rejected_before_creating_router_configuration(self):
        with tempfile.TemporaryDirectory() as t:
            root=pathlib.Path(t);src=root/'empty.json';src.write_text('{}')
            result=subprocess.run([sys.executable,str(ROOT/'scripts/prepare-config.py'),'--config',str(src),'--output-dir',str(root/'private')],capture_output=True)
            self.assertNotEqual(result.returncode,0)
            self.assertFalse((root/'private/config.json').exists())


if __name__=='__main__':unittest.main()
