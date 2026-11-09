import hashlib
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[2]
class BootstrapTests(unittest.TestCase):
    def fetch(self, failures, bad_hash=False):
        with tempfile.TemporaryDirectory() as f:
            base=Path(f); tools=base/'tools'; tools.mkdir()
            curl=tools/'curl'
            curl.write_text('#!'+os.sys.executable+'\nimport os,pathlib,sys\np=pathlib.Path(os.environ["RUN"])/"calls"\nn=int(p.read_text())+1 if p.exists() else 1\np.write_text(str(n))\nif n<=int(os.environ["FAILURES"]):sys.exit(22)\npathlib.Path(sys.argv[sys.argv.index("-o")+1]).write_bytes(b"official bytes")\n')
            curl.chmod(0o700)
            (tools/'sleep').write_text('#!/bin/sh\nexit 0\n');(tools/'sleep').chmod(0o700)
            digest=hashlib.sha256(b'wrong' if bad_hash else b'official bytes').hexdigest()
            env=dict(os.environ,PATH=str(tools)+':'+os.environ['PATH'],RUN=f,BASE_URL='https://api.github.com/fixture',BASE_FALLBACK_URL='https://github.com/fixture',BASE_ARCHIVE_SHA=digest,CORE_BINARY_SHA='not-a-real-sha',FAILURES=str(failures))
            r=subprocess.run(['sh','-eu','-c','. "$1"; bootstrap_fetch "$RUN/download.gz"','test',str(ROOT/'scripts/bootstrap-download.sh')],env=env,capture_output=True,text=True,timeout=10)
            return r.returncode,int((base/'calls').read_text()),(base/'download.gz').exists()
    def test_later_attempt_succeeds_after_transient_failures(self):
        self.assertEqual(self.fetch(3),(0,4,True))
    def test_wrong_hash_never_survives_and_retry_count_is_bounded(self):
        self.assertEqual(self.fetch(0,True),(1,12,False))
    def test_total_outage_ends_without_unbounded_requests(self):
        self.assertEqual(self.fetch(100),(1,12,False))

class BootstrapLockTests(unittest.TestCase):
    def test_legacy_stale_directory_and_unowned_lock_file_allow_start(self):
        import fcntl
        for legacy in (True,False):
            with self.subTest(legacy=legacy),tempfile.TemporaryDirectory() as f:
                base=Path(f);run=base/'run';data=base/'data';tools=base/'tools'
                for p in (run,data,tools):p.mkdir()
                lock=run/'bootstrap.lock'
                if legacy:lock.mkdir()
                else:lock.touch()
                (tools/'flock').write_text((ROOT/'tests/fixtures/flock.py').read_text().replace('#!/usr/bin/env python3','#!'+os.sys.executable));(tools/'flock').chmod(0o700)
                code=(ROOT/'scripts/bootstrap.sh').read_text().split('if [ ! -x "$RUN/router-agent" ]; then')[0].replace('DATA=/data/codex-router','DATA='+str(data)).replace('RUN=/tmp/codex-mihomo','RUN='+str(run))+'\nprintf LOCK_ACQUIRED\n'
                script=base/'bootstrap-prefix.sh';script.write_text(code)
                env=dict(os.environ,PATH=str(tools)+':'+os.environ['PATH'])
                result=subprocess.run(['sh',str(script)],env=env,capture_output=True,text=True,timeout=5)
                self.assertIn('LOCK_ACQUIRED',result.stdout,result.stderr)
                self.assertFalse((run/'bootstrap-owner').exists())
                fd=os.open(lock,os.O_RDWR);fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB)
                try:
                    result=subprocess.run(['sh',str(script)],env=env,capture_output=True,text=True,timeout=5)
                    self.assertNotIn('LOCK_ACQUIRED',result.stdout,'second bootstrap stole active kernel lock')
                finally:os.close(fd)
                result=subprocess.run(['sh',str(script)],env=env,capture_output=True,text=True,timeout=5)
                self.assertIn('LOCK_ACQUIRED',result.stdout,'released lock stayed stale')
