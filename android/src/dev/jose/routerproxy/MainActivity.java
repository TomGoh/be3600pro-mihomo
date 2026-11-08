package dev.jose.routerproxy;

import android.app.*;
import android.os.*;
import android.content.*;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.net.Uri;
import android.view.*;
import android.view.inputmethod.InputMethodManager;
import android.widget.*;
import org.json.*;
import java.io.*;
import java.nio.charset.StandardCharsets;
import java.text.DateFormat;
import java.util.Date;
import java.util.ArrayList;
import java.util.LinkedHashSet;
import java.util.concurrent.*;

public final class MainActivity extends Activity {
 private static final int GREEN=0xff156b62, INK=0xff183c36, MUTED=0xff64766f, BG=0xfff4f7f6;
 private final ExecutorService executor=Executors.newSingleThreadExecutor();
 private final PendingImport pendingImport=new PendingImport();
 private PairingStore store; private PairingStore.Pairing pairing; private ApiClient client;
 private LinearLayout root,content,nav; private TextView message; private ProgressBar progress;
 private int page=0; private boolean busy=false,destroyed=false; private JSONObject status,groups,subscriptions,devices;
 private String notice="正在读取安全配对…";
 private LinkedHashSet<String> bypassDraft;
 private boolean bypassDirty=false;
 interface Work {JSONObject run() throws Exception;}
 interface Done {void run(JSONObject value) throws Exception;}
 @Override public void onCreate(Bundle state){super.onCreate(state);store=new PairingStore(this);if(state!=null){page=state.getInt("page",0);pendingImport.offer(state.getString("pendingImport"));if(state.containsKey("bypassDraft")){bypassDraft=new LinkedHashSet<>(state.getStringArrayList("bypassDraft"));bypassDirty=state.getBoolean("bypassDirty");}}layout();runTask("正在读取安全配对…",()->{pairing=store.load();if(pairing!=null)client=new ApiClient(pairing);return new JSONObject();},r->{notice="";render();if(!drainPendingImport()&&client!=null)refresh();});}
 @Override public void onSaveInstanceState(Bundle s){s.putInt("page",page);s.putString("pendingImport",pendingImport.snapshot());if(bypassDraft!=null)s.putStringArrayList("bypassDraft",new ArrayList<>(bypassDraft));s.putBoolean("bypassDirty",bypassDirty);super.onSaveInstanceState(s);}
 @Override public void onDestroy(){destroyed=true;executor.shutdownNow();super.onDestroy();}
 private int dp(int n){return (int)(getResources().getDisplayMetrics().density*n+.5f);}
 private TextView text(String s,int size,int color){TextView t=new TextView(this);t.setText(s);t.setTextSize(size);t.setTextColor(color);t.setLineSpacing(dp(3),1);return t;}
 private LinearLayout vertical(){LinearLayout l=new LinearLayout(this);l.setOrientation(LinearLayout.VERTICAL);return l;}
 private void layout(){root=vertical();root.setBackgroundColor(BG);root.setPadding(dp(18),0,dp(18),0);root.setOnApplyWindowInsetsListener((v,i)->{root.setPadding(dp(18),i.getSystemWindowInsetTop()+dp(10),dp(18),i.getSystemWindowInsetBottom()+dp(8));return i;});setContentView(root);root.requestApplyInsets();LinearLayout title=new LinearLayout(this);title.setGravity(Gravity.CENTER_VERTICAL);TextView brand=text("路由代理",26,INK);brand.setTypeface(null,Typeface.BOLD);title.addView(brand,new LinearLayout.LayoutParams(0,dp(58),1));Button refresh=button("刷新",v->refresh());title.addView(refresh);root.addView(title);message=text(notice,13,MUTED);message.setPadding(dp(4),dp(3),dp(4),dp(10));root.addView(message);progress=new ProgressBar(this,null,android.R.attr.progressBarStyleHorizontal);progress.setMax(100);root.addView(progress,new LinearLayout.LayoutParams(-1,dp(4)));ScrollView scroll=new ScrollView(this);scroll.setFillViewport(true);content=vertical();content.setPadding(0,dp(14),0,dp(16));scroll.addView(content);root.addView(scroll,new LinearLayout.LayoutParams(-1,0,1));nav=new LinearLayout(this);root.addView(nav);render();}
 private Button button(String s,View.OnClickListener listener){Button b=new Button(this);b.setText(s);b.setTextSize(14);b.setTextColor(GREEN);b.setAllCaps(false);b.setOnClickListener(v->{if(!busy)listener.onClick(v);});return b;}
 private void gap(LinearLayout l,int height){l.addView(new View(this),new LinearLayout.LayoutParams(1,dp(height)));}
 private LinearLayout card(String title,String body){LinearLayout c=vertical();c.setPadding(dp(18),dp(16),dp(18),dp(16));GradientDrawable shape=new GradientDrawable();shape.setColor(Color.WHITE);shape.setCornerRadius(dp(18));c.setBackground(shape);LinearLayout.LayoutParams lp=new LinearLayout.LayoutParams(-1,-2);lp.bottomMargin=dp(14);content.addView(c,lp);if(!title.isEmpty()){TextView h=text(title,18,INK);h.setTypeface(null,Typeface.BOLD);c.addView(h);}if(!body.isEmpty()){gap(c,8);c.addView(text(body,14,MUTED));}return c;}
 private void render(){if(destroyed||content==null)return;message.setText(notice);progress.setVisibility(busy?View.VISIBLE:View.GONE);content.removeAllViews();nav.removeAllViews();String[] tabs={"状态","节点","订阅","设置"};for(int i=0;i<4;i++){final int selected=i;Button b=button(tabs[i],v->{page=selected;notice="";render();if(pairing!=null)refresh();});if(page==i){b.setTypeface(null,Typeface.BOLD);b.setTextColor(INK);}nav.addView(b,new LinearLayout.LayoutParams(0,dp(56),1));}if(pairing==null){onboarding();return;}switch(page){case 0:statusPage();break;case 1:nodesPage();break;case 2:subscriptionsPage();break;default:settingsPage();}}
 private void onboarding(){LinearLayout c=card("在家，安心连接","连接家庭 Wi-Fi，导入路由器提供的配对文件，即可管理代理、节点和订阅。配对文件含访问凭据，请妥善保管。");gap(c,14);c.addView(button("导入配对文件",v->importFile()));card("仅限家庭局域网","应用仅连接私有 IP 的 HTTPS 管理地址，并验证路由器证书指纹。手机离开家庭网络后，管理连接将不可用。");}
 private void runTask(String hint,Work work,Done done){if(busy||destroyed)return;busy=true;notice=hint;progress.setIndeterminate(true);render();executor.execute(()->{JSONObject result=null;Exception failure=null;try{result=work.run();}catch(Exception e){failure=e;}final JSONObject value=result;final Exception error=failure;runOnUiThread(()->{if(destroyed)return;busy=false;pendingImport.complete();pendingImport.initialized();progress.setIndeterminate(true);if(error!=null){notice=ApiClient.friendly(error);render();}else{try{notice="";done.run(value);}catch(Exception e){notice=ApiClient.friendly(e);}render();}drainPendingImport();});});}
 private void refresh(){if(client==null){notice="请先导入配对文件。";render();return;}final int requested=page;runTask("正在刷新…",()->client.json("GET",requested==1?"/api/groups":requested==2?"/api/subscriptions":requested==3?"/api/devices":"/api/status",null),r->{if(requested==1)groups=r;else if(requested==2)subscriptions=r;else if(requested==3){devices=r;if(!bypassDirty)bypassDraft=null;}else status=r;notice="已刷新 · "+DateFormat.getTimeInstance(DateFormat.SHORT).format(new Date());});}
 private void action(String method,String path,JSONObject data,String hint){runTask(hint,()->client.json(method,path,data),r->{notice="操作成功";status=null;if(page==1)groups=null;if(page==2)subscriptions=null;if(page==3){devices=null;bypassDraft=null;bypassDirty=false;}refresh();});}
 private JSONObject data(String key,Object value){try{return new JSONObject().put(key,value);}catch(JSONException e){throw new IllegalArgumentException("输入无效");}}
 private void statusPage(){if(status==null){card("路由器状态","点击刷新，获取当前代理状态。");return;}boolean ready=status.optBoolean("coreRunning"),enabled=status.optBoolean("enabled");LinearLayout c=card(enabled?"代理已开启":"代理已关闭",ready?"核心运行中 · "+modeLabel(status.optString("mode")):"核心未就绪 · 可离线恢复");gap(c,8);c.addView(text(enabled?"路由器正在接管家庭网络流量。":"当前未接管家庭网络流量。核心可以继续待命。",14,MUTED));if(ready)c.addView(button(enabled?"关闭代理":"开启代理",v->action("POST","/api/enabled",data("enabled",!enabled),enabled?"正在关闭代理…":"正在开启代理…")));if(status.optBoolean("busy"))c.addView(text("路由器正在处理任务，请稍后刷新。",14,MUTED));String scope=status.optString("scope");card("生效范围",scope.equals("lan")?"家庭局域网内的设备；已生效的设备直连设置会排除对应设备。":scope.equals("test-device")?"当前仅对测试设备生效。":"范围暂未确认，请刷新后再检查。");LinearLayout modes=card("流量模式","切换模式会影响路由器接管的设备。");String current=status.optString("mode");for(String mode:new String[]{"rule","global","direct"})modes.addView(button((current.equals(mode)?"✓ ":"")+modeLabel(mode),v->action("PUT","/api/mode",data("mode",mode),"正在切换模式…")));if(!ready)recoveryCard();String version=status.optString("version","");card("运行信息",(version.isEmpty()?"版本暂不可用":"核心版本 "+version)+"\n可用内存 "+(status.optLong("memoryAvailableKiB")/1024)+" MB · 闪存可用 "+(status.optLong("flashFreeBytes")/1048576)+" MB"+(status.optBoolean("memoryCore")?"\n核心保存在内存中，路由器重启后可能需要恢复。":""));if(!status.optString("error").isEmpty())card("路由器提示",safe(status.optString("error")));}
 private String modeLabel(String mode){switch(mode){case "rule":return "规则分流";case "global":return "全局代理";case "direct":return "全部直连";default:return "模式未知";}}
 private String safe(String s){return s.replaceAll("https?://[^\\s\\\"<>]+","[地址已隐藏]");}
 private void nodesPage(){card("选择节点","仅手动选择组支持切换。延迟未知不表示节点不可用。");if(groups==null){card("等待节点列表","点击刷新获取路由器上的节点组。");return;}JSONArray list=groups.optJSONArray("groups");if(list==null||list.length()==0){card("暂无节点组","请先添加订阅并更新节点。");return;}for(int i=0;i<list.length();i++){JSONObject group=list.optJSONObject(i);if(group==null)continue;String name=group.optString("name");LinearLayout c=card(name,"当前："+group.optString("current","未选择"));if(!group.optBoolean("selectable")){c.addView(text("此组由路由器自动管理",13,MUTED));continue;}c.addView(button("选择节点",v->chooseNode(group)));}}
 private void chooseNode(JSONObject group){JSONArray items=group.optJSONArray("candidates");if(items==null||items.length()==0){toast("此组暂无可选节点");return;}String[] labels=new String[items.length()],names=new String[items.length()];int selected=-1;for(int i=0;i<items.length();i++){JSONObject n=items.optJSONObject(i);names[i]=n==null?"":n.optString("name");long delay=n==null?0:n.optLong("delay");labels[i]=names[i]+"  ·  "+(delay>0?delay+" ms":"延迟未知");if(names[i].equals(group.optString("current")))selected=i;}AlertDialog d=new AlertDialog.Builder(this).setTitle(group.optString("name")).setSingleChoiceItems(labels,selected,(dialog,index)->{dialog.dismiss();action("PUT","/api/groups/"+SecurityRules.segment(group.optString("name")),data("name",names[index]),"正在切换节点…");}).setNegativeButton("取消",null).create();d.show();}
 private void subscriptionsPage(){LinearLayout intro=card("节点订阅","添加 Clash / Mihomo YAML 节点来源。更新节点会保留路由器现有分流策略，最多 10 个来源。");intro.addView(button("添加订阅",v->addSubscription()));if(subscriptions==null){card("等待订阅列表","点击刷新获取已添加的订阅。");return;}JSONObject imported=subscriptions.optJSONObject("imported");int importedCount=imported==null?0:imported.optInt("nodeCount");if(importedCount>0)card("已导入的配置",importedCount+" 个节点 · 当前配置自带节点\n这些节点随完整配置导入，原订阅链接未一并保存。添加原订阅链接后，可在这里管理和更新该来源。");JSONArray list=subscriptions.optJSONArray("subscriptions");if(list==null||list.length()==0){if(importedCount==0)card("暂无节点来源","添加订阅后，即可更新并选择节点。");return;}for(int i=0;i<list.length();i++){JSONObject sub=list.optJSONObject(i);if(sub==null)continue;String id=sub.optString("id");long updated=sub.optLong("updatedAt");LinearLayout c=card(sub.optString("name"),sub.optInt("nodeCount")+" 个节点\n"+(updated>0?"更新于 "+DateFormat.getDateTimeInstance(DateFormat.SHORT,DateFormat.SHORT).format(new Date(updated*1000)):"尚未更新")+"\n订阅地址已隐藏");c.addView(button("更新节点",v->action("POST","/api/subscriptions/"+SecurityRules.segment(id)+"/update",new JSONObject(),"正在更新订阅…")));c.addView(button("删除订阅",v->new AlertDialog.Builder(this).setTitle("删除此订阅？").setMessage("将移除此来源及其节点："+sub.optString("name")).setNegativeButton("取消",null).setPositiveButton("删除",(d,w)->action("DELETE","/api/subscriptions/"+SecurityRules.segment(id),null,"正在删除订阅…")).show()));}}
 private EditText input(String hint,boolean secret){EditText e=new EditText(this);e.setHint(hint);e.setTextSize(15);e.setSingleLine(true);e.setInputType(secret?android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_PASSWORD:android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_FLAG_CAP_SENTENCES);e.setImportantForAutofill(View.IMPORTANT_FOR_AUTOFILL_NO_EXCLUDE_DESCENDANTS);return e;}
 private void addSubscription(){LinearLayout form=vertical();form.setPadding(dp(24),dp(8),dp(24),0);EditText name=input("名称（最多 40 个字符）",false),url=input("订阅链接（保密）",true);name.setFilters(new android.text.InputFilter[]{new android.text.InputFilter.LengthFilter(80)});url.setFilters(new android.text.InputFilter[]{new android.text.InputFilter.LengthFilter(4096)});form.addView(name);form.addView(url);TextView validation=text("链接只用于路由器获取节点，请勿分享给他人。",13,MUTED);form.addView(validation);AlertDialog dialog=new AlertDialog.Builder(this).setTitle("添加订阅").setView(form).setNegativeButton("取消",null).setPositiveButton("添加",null).create();dialog.setOnShowListener(x->dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener(v->{String n=name.getText().toString().trim(),u=url.getText().toString().trim();Uri parsed=Uri.parse(u);if(n.isEmpty()||n.codePointCount(0,n.length())>40||u.length()>4096||!("https".equals(parsed.getScheme())||"http".equals(parsed.getScheme()))||parsed.getHost()==null){validation.setText("请输入有效名称和 HTTP(S) 订阅链接。");return;}try{JSONObject body=new JSONObject().put("name",n).put("url",u);url.setText("");dialog.dismiss();action("POST","/api/subscriptions",body,"正在添加订阅…");}catch(Exception e){validation.setText("输入无效，请检查后重试。");}}));dialog.show();}
 private void deviceBypassCard(){
  LinearLayout c=card("直连设备","勾选需要绕过代理的设备，上网与 DNS 都将直连。保存后，下次开启代理时生效。");
  if(devices==null){c.addView(button("读取设备列表",v->refresh()));return;}
  JSONArray list=devices.optJSONArray("devices");
  if(list==null||list.length()==0){c.addView(text("暂无已发现的设备，请连接家庭网络后刷新。",14,MUTED));return;}
  if(bypassDraft==null){bypassDraft=new LinkedHashSet<>();for(int i=0;i<list.length();i++){JSONObject d=list.optJSONObject(i);if(d!=null&&d.optBoolean("configured"))bypassDraft.add(d.optString("mac"));}}
  Button save=button("保存直连设备（"+bypassDraft.size()+"）",v->saveBypassSelection());
  for(int i=0;i<list.length();i++){
   JSONObject d=list.optJSONObject(i);if(d==null)continue;
   String mac=d.optString("mac"),ip=d.optString("ip"),name=d.optString("name","局域网设备");
   if(i>0){View line=new View(this);line.setBackgroundColor(0xffe5ebe7);c.addView(line,new LinearLayout.LayoutParams(-1,dp(1)));}
   LinearLayout row=new LinearLayout(this);row.setGravity(Gravity.CENTER_VERTICAL);row.setMinimumHeight(dp(76));
   LinearLayout labels=vertical();labels.setPadding(0,dp(10),dp(8),dp(10));
   labels.addView(text(name,15,INK));labels.addView(text((ip.isEmpty()?"IP 暂不可用":ip)+" · "+mac,11,MUTED));
   labels.addView(text(DeviceProtection.label(d.optBoolean("configured"),d.optBoolean("active")),12,MUTED));
   row.addView(labels,new LinearLayout.LayoutParams(0,-2,1));
   CheckBox check=new CheckBox(this);check.setContentDescription(name+" 直连");check.setChecked(bypassDraft.contains(mac));check.setEnabled(!busy);
   check.setOnCheckedChangeListener((v,on)->{if(on)bypassDraft.add(mac);else bypassDraft.remove(mac);bypassDirty=true;save.setText("保存直连设备（"+bypassDraft.size()+"）");});
   row.addView(check);row.setOnClickListener(v->{if(!busy)check.toggle();});c.addView(row);
  }
  gap(c,8);save.setEnabled(!busy);c.addView(save);
 }
 private void saveBypassSelection(){
  if(bypassDraft==null)return;
  if(bypassDraft.size()>32){toast("最多选择 32 台直连设备");return;}
  JSONArray macs=new JSONArray();for(String mac:bypassDraft)macs.put(mac);
  action("PUT","/api/devices/bypass",data("macs",macs),"正在保存直连设备…");
 }
 private void settingsPage(){deviceBypassCard();LinearLayout connection=card("安全配对",pairing.url+"\n配对凭据通过手机安全密钥加密保存。");connection.addView(button("更换配对文件",v->importFile()));connection.addView(button("清除配对",v->new AlertDialog.Builder(this).setTitle("清除手机上的配对？").setMessage("清除后需要重新导入配对文件。路由器的代理状态不会改变。").setNegativeButton("取消",null).setPositiveButton("清除",(d,w)->runTask("正在清除配对…",()->{store.clear();return new JSONObject();},r->{pairing=null;client=null;status=groups=subscriptions=devices=null;notice="已清除配对";})).show()));recoveryCard();LinearLayout about=card("关于路由代理","版本 0.2.1 · 仅限家庭局域网管理\n内置 Mihomo "+CoreRelease.DISPLAY_VERSION+"（Linux ARMv7）\n开源许可证：GNU GPL v3");about.addView(button("查看开源说明",v->showLicense()));}
 private void recoveryCard(){LinearLayout c=card("离线恢复核心","核心未就绪时，可通过家庭 Wi-Fi 上传应用内置的恢复包（约 "+String.format(java.util.Locale.ROOT,"%.1f",CoreRelease.ARCHIVE_BYTES/1048576.0)+" MB），无需手机联网下载。恢复期间服务可能短暂重启。");c.addView(button("恢复路由器核心",v->new AlertDialog.Builder(this).setTitle("恢复路由器核心？").setMessage("请保持应用在前台，并连接家庭 Wi-Fi。将上传内置的 Mihomo "+CoreRelease.DISPLAY_VERSION+"，完成后刷新路由器状态。").setNegativeButton("取消",null).setPositiveButton("开始恢复",(d,w)->recover()).show()));}
 private void recover(){runTask("正在上传核心…",()->{try(InputStream in=getAssets().open(CoreRelease.ARCHIVE_ASSET)){return client.recover(in,p->runOnUiThread(()->{if(!destroyed){progress.setIndeterminate(false);progress.setProgress(p);message.setText(p<100?"正在上传核心 · "+p+"%":"上传完成，正在等待路由器准备核心…");}}));}},r->{status=null;notice="核心恢复成功，正在检查状态…";page=0;refresh();});}
 private void importFile(){Intent i=new Intent(Intent.ACTION_OPEN_DOCUMENT);i.setType("*/*");i.addCategory(Intent.CATEGORY_OPENABLE);try{startActivityForResult(i,101);}catch(ActivityNotFoundException e){toast("此手机未提供文件选择器");}}
 @Override protected void onActivityResult(int request,int result,Intent intent){
  super.onActivityResult(request,result,intent);
  if(request!=101||result!=RESULT_OK||intent==null||intent.getData()==null)return;
  // onActivityResult may arrive while the recreated Activity is still loading Keystore data.
  pendingImport.offer(intent.getData().toString());
  drainPendingImport();
 }
 private boolean drainPendingImport(){
  String saved=pendingImport.startIfIdle(busy||destroyed);
  if(saved==null)return false;
  final Uri uri=Uri.parse(saved);
  runTask("正在校验并保存配对…",()->{
   try(InputStream in=getContentResolver().openInputStream(uri)){
    if(in==null)throw new IllegalArgumentException("无法读取配对文件");
    PairingStore.Pairing next=new PairingStore.Pairing(new String(ApiClient.readBounded(in,16384),StandardCharsets.UTF_8));
    ApiClient nextClient=new ApiClient(next);store.save(next);pairing=next;client=nextClient;
    return new JSONObject();
   }
  },r->{page=0;status=groups=subscriptions=devices=null;notice="配对已保存";if(!drainPendingImport())refresh();});
  return true;
 }

 private void showLicense(){runTask("正在打开开源说明…",()->{try(InputStream in=getAssets().open("LICENSE-mihomo.txt")){String license=new String(ApiClient.readBounded(in,100000),StandardCharsets.UTF_8);try(InputStream source=getAssets().open("CORE-SOURCE.txt")){return data("license",new String(ApiClient.readBounded(source,32768),StandardCharsets.UTF_8)+"\n\n"+license);}}},r->{TextView t=text("Mihomo "+CoreRelease.DISPLAY_VERSION+"\n\n"+r.optString("license"),12,INK);t.setTextIsSelectable(true);t.setPadding(dp(18),dp(12),dp(18),dp(12));android.text.util.Linkify.addLinks(t,android.text.util.Linkify.WEB_URLS);ScrollView scroll=new ScrollView(this);scroll.addView(t);new AlertDialog.Builder(this).setTitle("开源许可证").setView(scroll).setPositiveButton("关闭",null).show();});}
 private void toast(String message){Toast.makeText(this,message,Toast.LENGTH_SHORT).show();}
}
