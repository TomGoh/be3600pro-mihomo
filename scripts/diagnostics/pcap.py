"""Classic PCAP UDP identity pairing. Never infers radio loss from a capture alone."""
import collections
import ipaddress
import struct

from .records import EvidenceError,number

MAGIC=b'cdx-udp-v1'


def parse_pcap(source,peer_ip,peer_port):
    peer=ipaddress.ip_address(peer_ip)
    if isinstance(peer_port,bool) or not isinstance(peer_port,int) or not 1<=peer_port<=65535:raise EvidenceError('invalid peer port')
    head=source.read(24)
    variants={b'\xd4\xc3\xb2\xa1':'<',b'\xa1\xb2\xc3\xd4':'>',b'\x4d\x3c\xb2\xa1':'<',b'\xa1\xb2\x3c\x4d':'>'}
    if len(head)!=24 or head[:4] not in variants:raise EvidenceError('classic PCAP header required; PCAPNG is not supported')
    endian=variants[head[:4]]
    _,major,minor,_,_,snap,link=struct.unpack(endian+'IHHIIII',head)
    if (major,minor)!=(2,4) or not 1<=snap<=262144 or link not in (1,113,276):raise EvidenceError('unsupported PCAP version, link or snap length')
    result={'requests':collections.Counter(),'replies':collections.Counter(),'truncatedIdentities':0,'fragmentsSkipped':0}
    total=24
    while True:
        header=source.read(16)
        if not header:break
        if len(header)!=16:raise EvidenceError('truncated packet record header')
        _,_,size,original=struct.unpack(endian+'IIII',header)
        if size>snap or size>original:raise EvidenceError('invalid captured packet length')
        total+=16+size
        if total>64*1024*1024:raise EvidenceError('capture exceeds 64 MiB analysis budget')
        frame=source.read(size)
        if len(frame)!=size:raise EvidenceError('truncated packet record')
        offset={1:14,113:16,276:20}[link]
        if len(frame)<offset:raise EvidenceError('truncated link header')
        protocol=struct.unpack('!H',frame[{1:12,113:14,276:0}[link]:{1:14,113:16,276:2}[link]])[0]
        while protocol in (0x8100,0x88a8) and link==1:
            if len(frame)<offset+4:raise EvidenceError('truncated VLAN header')
            protocol=struct.unpack('!H',frame[offset+2:offset+4])[0];offset+=4
        if protocol==0x0800:
            if len(frame)<offset+20:raise EvidenceError('truncated IPv4 header')
            length=(frame[offset]&15)*4
            if frame[offset]>>4!=4 or length<20 or len(frame)<offset+length:raise EvidenceError('invalid IPv4 header')
            declared=struct.unpack('!H',frame[offset+2:offset+4])[0]
            ip_end=offset+declared
            if declared<length or ip_end>original:raise EvidenceError('invalid IPv4 declared length')
            if struct.unpack('!H',frame[offset+6:offset+8])[0]&0x3fff:
                result['fragmentsSkipped']+=1;continue
            next_header=frame[offset+9]
            src=ipaddress.ip_address(frame[offset+12:offset+16]);dst=ipaddress.ip_address(frame[offset+16:offset+20]);offset+=length
        elif protocol==0x86dd:
            if len(frame)<offset+40 or frame[offset]>>4!=6:raise EvidenceError('truncated or invalid IPv6 header')
            declared=struct.unpack('!H',frame[offset+4:offset+6])[0]
            ip_end=offset+40+declared
            if ip_end>original:raise EvidenceError('invalid IPv6 declared length')
            next_header=frame[offset+6];src=ipaddress.ip_address(frame[offset+8:offset+24]);dst=ipaddress.ip_address(frame[offset+24:offset+40]);offset+=40
            for _ in range(8):
                if next_header not in (0,43,44,51,60):break
                if len(frame)<offset+8 or ip_end<offset+8:raise EvidenceError('truncated IPv6 extension')
                current=next_header;next_header=frame[offset]
                if current==44:
                    result['fragmentsSkipped']+=1;next_header=-1;break
                else:length=(frame[offset+1]+(2 if current==51 else 1))*(4 if current==51 else 8)
                if len(frame)<offset+length or ip_end<offset+length:raise EvidenceError('truncated IPv6 extension')
                offset+=length
            if next_header in (0,43,44,51,60):raise EvidenceError('IPv6 extension chain exceeds analysis bound')
        else:continue
        if next_header!=17 or peer not in (src,dst):continue
        if len(frame)<offset+8 or ip_end<offset+8:raise EvidenceError('truncated UDP header')
        sport,dport,udp_length,_=struct.unpack('!HHHH',frame[offset:offset+8])
        if udp_length<8 or offset+udp_length>ip_end:raise EvidenceError('invalid UDP length')
        body=frame[offset+8:offset+udp_length]
        if not body.startswith(MAGIC):continue
        if len(body)<34:
            result['truncatedIdentities']+=1;continue
        worker,sequence=struct.unpack('!II',body[10:18]);key=(body[18:34].hex(),worker,sequence)
        if dst==peer and dport==peer_port:result['requests'][key]+=1
        elif src==peer and sport==peer_port:result['replies'][key]+=1
    return result


