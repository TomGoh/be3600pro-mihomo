import http.server
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import unittest

ROOT=Path(__file__).resolve().parents[2]
PROBE=os.environ.get('NETPROBE_BINARY')


@unittest.skipUnless(PROBE,'build netprobe and set NETPROBE_BINARY for loopback CLI integration')
class CLIIntegration(unittest.TestCase):
    def exchange(self,status):
        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version='HTTP/1.1'
            def do_GET(self):
                self.send_response(status);self.send_header('Content-Length','2');self.end_headers();self.wfile.write(b'ok')
            def log_message(self,*args):pass
        server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler)
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            with tempfile.TemporaryDirectory() as td:
                log=Path(td)/'probe.jsonl'
                with log.open('w') as f:r=subprocess.run([PROBE,'-run-live','-url','http://127.0.0.1:'+str(server.server_port),'-requests','3','-interval','1ms','-duration','2s'],stdout=f,stderr=subprocess.PIPE,text=True,timeout=5)
                analyzer=[sys.executable,str(ROOT/'scripts/analyze-stability.py'),'http',str(log),'--minimum-samples','3']
                report=subprocess.run(analyzer,capture_output=True,text=True,timeout=5)
                original=json.loads(report.stdout)
                lines=log.read_text().splitlines();log.write_text('\n'.join(lines[:-1])+'\n')
                incomplete=subprocess.run(analyzer,capture_output=True,text=True,timeout=5)
                return r,report,original,incomplete
        finally:server.shutdown();server.server_close();thread.join(timeout=2)

    def test_local_http_cli_and_analyzer_pass_and_detect_missing_footer(self):
        probe,report,data,incomplete=self.exchange(200)
        self.assertEqual(probe.returncode,0,probe.stderr);self.assertEqual(report.returncode,0,report.stderr)
        self.assertEqual(data['completed'],3);self.assertEqual(data['newConnections'],1)
        self.assertEqual(incomplete.returncode,2,incomplete.stdout)

    def test_local_http_failure_is_nonzero_in_probe_and_report(self):
        probe,report,data,_=self.exchange(500)
        self.assertEqual(probe.returncode,1);self.assertEqual(report.returncode,1);self.assertEqual(data['failed'],3)


class AnalyzerCLI(unittest.TestCase):
    def test_bad_json_is_an_inconclusive_error_not_a_zero_failure_pass(self):
        with tempfile.TemporaryDirectory() as td:
            p=Path(td)/'broken';p.write_text('{"event":')
            r=subprocess.run([sys.executable,str(ROOT/'scripts/analyze-stability.py'),'http',str(p)],capture_output=True,text=True)
            self.assertEqual(r.returncode,2)
            self.assertEqual(json.loads(r.stdout)['verdict'],'inconclusive')

if __name__=='__main__':unittest.main()
