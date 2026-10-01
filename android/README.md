# Atlas Commander for Android

A remote control for Commander on your PC: the board, each agent's transcript,
approvals (also as notifications with Allow and Deny), and hold, stop and kill.
Agents always run on the PC. The protocol is [docs/phone-api.md](../docs/phone-api.md).

Kotlin and Jetpack Compose, Android 10 (API 29) and newer.

## Build

Needs JDK 17 or newer, the Android SDK (platform 35) and Gradle 9.8. There is
no Gradle wrapper in the repo.

    cd android
    ANDROID_HOME=~/Android/Sdk gradle assembleDebug testDebugUnitTest

The APK is `app/build/outputs/apk/debug/app-debug.apk`. It is signed with the
machine's debug key (`~/.android/debug.keystore`); keep that file, because a
phone only installs an update signed with the same key it was first installed
with.

## Install and pair

1. Copy the APK to the phone and open it. Android asks once to allow installs
   from the app you opened it with.
2. On the PC: Settings, Phone, Allow phone access, then Show pairing code.
3. In the app, scan the QR code, or paste the link.

If the phone can't reach the PC, open the port in the PC's firewall (see the
main README).
