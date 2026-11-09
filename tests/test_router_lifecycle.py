"""Offline router lifecycle regression; all network/process-control tools are fakes."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class RouterLifecycle(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.data = self.root / "data"
        self.run = self.root / "run"
        self.tools = self.root / "tools"
        self.proc = self.root / "fake-bridge-sysctls"
        for path in (self.data, self.run, self.tools, self.proc, self.data / "backup"):
            path.mkdir(parents=True, exist_ok=True)
        for family in ("iptables", "ip6tables"):
            (self.proc / ("bridge-nf-call-" + family)).write_text("1\n")
        (self.data / "lifecycle.sh").write_text((ROOT / "scripts/lifecycle.sh").read_text())
        self._tool("flock",(ROOT / "tests/fixtures/flock.py").read_text().replace("#!/usr/bin/env python3","#!"+os.sys.executable))
        (self.data / "desired-enabled").write_text("0\n")
        (self.data / "config.json").write_text('{"mode":"rule"}')
        (self.data / "bypass-rules.sh").write_text((ROOT / "scripts/bypass-rules.sh").read_text())
        planner=self.run/'router-agent'
        planner.write_text('#!/bin/sh\nwhile [ "$#" -gt 0 ]; do if [ "$1" = -run ]; then run=$2; shift; fi; shift; done\n: > "$run/relay-endpoints.ipset"\n')
        planner.chmod(0o700)
        rc = self.root / "fake-rc.common"
        rc.write_text('if [ "${REVIEW_CONFIRM_FAIL:-0}" = 1 ] && [ "$2" = stop ]; then exit 1; fi\nexit 0\n')
        source = (ROOT / "scripts/routerctl.sh").read_text()
        source = source.replace("DATA=/data/codex-router", "DATA=" + str(self.data))
        source = source.replace("RUN=/tmp/codex-mihomo", "RUN=" + str(self.run))
        source = source.replace("/etc/rc.common", str(rc))
        source = source.replace("/proc/sys/net/bridge", str(self.proc))
        source = source.replace("set -eu\n", "set -eu\nexport REVIEW_ROUTER_PID=$$\n", 1)
        dispatch = 'case "${1:-status}" in'
        self.assertIn(dispatch, source)
        # Extension preparation is outside this test; never touch host mounts/libraries.
        source = source.replace(dispatch, "ensure_xtables() { :; }\n" + dispatch, 1)
        self.script = self.root / "routerctl.sh"
        self.script.write_text(source)
        self._tool("curl", '#!/bin/sh\ncase " $* " in *" -w "*) case " $* " in *baidu.com*) printf 200;; *) printf 204;; esac;; esac\nexit 0\n')
        self._tool("jsonfilter", '#!'+os.sys.executable+'\nimport json,sys\nprint(json.load(open(sys.argv[sys.argv.index("-i")+1]))["mode"])\n')
        self._tool("ipset", '#!/bin/sh\nexit 0\n')
        for name in ("iptables", "ip6tables"):
            self._tool(name, '#!/bin/sh\ncase " $* " in *" -C "*) exit 1;; esac\nexit 0\n')
        self._tool("ip", "#!" + os.sys.executable + '\n'
                   'import os,pathlib,signal,sys\n'
                   'marker=pathlib.Path(os.environ["REVIEW_SIGNAL_MARKER"])\n'
                   'if os.environ.get("REVIEW_SIGNAL_ON_ROUTE")=="1" and sys.argv[1:4]==["route","replace","local"] and not marker.exists():\n'
                   ' marker.write_text("sent\\n")\n'
                   ' os.kill(int(os.environ["REVIEW_ROUTER_PID"]),signal.SIGTERM)\n')

    def _tool(self, name, source):
        path = self.tools / name
        path.write_text(source)
        path.chmod(0o700)

    def _command(self, action, *args, **settings):
        env = dict(os.environ, PATH=str(self.tools) + ":" + os.environ["PATH"],
                   REVIEW_SIGNAL_MARKER=str(self.root / "signal-sent"), **settings)
        return subprocess.run(["/bin/sh", str(self.script), action, *args], env=env,
                              capture_output=True, text=True, timeout=10)

    def _enable(self, **settings):
        return self._command("enable", **settings)

    def _assert_disabled(self):
        self.assertFalse((self.run / "enabled").exists(), "failed activation published enabled state")
        self.assertFalse((self.run / "pending-token").exists(), "failed activation retained pending token")
        self.assertFalse((self.run / "confirmed-token").exists(), "failed activation retained confirmation")
        self.assertFalse((self.run / "bypass-active").exists(), "failed activation published active policy")
        self.assertEqual((self.data / "desired-enabled").read_text(), "0\n")
        for family in ("iptables", "ip6tables"):
            self.assertEqual((self.proc / ("bridge-nf-call-" + family)).read_text(), "1\n")

    def test_refresh_populates_private_sets_before_swapping_live_sets(self):
        import json
        self.assertEqual(self._enable().returncode,0)
        log=self.root/'ipset-log'
        self._tool("ipset",'#!'+os.sys.executable+'\nimport json,os,sys\nwith open(os.environ["IPSET_LOG"],"a") as f:f.write(json.dumps({"args":sys.argv[1:],"stdin":sys.stdin.read() if "restore" in sys.argv else ""})+"\\n")\n')
        planner=self.run/'router-agent'
        planner.write_text('#!/bin/sh\nprintf "create CDX_RLY4 hash:ip,port family inet timeout 3600\\nflush CDX_RLY4\\nadd CDX_RLY4 192.0.2.1,tcp:443 timeout 3600\\ncreate CDX_RLY6 hash:ip,port family inet6 timeout 3600\\nflush CDX_RLY6\\n" > "'+str(self.run/'relay-endpoints.ipset')+'"\n');planner.chmod(0o700)
        r=self._command('relay-refresh',IPSET_LOG=str(log));self.assertEqual(r.returncode,0,r.stderr)
        records=[json.loads(line) for line in log.read_text().splitlines()]
        self.assertEqual(records[0]['args'],['restore','-exist'])
        self.assertIn('flush CDX_RLY4N',records[0]['stdin']);self.assertNotIn('flush CDX_RLY4\n',records[0]['stdin'])
        self.assertEqual(records[1]['args'],['swap','CDX_RLY4N','CDX_RLY4'])
        self.assertEqual(records[2]['args'],['swap','CDX_RLY6N','CDX_RLY6'])

    def test_successful_enable_publishes_confirmed_state(self):
        result = self._enable()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.run / "enabled").read_text(), "1\n")
        self.assertEqual((self.data / "desired-enabled").read_text(), "1\n")
        self.assertTrue((self.run / "bypass-active").exists())
        for family in ("iptables", "ip6tables"):
            self.assertEqual((self.proc / ("bridge-nf-call-" + family)).read_text(), "0\n")

    def test_test_device_confirmation_cannot_persist_full_lan_enable(self):
        scope = "02:11:22:33:44:55"
        pending = self._command("enable-pending", scope)
        self.assertEqual(pending.returncode, 0, pending.stderr)
        confirmed = self._command("confirm")
        self.assertEqual(confirmed.returncode, 0, confirmed.stderr)
        self.assertEqual((self.run / "scope").read_text().strip(), scope)
        self.assertEqual((self.data / "desired-enabled").read_text(), "0\n")

    def test_direct_mode_can_enable_without_foreign_connectivity(self):
        (self.data / "config.json").write_text('{"mode":"direct"}')
        self._tool("curl", '#!/bin/sh\ncase " $* " in *" -w "*) case " $* " in *baidu.com*) printf 200;; *) exit 1;; esac;; esac\nexit 0\n')
        result=self._enable()
        self.assertEqual(result.returncode,0,result.stdout+result.stderr)
        self.assertEqual((self.data/'desired-enabled').read_text(),'1\n')

    def test_sigterm_during_enable_cannot_publish_active_state(self):
        result = self._enable(REVIEW_SIGNAL_ON_ROUTE="1")
        self.assertTrue((self.root / "signal-sent").exists(), "signal injection did not occur")
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self._assert_disabled()

    def test_confirmation_failure_is_fail_closed(self):
        result = self._enable(REVIEW_CONFIRM_FAIL="1")
        self.assertNotEqual(result.returncode, 0, "confirmation failure was ignored")
        self._assert_disabled()

    def test_second_pending_enable_rearms_procd_worker_for_new_token(self):
        # procd start is idempotent for an unchanged running command. A restart
        # is required to take a new token snapshot; execute the real expiry
        # worker against that snapshot with the sleep advanced by the fixture.
        rc=self.root/'fake-rc.common'
        snapshot=self.root/'worker-token'
        rc.write_text('case "$2" in\nrestart) cp "'+str(self.run/'pending-token')+'" "'+str(snapshot)+'";;\nstart) [ -f "'+str(snapshot)+'" ] || cp "'+str(self.run/'pending-token')+'" "'+str(snapshot)+'";;\nstop) rm -f "'+str(snapshot)+'";;\nesac\n')
        self.assertEqual(self._command('enable-pending').returncode,0)
        first=(self.run/'pending-token').read_text()
        self.assertEqual(self._command('enable-pending').returncode,0)
        self.assertNotEqual(first,(self.run/'pending-token').read_text())
        worker=(ROOT/'scripts/rollback-worker.sh').read_text()
        worker=worker.replace('RUN=/tmp/codex-mihomo','RUN='+str(self.run))
        worker=worker.replace('token=$(cat "$RUN/pending-token")','token=$(cat "'+str(snapshot)+'")')
        worker=worker.replace('sleep 120',': # fixture advances the timer')
        worker=worker.replace('/data/codex-router/routerctl.sh',str(self.script))
        expiry=self.root/'expire.sh';expiry.write_text(worker)
        env=dict(os.environ,PATH=str(self.tools)+':'+os.environ['PATH'],REVIEW_SIGNAL_MARKER=str(self.root/'signal-sent'))
        subprocess.run(['/bin/sh',str(expiry)],env=env,check=True)
        self._assert_disabled()


if __name__ == "__main__":
    unittest.main()
