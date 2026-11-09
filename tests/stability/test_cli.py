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

    def test_measurement_cutoff_cli_preserves_worker_coverage_without_samples(self):
        for with_sample in [True,False]:
            with self.subTest(with_sample=with_sample):
                release=threading.Event();lock=threading.Lock();calls=0
                class Handler(http.server.BaseHTTPRequestHandler):
                    protocol_version='HTTP/1.1'
                    def do_GET(self):
                        nonlocal calls
                        with lock:
                            calls+=1;first=calls==1
                        if with_sample and first:
                            self.send_response(200);self.send_header('Content-Length','2');self.end_headers();self.wfile.write(b'ok')
                        else:release.wait(5)
                    def log_message(self,*args):pass
                server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler)
                thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
                try:
                    with tempfile.TemporaryDirectory() as td:
                        log=Path(td)/'probe.jsonl'
                        with log.open('w') as f:
                            probe=subprocess.run([PROBE,'-run-live','-url','http://127.0.0.1:'+str(server.server_port),
                                '-workers','2','-requests','2','-interval','0','-duration','1s'],
                                stdout=f,stderr=subprocess.PIPE,text=True,timeout=4)
                        rows=[json.loads(line) for line in log.read_text().splitlines()]
                        stops=[row for row in rows if row['event']=='worker-stop']
                        self.assertEqual(len(stops),2)
                        self.assertEqual({row['worker'] for row in stops},{0,1})
                        self.assertTrue(all(row['stopReason']=='duration' and not row['ok'] and row['status']==0 and row['bytes']==0 for row in stops))
                        report=subprocess.run([sys.executable,str(ROOT/'scripts/analyze-stability.py'),'http',str(log)],
                            capture_output=True,text=True,timeout=4)
                        data=json.loads(report.stdout)
                        self.assertEqual(probe.returncode,0 if with_sample else 1,probe.stderr)
                        self.assertEqual(report.returncode,0 if with_sample else 1,report.stdout+report.stderr)
                        self.assertEqual(data['responseSamples'],1 if with_sample else 0)
                        self.assertEqual(data['transferredBytes'],2 if with_sample else 0)
                        self.assertEqual(data['plannedStops'],0)
                        if not with_sample:self.assertEqual(data['longestMeasuredRequestSeconds'],0)
                finally:
                    release.set();server.shutdown();server.server_close();thread.join(timeout=2)

    def test_stream_duration_stop_still_passes_offline_analysis(self):
        release=threading.Event()
        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version='HTTP/1.1'
            def do_GET(self):
                self.send_response(200);self.send_header('Content-Length','100');self.end_headers()
                self.wfile.write(b'x');self.wfile.flush();release.wait(5)
            def log_message(self,*args):pass
        server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler)
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            with tempfile.TemporaryDirectory() as td:
                log=Path(td)/'stream.jsonl'
                with log.open('w') as f:
                    probe=subprocess.run([PROBE,'-run-live','-url','http://127.0.0.1:'+str(server.server_port),
                        '-stream','-requests','1','-duration','1s'],stdout=f,stderr=subprocess.PIPE,text=True,timeout=4)
                report=subprocess.run([sys.executable,str(ROOT/'scripts/analyze-stability.py'),'http',str(log)],
                    capture_output=True,text=True,timeout=4)
                self.assertEqual(probe.returncode,0,probe.stderr)
                self.assertEqual(report.returncode,0,report.stdout+report.stderr)
                data=json.loads(report.stdout)
                self.assertEqual(data['responseSamples'],1);self.assertEqual(data['plannedStops'],1)
                self.assertEqual(data['transferredBytes'],1)
        finally:release.set();server.shutdown();server.server_close();thread.join(timeout=2)

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
