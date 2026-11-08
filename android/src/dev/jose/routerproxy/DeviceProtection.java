package dev.jose.routerproxy;
final class DeviceProtection {
 static String label(boolean configured,boolean active){
  if(configured)return active?"直连保护已生效":"已保存直连 · 下次开启生效";
  return active?"当前直连 · 下次开启恢复分流":"跟随代理规则";
 }
}
