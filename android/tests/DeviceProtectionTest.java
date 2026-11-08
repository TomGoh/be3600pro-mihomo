package dev.jose.routerproxy;
public final class DeviceProtectionTest {
 public static void main(String[] args){
  expect(DeviceProtection.label(true,false),"已保存直连 · 下次开启生效");
  expect(DeviceProtection.label(true,true),"直连保护已生效");
  expect(DeviceProtection.label(false,true),"当前直连 · 下次开启恢复分流");
  expect(DeviceProtection.label(false,false),"跟随代理规则");
  System.out.println("PASS: 4 device protection state checks");
 }
 private static void expect(String actual,String wanted){if(!wanted.equals(actual))throw new AssertionError(actual+" != "+wanted);}
}
