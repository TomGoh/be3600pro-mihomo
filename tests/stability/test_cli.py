import http.server
import json
import os
import socket
import struct
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

    def test_udp_cli_identity_matches_offline_capture_analysis(self):
        from test_pcap import capture
        peer=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);peer.bind(('127.0.0.1',0));peer.settimeout(.1)
        address=peer.getsockname();frames=[];stop=threading.Event()
        def frame(src,dst,data):
            udp=struct.pack('!HHHH',src[1],dst[1],8+len(data),0)+data
            ip=struct.pack('!BBHHHBBH',0x45,0,20+len(udp),1,0,64,17,0)+socket.inet_aton(src[0])+socket.inet_aton(dst[0])
            return bytes(12)+b'\x08\x00'+ip+udp
        def echo():
            while not stop.is_set():
                try:data,source=peer.recvfrom(2048)
                except socket.timeout:continue
                frames.extend([frame(source,address,data),frame(address,source,data)]);peer.sendto(data,source)
        thread=threading.Thread(target=echo);thread.start()
        try:
            with tempfile.TemporaryDirectory() as td:
                log=Path(td)/'udp.jsonl';pcap=Path(td)/'udp.pcap'
                with log.open('w') as f:r=subprocess.run([PROBE,'-run-live','-mode','udp','-address','127.0.0.1:'+str(address[1]),'-duration','2s','-packets','3','-interval','1ms'],stdout=f,stderr=subprocess.PIPE,text=True,timeout=4)
                self.assertEqual(r.returncode,0,r.stderr)
                stop.set();thread.join(timeout=2)
                pcap.write_bytes(capture([f[:160] for f in frames],original_sizes=[len(f) for f in frames]))
                report=subprocess.run([sys.executable,str(ROOT/'scripts/analyze-stability.py'),'pcap','--client-log',str(log),'--capture',str(pcap),'--peer-ip','127.0.0.1','--peer-port',str(address[1]),'--capture-drops','0'],capture_output=True,text=True,timeout=4)
                self.assertEqual(report.returncode,0,report.stdout+report.stderr)
                result=json.loads(report.stdout);self.assertEqual(result['received'],3);self.assertTrue(result['attributionVerified'])
        finally:stop.set();thread.join(timeout=2);peer.close()


class AnalyzerCLI(unittest.TestCase):
    def test_bad_json_is_an_inconclusive_error_not_a_zero_failure_pass(self):
        with tempfile.TemporaryDirectory() as td:
            p=Path(td)/'broken';p.write_text('{"event":')
            r=subprocess.run([sys.executable,str(ROOT/'scripts/analyze-stability.py'),'http',str(p)],capture_output=True,text=True)
            self.assertEqual(r.returncode,2)
            self.assertEqual(json.loads(r.stdout)['verdict'],'inconclusive')

if __name__=='__main__':unittest.main()
