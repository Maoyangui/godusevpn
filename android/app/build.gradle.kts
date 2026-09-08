import javax.inject.Inject

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

// 页面就是仓库根 web/dist 那一套,打包前拷进 assets/web。
// 用 AGP 的"任务生成的资源目录"接口挂进去:mergeAssets、release 的 lintVital 这些读 assets 的任务都会正确依赖它;
// 手工 srcDir + dependsOn 的写法要么漏了 lint(CI 报 implicit dependency),要么改了页面不重新合并(打出旧页面)
abstract class CopyWebTask : DefaultTask() {
    @get:InputDirectory abstract val src: DirectoryProperty
    @get:OutputDirectory abstract val out: DirectoryProperty
    @get:Inject abstract val fs: FileSystemOperations
    @TaskAction fun run() {
        val dst = out.get().asFile
        dst.deleteRecursively()
        fs.copy { from(src); into(File(dst, "web")) }
    }
}
val copyWeb = tasks.register<CopyWebTask>("copyWeb") {
    src.set(rootProject.file("../web/dist"))
    out.set(layout.buildDirectory.dir("generated/web/assets"))
}
androidComponents {
    onVariants { variant -> variant.sources.assets?.addGeneratedSourceDirectory(copyWeb, CopyWebTask::out) }
}

dependencies {
    implementation(files("libs/godusevpn.aar")) // gomobile bind ./mobile 的产物
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("androidx.webkit:webkit:1.11.0")
    implementation("androidx.activity:activity-ktx:1.9.2")
}
