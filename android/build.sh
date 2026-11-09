#!/bin/bash
set -euo pipefail
umask 077
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
JAVA_HOME="${JAVA_HOME:-/opt/homebrew/opt/openjdk@21}"
SDK="${ANDROID_SDK_ROOT:-$ROOT/tools/android-sdk}"
BT="${ANDROID_BUILD_TOOLS:-$SDK/build-tools/36.0.0}"
JAR="$SDK/platforms/android-35/android.jar"
OUT="$ROOT/build/android"
STAGE="$OUT/stage"
APK="$OUT/router-proxy-0.3.0.apk"
KEYSTORE="${ANDROID_KEYSTORE:-$ROOT/private/android-signing.jks}"
PASSFILE="${ANDROID_SIGNING_PASSFILE:-$ROOT/private/android-signing.pass}"
CORE="${MIHOMO_CORE_GZIP:-$ROOT/build/release/mihomo-router-armv7.gz}"
RELEASE="$ROOT/release/core-release.json"
PATCH="$ROOT/patches/mihomo-v1.19.32-udp-reply-candidate.patch"
export JAVA_HOME
export PATH="$JAVA_HOME/bin:$PATH"
python3 -m unittest discover -s "$ROOT/android/tests" -p "*_test.py" -v
mkdir -p "$OUT" "$ROOT/private"
# The stage is disposable generated material owned by this build only.
rm -rf "$STAGE"
mkdir -p "$STAGE"/{classes,dex,assets,compiled,test-classes,generated}
cp "$ROOT"/android/assets/* "$STAGE/assets/"
python3 "$ROOT/android/core_release.py" generate "$RELEASE" "$CORE" "$PATCH" "$STAGE/assets" "$STAGE/generated"
"$JAVA_HOME/bin/javac" --release 11 -encoding UTF-8 -d "$STAGE/test-classes" "$ROOT/android/src/dev/jose/routerproxy/SecurityRules.java" "$ROOT/android/tests/SecurityRulesTest.java" "$ROOT/android/src/dev/jose/routerproxy/PendingImport.java" "$ROOT/android/tests/PendingImportTest.java" "$ROOT/android/src/dev/jose/routerproxy/DeviceProtection.java" "$ROOT/android/tests/DeviceProtectionTest.java" "$ROOT/android/src/dev/jose/routerproxy/TaskEpoch.java" "$ROOT/android/tests/TaskEpochTest.java"
"$JAVA_HOME/bin/java" -cp "$STAGE/test-classes" dev.jose.routerproxy.SecurityRulesTest "$ROOT/android/tests/fixtures"
"$JAVA_HOME/bin/java" -cp "$STAGE/test-classes" dev.jose.routerproxy.PendingImportTest
"$JAVA_HOME/bin/java" -cp "$STAGE/test-classes" dev.jose.routerproxy.DeviceProtectionTest
"$JAVA_HOME/bin/java" -cp "$STAGE/test-classes" dev.jose.routerproxy.TaskEpochTest
"$BT/aapt2" compile --dir "$ROOT/android/res" -o "$STAGE/compiled/"
"$BT/aapt2" link -o "$STAGE/resources.apk" -I "$JAR" --manifest "$ROOT/android/AndroidManifest.xml" -A "$STAGE/assets" "$STAGE"/compiled/*.flat
find "$ROOT/android/src" "$STAGE/generated" -name '*.java' -print | LC_ALL=C sort > "$STAGE/sources.list"
"$JAVA_HOME/bin/javac" --release 11 -encoding UTF-8 -cp "$JAR" -d "$STAGE/classes" @"$STAGE/sources.list"
find "$STAGE/classes" -name '*.class' -print | LC_ALL=C sort > "$STAGE/classes.list"
"$JAVA_HOME/bin/java" -cp "$BT/lib/d8.jar" com.android.tools.r8.D8 --lib "$JAR" --min-api 26 --output "$STAGE/dex" @"$STAGE/classes.list"
# Normalize archive order and timestamps, independent of source checkout/build time.
python3 - "$STAGE/resources.apk" "$STAGE/dex/classes.dex" "$STAGE/unsigned.apk" <<'PY'
import sys,zipfile
src,dex,out=sys.argv[1:]
with zipfile.ZipFile(src) as z:
    entries={n:z.read(n) for n in z.namelist()}
entries['classes.dex']=open(dex,'rb').read()
with zipfile.ZipFile(out,'w') as z:
    for name in sorted(entries):
        info=zipfile.ZipInfo(name,(2026,1,1,0,0,0));info.create_system=3;info.external_attr=0o100644<<16
        info.compress_type=zipfile.ZIP_STORED if name == 'resources.arsc' or name.endswith('.gz') else zipfile.ZIP_DEFLATED
        z.writestr(info,entries[name],compresslevel=9)
PY
"$BT/zipalign" -f -p 4 "$STAGE/unsigned.apk" "$STAGE/aligned.apk"
if [[ ! -f "$KEYSTORE" && ! -f "$PASSFILE" ]]; then
 python3 - "$PASSFILE" <<'PY'
import os,secrets,sys
fd=os.open(sys.argv[1],os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
with os.fdopen(fd,'w') as f:f.write(secrets.token_urlsafe(32)+'\n')
PY
 "$JAVA_HOME/bin/keytool" -genkeypair -keystore "$KEYSTORE" -storetype JKS -alias router-proxy -keyalg RSA -keysize 3072 -validity 10000 -dname 'CN=Router Proxy Local Release,O=Personal,C=CN' -storepass:file "$PASSFILE" -keypass:file "$PASSFILE" >/dev/null 2>&1
 chmod 600 "$KEYSTORE" "$PASSFILE"
fi
[[ -f "$KEYSTORE" && -f "$PASSFILE" ]] || { echo 'Signing keystore/password file pair missing' >&2; exit 1; }
"$JAVA_HOME/bin/java" -jar "$BT/lib/apksigner.jar" sign --ks "$KEYSTORE" --ks-key-alias router-proxy --ks-pass "file:$PASSFILE" --v1-signing-enabled true --v2-signing-enabled true --v3-signing-enabled true --v4-signing-enabled false --out "$APK" "$STAGE/aligned.apk"
"$JAVA_HOME/bin/java" -jar "$BT/lib/apksigner.jar" verify --verbose --print-certs "$APK"
"$BT/zipalign" -c -p 4 "$APK"
# Android R+ rejects a compressed or non-4-byte-aligned resource table.
# zipalign alone skips compressed entries, so check the final signed ZIP metadata too.
python3 "$ROOT/android/core_release.py" verify-apk "$RELEASE" "$APK"
shasum -a 256 "$APK"
echo "APK: $APK"
