package dev.jose.routerproxy;
import java.nio.charset.StandardCharsets;
public final class SecurityRulesTest {
 static int count;
 interface Checked { void run() throws Exception; }
 static void accept(Checked c) throws Exception { c.run(); count++; }
 static void reject(Checked c) throws Exception { try { c.run(); } catch (IllegalArgumentException e) { count++; return; } throw new AssertionError("unsafe value accepted"); }
 static void rejectCert(Checked c) throws Exception {try{c.run();}catch(java.security.cert.CertificateException e){count++;return;}throw new AssertionError("unsafe certificate accepted");}
 public static void main(String[] args) throws Exception {
  for(String s:new String[]{"https://192.168.31.1:9443","https://10.0.0.1","https://172.16.0.1:443/","https://172.31.255.254","https://[fd00::1]:9443"}) accept(()->SecurityRules.endpoint(s));
  for(String s:new String[]{"http://192.168.31.1","https://8.8.8.8","https://127.0.0.1","https://169.254.1.2","https://172.32.0.1","https://router.local","https://192.168.31.1.evil.example","https://user@192.168.31.1","https://192.168.31.1/path","https://192.168.31.1?token=x","https://192.168.31.1#x","https://192.168.031.1","https://[::1]","https://[fe80::1%25en0]","https://192.168.31.1:0","https://192.168.31.1:65536"}) reject(()->SecurityRules.endpoint(s));
  accept(()->SecurityRules.pairing(1,"https://192.168.31.1:9443","abcdefghijklmnopqrstuvwxyz123456","0".repeat(64)));
  reject(()->SecurityRules.pairing(2,"https://192.168.31.1","abcdefghijklmnopqrstuvwxyz123456","0".repeat(64)));
  for(String t:new String[]{"","short","abcd\r\nHeader: bad","a".repeat(513),"contains space----------------"}) reject(()->SecurityRules.pairing(1,"https://192.168.31.1",t,"0".repeat(64)));
  for(String p:new String[]{"","0".repeat(63),"g".repeat(64),"A".repeat(64)}) reject(()->SecurityRules.pairing(1,"https://192.168.31.1","abcdefghijklmnopqrstuvwxyz123456",p));
  if(!SecurityRules.sha256("abc".getBytes(StandardCharsets.UTF_8)).equals("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")) throw new AssertionError("digest mismatch"); count++;
  if(!SecurityRules.segment("香港 🚀/A+B").equals("%E9%A6%99%E6%B8%AF%20%F0%9F%9A%80%2FA%2BB")) throw new AssertionError("path injection/encoding"); count++;
  java.security.cert.CertificateFactory factory=java.security.cert.CertificateFactory.getInstance("X.509");
  java.security.cert.X509Certificate valid,expired;
  try(java.io.InputStream in=new java.io.FileInputStream(args[0]+"/valid.pem")){valid=(java.security.cert.X509Certificate)factory.generateCertificate(in);}
  try(java.io.InputStream in=new java.io.FileInputStream(args[0]+"/expired.pem")){expired=(java.security.cert.X509Certificate)factory.generateCertificate(in);}
  java.security.cert.X509Certificate[] good={valid};
  String pin=SecurityRules.sha256(valid.getEncoded());
  SecurityRules.verifyLeaf(good,pin);count++;
  rejectCert(()->SecurityRules.verifyLeaf(good,"0".repeat(64)));
  rejectCert(()->SecurityRules.verifyLeaf(good,pin.toUpperCase(java.util.Locale.ROOT)));
  rejectCert(()->SecurityRules.verifyLeaf(null,pin));
  rejectCert(()->SecurityRules.verifyLeaf(new java.security.cert.X509Certificate[0],pin));
  rejectCert(()->SecurityRules.verifyLeaf(new java.security.cert.X509Certificate[]{null},pin));
  rejectCert(()->SecurityRules.verifyLeaf(new java.security.cert.X509Certificate[]{expired},SecurityRules.sha256(expired.getEncoded())));
  System.out.println("PASS: "+count+" security boundary checks (including real X.509 pin and expiry)");
 }
}