def compare_udp(clients,capture,capture_drops=None):
    expected=set();missing=set();received=sent=corrupt=0;owners=set();incomplete=False;client_error=False
    for row in clients:
        for key in ('worker','sent','received','corrupt'):number(row.get(key),key,True)
        nonce=row.get('nonce')
        try:valid=isinstance(nonce,str) and len(nonce)==32 and len(bytes.fromhex(nonce))==16
        except ValueError:valid=False
        if not valid:raise EvidenceError('16-byte run nonce required')
        owner=(nonce,row['worker'])
        if owner in owners:raise EvidenceError('duplicate client worker');
        owners.add(owner)
        lost=row.get('missing')
        if not isinstance(lost,list) or any(isinstance(v,bool) or not isinstance(v,int) or not 0<=v<row['sent'] for v in lost) or len(set(lost))!=len(lost):raise EvidenceError('invalid missing sequences')
        if row['received']+row['corrupt']+len(lost)!=row['sent']:raise EvidenceError('inconsistent client packet counts')
        if 'expectedPackets' in row:
            number(row['expectedPackets'],'expectedPackets',True)
            if not row['sent']<=row['expectedPackets']<=20000:raise EvidenceError('invalid expected packet count')
            incomplete|=row['sent']!=row['expectedPackets']
        if not isinstance(row.get('error',''),str):raise EvidenceError('client error must be a string')
        client_error|=bool(row.get('error'))
        if row['sent']>20000 or len(expected)+row['sent']>20000:raise EvidenceError('client packet budget exceeded')
        expected.update((nonce,row['worker'],i) for i in range(row['sent']));missing.update((nonce,row['worker'],i) for i in lost)
        sent+=row['sent'];received+=row['received'];corrupt+=row['corrupt']
    if capture_drops is not None:number(capture_drops,'capture drops',True)
    valid=capture_drops==0 and not capture['truncatedIdentities'] and not capture['fragmentsSkipped']
    entering=set(capture['requests'])&expected;leaving=set(capture['replies'])&expected
    inconsistent=bool((expected-missing)-(entering&leaving)) if valid else False
    valid=valid and bool(entering or leaving)
    return {'verdict':'fail' if missing or corrupt or client_error else 'inconclusive' if not sent or inconsistent or not valid or incomplete else 'pass',
            'sent':sent,'received':received,'missing':len(missing),'corrupt':corrupt,
            'attributionVerified':valid and not inconsistent and bool(sent),
            'missingBeforeCapture':len(missing-entering),
            'requestWithoutReply':len((missing&entering)-leaving),
            'replyCapturedButNotReceived':len(missing&leaving),
            'duplicateRequests':sum(max(0,capture['requests'][k]-1) for k in expected),
            'duplicateReplies':sum(max(0,capture['replies'][k]-1) for k in expected),
            'captureIntegrityKnown':capture_drops is not None,
            'truncatedIdentities':capture['truncatedIdentities'],
            'boundary':'absence before this capture point; not proof of a particular radio, OS or driver cause'}
