package dev.jose.routerproxy;
import android.content.Context;
import android.content.SharedPreferences;
import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;
import org.json.JSONObject;
import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;
import java.security.KeyStore;
import java.nio.charset.StandardCharsets;
final class PairingStore {
 static final class Pairing {
  final String url,token,pin;
  Pairing(String json) throws Exception {
   JSONObject o=new JSONObject(json);
   SecurityRules.pairing(o.getInt("schema"),o.getString("routerUrl"),o.getString("token"),o.getString("certificateSha256"));
   url=SecurityRules.endpoint(o.getString("routerUrl")); token=o.getString("token");pin=o.getString("certificateSha256");
  }
  String json() throws Exception {return new JSONObject().put("schema",1).put("routerUrl",url).put("token",token).put("certificateSha256",pin).toString();}
 }
 private static final String ALIAS="router-proxy-pairing-v1";
 private final SharedPreferences prefs;
 PairingStore(Context c){prefs=c.getSharedPreferences("secure_pairing",Context.MODE_PRIVATE);}
 private SecretKey key() throws Exception {
  KeyStore ks=KeyStore.getInstance("AndroidKeyStore");ks.load(null);
  if(!ks.containsAlias(ALIAS)) {KeyGenerator g=KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES,"AndroidKeyStore");g.init(new KeyGenParameterSpec.Builder(ALIAS,KeyProperties.PURPOSE_ENCRYPT|KeyProperties.PURPOSE_DECRYPT).setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build());g.generateKey();}
  return (SecretKey)ks.getKey(ALIAS,null);
 }
 synchronized Pairing load() throws Exception {
  String value=prefs.getString("ciphertext",null);if(value==null)return null;
  Cipher c=Cipher.getInstance("AES/GCM/NoPadding");c.init(Cipher.DECRYPT_MODE,key(),new GCMParameterSpec(128,Base64.decode(prefs.getString("iv",""),Base64.NO_WRAP)));
  return new Pairing(new String(c.doFinal(Base64.decode(value,Base64.NO_WRAP)),StandardCharsets.UTF_8));
 }
 synchronized void save(Pairing p) throws Exception {
  Cipher c=Cipher.getInstance("AES/GCM/NoPadding");c.init(Cipher.ENCRYPT_MODE,key());
  String data=Base64.encodeToString(c.doFinal(p.json().getBytes(StandardCharsets.UTF_8)),Base64.NO_WRAP);
  if(!prefs.edit().putString("ciphertext",data).putString("iv",Base64.encodeToString(c.getIV(),Base64.NO_WRAP)).commit())throw new Exception("无法保存配对");
 }
 synchronized void clear() throws Exception {if(!prefs.edit().clear().commit())throw new Exception("无法清除配对");KeyStore ks=KeyStore.getInstance("AndroidKeyStore");ks.load(null);ks.deleteEntry(ALIAS);}
}
