import io
import ipaddress
from pathlib import Path
import struct
import sys
import unittest

sys.path.insert(0,str(Path(__file__).resolve().parents[2]/'scripts'))
from diagnostics.pcap import parse_pcap, compare_udp
from diagnostics.records import EvidenceError

NONCE='11'*16

def capture(frames,link=1,endian='<'):
    header=struct.pack(endian+'IHHIIII',0xa1b2c3d4,2,4,0,0,160,link)
    return header+b''.join(struct.pack(endian+'IIII',1,0,len(f),len(f))+f for f in frames)

def packet(reply=False,seq=0,ipv6=False,link=1,nonce=NONCE):
    body=b'cdx-udp-v1'+struct.pack('!II',0,seq)+bytes.fromhex(nonce)+bytes(20)
    src,dst=('192.0.2.2','198.51.100.77') if not ipv6 else ('2001:db8::2','2001:db8::77')
    if reply:src,dst=dst,src
    udp=struct.pack('!HHHH',27051 if reply else 40000,40000 if reply else 27051,8+len(body),0)+body
    if ipv6:ip=struct.pack('!IHBB',6<<28,len(udp),17,64)+ipaddress.ip_address(src).packed+ipaddress.ip_address(dst).packed
    else:ip=struct.pack('!BBHHHBBH',0x45,0,20+len(udp),1,0,64,17,0)+ipaddress.ip_address(src).packed+ipaddress.ip_address(dst).packed
    proto=b'\x86\xdd' if ipv6 else b'\x08\x00'
    prefix=bytes(12)+proto if link==1 else bytes(14)+proto if link==113 else proto+bytes(18)
    return prefix+ip+udp

def clients():return [dict(worker=0,nonce=NONCE,sent=2,received=1,missing=[1],corrupt=0)]

class PcapTests(unittest.TestCase):
    def test_pairing_distinguishes_before_entry_from_missing_reply(self):
        parsed=parse_pcap(io.BytesIO(capture([packet(),packet(True)])),peer_ip='198.51.100.77',peer_port=27051)
        d=compare_udp(clients(),parsed,capture_drops=0)
        self.assertEqual(d['missingBeforeCapture'],1)
        self.assertEqual(d['requestWithoutReply'],0)
        parsed=parse_pcap(io.BytesIO(capture([packet(),packet(True),packet(seq=1)])),peer_ip='198.51.100.77',peer_port=27051)
        self.assertEqual(compare_udp(clients(),parsed,capture_drops=0)['requestWithoutReply'],1)

    def test_unknown_capture_loss_prevents_attribution(self):
        parsed=parse_pcap(io.BytesIO(capture([packet(),packet(True)])),peer_ip='198.51.100.77',peer_port=27051)
        for lost in [None,1]:
            self.assertFalse(compare_udp(clients(),parsed,capture_drops=lost)['attributionVerified'])

    def test_ipv6_linux_cooked_and_big_endian_captures(self):
        for ipv6 in [False,True]:
            for link in [1,113,276]:
                raw=capture([packet(ipv6=ipv6,link=link),packet(True,ipv6=ipv6,link=link)],link=link,endian='>')
                p=parse_pcap(io.BytesIO(raw),peer_ip='2001:db8::77' if ipv6 else '198.51.100.77',peer_port=27051)
                self.assertEqual(len(p['requests']),1);self.assertEqual(len(p['replies']),1)

    def test_duplicate_and_other_run_packets_are_not_successes(self):
        raw=capture([packet(),packet(),packet(True),packet(seq=1,nonce='22'*16)])
        p=parse_pcap(io.BytesIO(raw),peer_ip='198.51.100.77',peer_port=27051)
        d=compare_udp(clients(),p,capture_drops=0)
        self.assertEqual(d['duplicateRequests'],1)
        self.assertEqual(d['missingBeforeCapture'],1)

    def test_truncated_record_and_unsupported_link_are_rejected(self):
        for raw in [capture([packet()])[:-2],capture([],link=999),b'pcap']:
            with self.assertRaises(EvidenceError):parse_pcap(io.BytesIO(raw),peer_ip='198.51.100.77',peer_port=27051)

    def test_truncated_probe_identity_prevents_attribution(self):
        f=packet()[:65]
        p=parse_pcap(io.BytesIO(capture([f])),peer_ip='198.51.100.77',peer_port=27051)
        self.assertFalse(compare_udp(clients(),p,capture_drops=0)['attributionVerified'])

    def test_empty_and_inconsistent_client_records_cannot_pass(self):
        p=parse_pcap(io.BytesIO(capture([])),peer_ip='198.51.100.77',peer_port=27051)
        self.assertEqual(compare_udp([],p,capture_drops=0)['verdict'],'inconclusive')
        bad=clients();bad[0]['missing']=[1,1]
        with self.assertRaises(EvidenceError):compare_udp(bad,p,capture_drops=0)

    def test_vlan_and_ipv6_hop_extension_keep_identity_pairing(self):
        vlan=packet()[:12]+b'\x81\x00\x00\x01'+packet()[12:]
        p=parse_pcap(io.BytesIO(capture([vlan])),peer_ip='198.51.100.77',peer_port=27051)
        self.assertEqual(len(p['requests']),1)
        original=bytearray(packet(ipv6=True));original[20]=0
        length=struct.unpack('!H',original[18:20])[0]+8;original[18:20]=struct.pack('!H',length)
        extended=bytes(original[:54])+bytes([17,0])+bytes(6)+bytes(original[54:])
        p=parse_pcap(io.BytesIO(capture([extended])),peer_ip='2001:db8::77',peer_port=27051)
        self.assertEqual(len(p['requests']),1)

    def test_noninitial_fragment_disables_absence_attribution(self):
        frame=bytearray(packet());frame[20:22]=b'\x00\x01'
        p=parse_pcap(io.BytesIO(capture([bytes(frame)])),peer_ip='198.51.100.77',peer_port=27051)
        self.assertFalse(compare_udp(clients(),p,capture_drops=0)['attributionVerified'])

    def test_incomplete_udp_worker_cannot_pass_as_a_smaller_test(self):
        p=parse_pcap(io.BytesIO(capture([packet(),packet(True)])),peer_ip='198.51.100.77',peer_port=27051)
        rows=[dict(worker=0,nonce=NONCE,sent=1,received=1,missing=[],corrupt=0,expectedPackets=2)]
        self.assertEqual(compare_udp(rows,p,capture_drops=0)['verdict'],'inconclusive')

if __name__=='__main__':unittest.main()
