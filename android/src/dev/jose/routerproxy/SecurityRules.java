package dev.jose.routerproxy;
import java.net.URI;
import java.net.InetAddress;
import java.net.Inet6Address;
import java.net.URLEncoder;
import java.security.MessageDigest;
import java.nio.charset.StandardCharsets;
public final class SecurityRules {
 private SecurityRules() {}
 public static String endpoint(String input) {
  try {
   URI u=new URI(input);
   if(!"https".equals(u.getScheme()) || u.getHost()==null || u.getRawUserInfo()!=null || u.getRawQuery()!=null || u.getRawFragment()!=null || !(u.getRawPath().isEmpty() || "/".equals(u.getRawPath())) || u.getPort()==0 || u.getPort()>65535 || u.getPort() < -1) throw new Exception();
   String h=u.getHost(); boolean ok=false;
   if(h.startsWith("[") && h.endsWith("]")) {
    h=h.substring(1,h.length()-1);
    if(!h.matches("[0-9a-fA-F:]+")) throw new Exception();
    InetAddress a=InetAddress.getByName(h);
    ok=a instanceof Inet6Address && (a.getAddress()[0]&254)==252;
   } else if(h.matches("[0-9.]+")) {
    String[] p=h.split("\\.",-1); if(p.length!=4) throw new Exception();
    int[] n=new int[4]; for(int i=0;i<4;i++){if(p[i].isEmpty() || p[i].length()>3 || (p[i].length()>1&&p[i].startsWith("0")))throw new Exception(); n[i]=Integer.parseInt(p[i]); if(n[i]>255)throw new Exception();}
    ok=n[0]==10 || (n[0]==172&&n[1]>=16&&n[1]<=31) || (n[0]==192&&n[1]==168);
   }
   if(!ok)throw new Exception();
   return "https://"+u.getRawAuthority();
  } catch(Exception e) { throw new IllegalArgumentException("路由器地址必须为家庭局域网 IP 的 HTTPS 地址，且不能包含路径、账号或参数。"); }
 }
 public static void pairing(int schema,String url,String token,String pin) {
  if(schema!=1)throw new IllegalArgumentException("不支持此配对文件版本。");
  endpoint(url);
  if(token==null || !token.matches("[A-Za-z0-9._~+/=-]{16,512}"))throw new IllegalArgumentException("配对令牌格式无效，请重新导出配对文件。");
  if(pin==null || !pin.matches("[0-9a-f]{64}"))throw new IllegalArgumentException("证书指纹无效，请重新导出配对文件。");
 }
 public static void verifyLeaf(java.security.cert.X509Certificate[] chain,String pin) throws java.security.cert.CertificateException {
  if(chain==null || chain.length==0 || chain[0]==null)throw new java.security.cert.CertificateException("Missing server certificate");
  if(pin==null || !pin.matches("[0-9a-f]{64}"))throw new java.security.cert.CertificateException("Invalid certificate fingerprint");
  chain[0].checkValidity();
  if(!MessageDigest.isEqual(sha256(chain[0].getEncoded()).getBytes(StandardCharsets.US_ASCII),pin.getBytes(StandardCharsets.US_ASCII)))throw new java.security.cert.CertificateException("Certificate fingerprint mismatch");
 }
 public static String sha256(byte[] bytes) {
  try {byte[] digest=MessageDigest.getInstance("SHA-256").digest(bytes); StringBuilder s=new StringBuilder();for(byte b:digest)s.append(String.format(java.util.Locale.ROOT,"%02x",b&255));return s.toString();}
  catch(Exception e){throw new IllegalStateException("SHA-256 unavailable",e);}
 }
 public static String segment(String s) {try{return URLEncoder.encode(s,"UTF-8").replace("+","%20");}catch(Exception e){throw new IllegalArgumentException("名称编码失败");}}
}
