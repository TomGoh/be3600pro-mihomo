package dev.jose.routerproxy;
import javax.net.ssl.*;
import java.security.cert.X509Certificate;
import java.security.cert.CertificateException;
import java.security.MessageDigest;
import java.io.*;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import org.json.JSONObject;
final class ApiClient {
 interface Progress {void update(int percent);}
 static final long CORE_LENGTH=CoreRelease.ARCHIVE_BYTES;
 private final PairingStore.Pairing pairing;
 private final SSLSocketFactory sockets;
 private volatile String intentEpoch;
 private final ThreadLocal<String> operationEpoch=new ThreadLocal<>();
 String intentSnapshot(){return intentEpoch;}
 void beginOperation(String epoch){if(epoch==null)operationEpoch.remove();else operationEpoch.set(epoch);}
 void endOperation(){operationEpoch.remove();}
 ApiClient(PairingStore.Pairing p) throws Exception {
  pairing=p;
  X509TrustManager tm=new X509TrustManager(){
   public X509Certificate[] getAcceptedIssuers(){return new X509Certificate[0];}
   public void checkClientTrusted(X509Certificate[] chain,String auth) throws CertificateException {throw new CertificateException("Client certificates unsupported");}
   public void checkServerTrusted(X509Certificate[] chain,String auth) throws CertificateException {
    SecurityRules.verifyLeaf(chain,pairing.pin);
   }
  };
  SSLContext ctx=SSLContext.getInstance("TLS");ctx.init(null,new TrustManager[]{tm},null);sockets=ctx.getSocketFactory();
 }
 JSONObject json(String method,String path,JSONObject body) throws Exception {byte[] data=body==null?null:body.toString().getBytes(StandardCharsets.UTF_8);return request(method,path,data==null?null:new ByteArrayInputStream(data),data==null?0:data.length,"application/json; charset=utf-8",null);}
 JSONObject recover(InputStream asset,Progress p) throws Exception {return request("POST","/api/core",asset,CORE_LENGTH,"application/octet-stream",p);}
 private JSONObject request(String method,String path,InputStream body,long size,String contentType,Progress progress) throws Exception {
  if(!path.startsWith("/api/")||path.contains("\r")||path.contains("\n"))throw new IllegalArgumentException("请求地址无效");
  String sentEpoch=operationEpoch.get();if(sentEpoch==null)sentEpoch=intentEpoch;
  HttpsURLConnection conn=(HttpsURLConnection)new URL(pairing.url+path).openConnection();
  try {
   conn.setSSLSocketFactory(sockets); // Default platform hostname verifier remains active, including IP SAN checks.
   conn.setInstanceFollowRedirects(false);conn.setConnectTimeout(12000);conn.setReadTimeout(progress==null?120000:180000);
   conn.setRequestMethod(method);if(sentEpoch!=null)conn.setRequestProperty("X-Intent-Epoch",sentEpoch);conn.setRequestProperty("Authorization","Bearer "+pairing.token);conn.setRequestProperty("Accept","application/json");conn.setUseCaches(false);
   if(body!=null){conn.setDoOutput(true);conn.setRequestProperty("Content-Type",contentType);conn.setFixedLengthStreamingMode(size);
    try(OutputStream out=conn.getOutputStream()){byte[] b=new byte[32768];long sent=0;int last=-1,n;while((n=body.read(b))!=-1){if(Thread.currentThread().isInterrupted())throw new IOException("cancelled");out.write(b,0,n);sent+=n;int pct=(int)(sent*100/size);if(progress!=null&&pct!=last){last=pct;progress.update(pct);}}if(sent!=size)throw new IOException("Asset length mismatch");}
   }
   int code=conn.getResponseCode();InputStream source=code>=200&&code<300?conn.getInputStream():conn.getErrorStream();
   String raw="";if(source!=null)try(InputStream in=source){raw=new String(readBounded(in,2*1024*1024),StandardCharsets.UTF_8);}
   JSONObject result=raw.trim().isEmpty()?new JSONObject():new JSONObject(raw);
   String received=result.has("intentEpoch")?result.optString("intentEpoch"):conn.getHeaderField("X-Intent-Epoch");
   if(received!=null&&(intentEpoch==null||java.util.Objects.equals(intentEpoch,sentEpoch)||path.equals("/api/emergency-stop")))intentEpoch=received;
   if(code<200||code>=300){String msg=code==401?"配对已失效，请重新导入配对文件。":result.optString("error","请求未完成（HTTP "+code+"）");throw new UserError(code,msg.replaceAll("https?://[^\\s\\\"<>]+","[地址已隐藏]"));}
   return result;
  } finally {conn.disconnect();}
 }
 static byte[] readBounded(InputStream in,int limit) throws IOException {ByteArrayOutputStream out=new ByteArrayOutputStream();byte[] b=new byte[8192];int n;while((n=in.read(b))!=-1){if(out.size()+n>limit)throw new IOException("文件或响应过大");out.write(b,0,n);}return out.toByteArray();}
 static final class UserError extends Exception {final int status;UserError(String message){this(0,message);}UserError(int code,String message){super(message);status=code;}}
 static String friendly(Exception e){
  if(e instanceof UserError || e instanceof IllegalArgumentException)return e.getMessage();
  if(e instanceof SSLException || e instanceof CertificateException)return "路由器证书校验失败。请检查手机时间，或重新导入可信配对文件。";
  if(e instanceof java.net.SocketTimeoutException)return "连接超时，请确认手机连接家庭 Wi-Fi 后重试。";
  return "暂时无法完成操作。请检查家庭 Wi-Fi 和路由器状态，再点击刷新。";
 }
}
