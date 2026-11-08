import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

ROOT=pathlib.Path(__file__).resolve().parents[1]


class SourceGateTest(unittest.TestCase):
    def check(self,files,private):
        with tempfile.TemporaryDirectory() as t:
            root=pathlib.Path(t)
            subprocess.run(['git','init','-q',str(root)],check=True,capture_output=True)
            (root/'private').mkdir();(root/'private/config.json').write_text(json.dumps(private))
            for name,body in files.items():(root/name).write_text(body)
            subprocess.run(['git','-C',str(root),'add',*files],check=True,capture_output=True)
            return subprocess.run([sys.executable,str(ROOT/'scripts/check-source.py'),'--root',str(root)],capture_output=True,text=True)

    def test_private_value_is_detected_without_echoing_it(self):
        secret='synthetic-private-token-for-gate'
        r=self.check({'exposed.txt':secret},{'secret':secret})
        self.assertEqual(r.returncode,1)
        self.assertNotIn(secret,r.stdout)
        self.assertEqual(json.loads(r.stdout)['findings'][0]['reason'],'known private value')

    def test_public_owned_group_identifier_is_not_a_credential(self):
        r=self.check({'safe.txt':'__cdx_bootstrap'},{'proxy':'__cdx_bootstrap'})
        self.assertEqual(r.returncode,0,r.stdout+r.stderr)

    def test_private_key_block_is_detected_without_known_credentials(self):
        r=self.check({'unsafe.txt':'-----BEGIN OPENSSH PRIVATE KEY-----\nfixture\n-----END OPENSSH PRIVATE KEY-----\n'}, {})
        self.assertEqual(r.returncode,1)
        self.assertEqual(json.loads(r.stdout)['findings'][0]['reason'],'private key block')


if __name__=='__main__':unittest.main()
