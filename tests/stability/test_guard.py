import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'scripts'))
from diagnostics.lease import heartbeat


class GuardTests(unittest.TestCase):
    def setup_guard(self,directory,lease='100',owner='owner',age=2,maximum=30,actions=None):
        p=Path(directory);(p/'stability-token').write_text(owner);(p/'stability-lease').write_text(lease);(p/'clock').write_text('100.00 0.00\n')
        cleanup=p/'cleanup';cleanup.write_text('#!/bin/sh\nprintf "%s\\n" "$1" >> "'+str(p/'called')+'"\n')
        sleep=p/'sleep'
        sleep.write_text('#!'+sys.executable+'\nfrom pathlib import Path\np=Path('+repr(str(p))+')\ni=int((p/"step").read_text()) if (p/"step").exists() else 0\na='+repr(actions or [])+'\nif i>=len(a):\n (p/"stability-token").write_text("other")\nelse:\n for k,v in a[i].items():(p/k).write_text(v)\n(p/"step").write_text(str(i+1))\n')
        sleep.chmod(0o700)
        return ['sh',str(ROOT/'scripts/stability-guard.sh'),'--run-live','--run-dir',str(p),'--token','owner','--cleanup-script',str(cleanup),'--clock-file',str(p/'clock'),'--lease-seconds',str(age),'--max-seconds',str(maximum),'--poll-seconds','1'],dict(os.environ,PATH=str(p)+':'+os.environ['PATH'])

    def test_guard_is_opt_in_and_does_not_touch_files_by_default(self):
        r=subprocess.run(['sh',str(ROOT/'scripts/stability-guard.sh')],capture_output=True)
        self.assertEqual(r.returncode,2)

    def test_empty_heartbeat_window_does_not_trigger_false_expiry(self):
        with tempfile.TemporaryDirectory() as td:
            args,env=self.setup_guard(td,actions=[{'clock':'101.00','stability-lease':''},{'clock':'102.00','stability-lease':'102'},{'stability-token':'other'}])
            subprocess.run(args,env=env,check=True,timeout=3)
            self.assertFalse((Path(td)/'called').exists())

    def test_real_expiration_calls_disable_and_releases_own_state(self):
        with tempfile.TemporaryDirectory() as td:
            args,env=self.setup_guard(td,lease='0')
            subprocess.run(args,env=env,check=True,timeout=3)
            self.assertEqual((Path(td)/'called').read_text(),'disable\n')
            self.assertFalse((Path(td)/'stability-token').exists())

    def test_stale_heartbeat_cannot_rewind_a_known_good_timestamp(self):
        with tempfile.TemporaryDirectory() as td:
            args,env=self.setup_guard(td,actions=[{'clock':'101.00','stability-lease':'0'},{'clock':'102.00','stability-lease':'102'},{'stability-token':'other'}])
            subprocess.run(args,env=env,check=True,timeout=3)
            self.assertFalse((Path(td)/'called').exists())

    def test_future_or_malformed_heartbeat_cannot_extend_total_deadline(self):
        for lease in ['999999','bad','9999999999999999999999999']:
            with self.subTest(lease=lease),tempfile.TemporaryDirectory() as td:
                args,env=self.setup_guard(td,lease=lease,age=20,maximum=2,actions=[{'clock':'102.00'}])
                subprocess.run(args,env=env,check=True,timeout=3)
                self.assertTrue((Path(td)/'called').exists())

    def test_changed_owner_is_not_disabled_or_deleted(self):
        with tempfile.TemporaryDirectory() as td:
            args,env=self.setup_guard(td,owner='other',lease='0')
            subprocess.run(args,env=env,check=True,timeout=3)
            self.assertFalse((Path(td)/'called').exists());self.assertEqual((Path(td)/'stability-token').read_text(),'other')

    def test_cleanup_failure_preserves_evidence_and_returns_failure(self):
        with tempfile.TemporaryDirectory() as td:
            args,env=self.setup_guard(td,lease='0');(Path(td)/'cleanup').write_text('exit 7\n')
            r=subprocess.run(args,env=env,capture_output=True,timeout=3)
            self.assertNotEqual(r.returncode,0);self.assertTrue((Path(td)/'stability-token').exists())

    def test_atomic_heartbeat_has_private_mode_and_replaces_empty_value(self):
        with tempfile.TemporaryDirectory() as td:
            p=Path(td)/'stability-lease';p.write_text('')
            heartbeat(p,123)
            self.assertEqual(p.read_text(),'123\n');self.assertEqual(p.stat().st_mode&0o777,0o600)
            self.assertEqual(list(Path(td).iterdir()),[p])
            with self.assertRaises(ValueError):heartbeat(p,-1)

if __name__=='__main__':unittest.main()
