"""Offline validation of runner exit/cleanup semantics; does not run Docker or UDP."""
import os
import json
import signal
import time
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[1]
FAKE=r'''import json,os,sys,time
from pathlib import Path
p=Path(os.environ['FAKE_DOCKER_STATE'])
s=json.loads(p.read_text()) if p.exists() else {'calls':[],'containers':{}}
a=sys.argv[1:]
if a[:1]==['--context']:a=a[2:]
s['calls'].append(a)
result='';code=0
if a[0]=='info':result='aarch64'
elif a[:2]==['image','inspect']:pass
elif a[:2]==['network','create']:result='owned-network-id'
elif a[:2]==['network','rm']:pass
elif a[0]=='create':
 role='peer' if '/udp-peer-' in a[a.index('--entrypoint')+1] else 'sender'
 if role=='sender' and os.environ.get('FAIL_CREATE')=='1':code=1
 else:
  result='owned-'+role+'-'+str(len(s['containers']))
  s['containers'][result]=role
elif a[0]=='start':pass
elif a[0]=='logs':result='UDP_PEER_READY' if s['containers'][a[1]]=='peer' else 'simulated sender log'
elif a[0]=='wait':result=os.environ.get('PEER_EXIT' if s['containers'][a[1]]=='peer' else 'SENDER_EXIT','0')
elif a[0]=='rm':pass
else:code=99
p.write_text(json.dumps(s))
if a[0]=='logs' and os.environ.get('DELAY_LOGS')=='1':time.sleep(0.5)
if result:print(result)
sys.exit(code)
'''

class UdpRunner(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name);(self.root/'scripts').mkdir();(self.root/'bin').mkdir()
        self.script=self.root/'scripts/test-udp-isolated-linux.sh'
        self.script.write_text((ROOT/'scripts/test-udp-isolated-linux.sh').read_text())
        build=self.root/'build/udp-rootfix';build.mkdir(parents=True)
        for name in ['udpreply-linux-arm64.test','tproxy-linux-arm64.test','udp-peer-linux-arm64']:(build/name).write_bytes(b'placeholder; never executed')
        docker=self.root/'bin/docker';docker.write_text('#!'+os.sys.executable+'\n'+FAKE);docker.chmod(0o700)
        self.state=self.root/'docker-state.json'
    def run_runner(self,**settings):
        env=dict(os.environ,PATH=str(self.root/'bin')+':'+os.environ['PATH'],FAKE_DOCKER_STATE=str(self.state),**settings)
        return subprocess.run(['/bin/sh',str(self.script)],env=env,capture_output=True,text=True,timeout=10)
    def assert_own_cleanup(self):
        import json
        state=json.loads(self.state.read_text())
        for call in state['calls']:
            if call[0]=='rm':self.assertTrue(all(x=='-f' or x.startswith('owned-') for x in call[1:]))
            if call[:2]==['network','rm']:self.assertEqual(call[2:],["owned-network-id"])
        self.assertTrue(any(c[:2]==['network','rm'] for c in state['calls']))
    def test_both_senders_and_peers_required_to_pass(self):
        result=self.run_runner();self.assertEqual(result.returncode,0,result.stderr)
        self.assertIn('PASS: both Linux suites',result.stdout);self.assert_own_cleanup()
    def test_peer_failure_cannot_report_success(self):
        result=self.run_runner(PEER_EXIT='2');self.assertNotEqual(result.returncode,0)
        self.assertNotIn('PASS: both Linux suites',result.stdout);self.assert_own_cleanup()
    def test_sender_failure_cannot_report_success(self):
        result=self.run_runner(SENDER_EXIT='1');self.assertNotEqual(result.returncode,0)
        self.assertNotIn('PASS: both Linux suites',result.stdout);self.assert_own_cleanup()
    def test_partial_setup_cleans_only_created_ids(self):
        result=self.run_runner(FAIL_CREATE='1');self.assertNotEqual(result.returncode,0);self.assert_own_cleanup()
    def test_each_container_create_forbids_image_downloads(self):
        result=self.run_runner();self.assertEqual(result.returncode,0,result.stderr)
        calls=json.loads(self.state.read_text())['calls']
        creates=[call for call in calls if call[0]=='create']
        self.assertEqual(len(creates),4)
        self.assertTrue(all('--pull=never' in call for call in creates),'container create may attempt a registry download')
    def test_cancellation_exits_before_creating_sender(self):
        env=dict(os.environ,PATH=str(self.root/'bin')+':'+os.environ['PATH'],FAKE_DOCKER_STATE=str(self.state),DELAY_LOGS='1')
        process=subprocess.Popen(['/bin/sh',str(self.script)],env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,start_new_session=True)
        try:
            deadline=time.monotonic()+5
            while time.monotonic()<deadline:
                try:
                    calls=json.loads(self.state.read_text())['calls']
                    if any(c[0]=='logs' for c in calls):break
                except (FileNotFoundError,json.JSONDecodeError):pass
                time.sleep(0.02)
            else:self.fail('runner did not reach peer readiness stage')
            os.kill(process.pid,signal.SIGTERM)
            stdout,stderr=process.communicate(timeout=10)
            self.assertNotEqual(process.returncode,0,'cancelled runner continued and reported success')
            self.assertNotIn('PASS: both Linux suites',stdout)
            creates=[c for c in json.loads(self.state.read_text())['calls'] if c[0]=='create']
            self.assertEqual(len(creates),1,'runner created more resources after cancellation')
            self.assert_own_cleanup()
        finally:
            if process.poll() is None:process.kill();process.communicate()
    def test_negative_control_removing_peer_gate_is_detectably_false_green(self):
        text=self.script.read_text();gate='[ "$sender_code" = 0 ] && [ "$peer_code" = 0 ]'
        self.assertIn(gate,text)
        self.script.write_text(text.replace(gate,'[ "$sender_code" = 0 ]'))
        broken=self.run_runner(PEER_EXIT='2')
        self.assertEqual(broken.returncode,0,'negative control did not exercise the peer gate')
        self.assertIn('PASS: both Linux suites',broken.stdout)
        evidence='\n'.join(p.read_text() for p in (self.root/'logs').rglob('exit-codes.txt'))
        self.assertIn('peer=2',evidence)

if __name__=='__main__':unittest.main()
