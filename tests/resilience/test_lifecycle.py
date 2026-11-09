import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[2]

class LifecycleTests(unittest.TestCase):
    def run_shell(self, body):
        with tempfile.TemporaryDirectory() as folder:
            env=dict(os.environ, DATA=folder, RUN=folder)
            return subprocess.run(['sh','-eu','-c', '. "'+str(ROOT/'scripts/lifecycle.sh')+'"; '+body],env=env,capture_output=True,text=True,timeout=5)

    def test_stale_intent_is_rejected(self):
        result=self.run_shell('intent_snapshot; intent_cancel; if intent_current; then exit 31; fi; intent_snapshot; intent_current')
        self.assertEqual(result.returncode,0,result.stderr)

    def test_explicit_epoch_cannot_be_rebased_by_late_enable(self):
        result=self.run_shell('intent_snapshot; old=$EXPECT_EPOCH; intent_cancel; CDX_EXPECT_EPOCH=$old; intent_snapshot; if intent_current; then exit 32; fi')
        self.assertEqual(result.returncode,0,result.stderr)
