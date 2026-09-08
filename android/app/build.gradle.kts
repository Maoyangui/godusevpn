plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

val appVersion = (project.findProperty("appVersion") as String?) ?: "0.6.0-a0"

android {
    namespace = "com.maoyangui.godusevpn"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.maoyangui.godusevpn"
        minSdk = 26
        targetSdk = 34
        versionCode = (project.findProperty("versionCode") as String?)?.toInt() ?: 1
        versionName = appVersion
        // 手机与电视共用一个包;按 ABI 分包在 CI 里用 splits 出
    }
    splits {
        abi {
            isEnable = (project.findProperty("splitAbi") as String?) == "true"
            reset()
            include("arm64-v8a", "armeabi-v7a", "x86_64")
            isUniversalApk = true
        }
    }
    signingConfigs {
        // 自建密钥:CI 从机密里放到 keystore.jks;本机没有就用 debug 签名
        create("release") {
            val ks = rootProject.file("keystore.jks")
            if (ks.exists()) {
                storeFile = ks
                storePassword = System.getenv("ANDROID_KEYSTORE_PASSWORD") ?: ""
                keyAlias = System.getenv("ANDROID_KEY_ALIAS") ?: "godusevpn"
                keyPassword = System.getenv("ANDROID_KEY_PASSWORD") ?: ""
            }
        }
    }
    buildTypes {
        release {
            isMinifyEnabled = false
            signingConfig = if (rootProject.file("keystore.jks").exists()) signingConfigs.getByName("release") else signingConfigs.getByName("debug")
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    buildFeatures { buildConfig = true }
    packaging { jniLibs.useLegacyPackaging = true }
}

// 页面就是仓库根 web/dist 那一套,打包前拷进 assets/web
val copyWeb by tasks.registering(Copy::class) {
    from(rootProject.file("../web/dist"))
    into(layout.buildDirectory.dir("generated/web/assets/web"))
}
android.sourceSets["main"].assets.srcDir(layout.buildDirectory.dir("generated/web/assets"))
tasks.matching { it.name.startsWith("merge") && it.name.endsWith("Assets") }.configureEach { dependsOn(copyWeb) }

dependencies {
    implementation(files("libs/godusevpn.aar")) // gomobile bind ./mobile 的产物
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.webkit:webkit:1.11.0")
    implementation("androidx.activity:activity-ktx:1.9.2")
}
