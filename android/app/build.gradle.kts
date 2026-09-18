import javax.inject.Inject

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
}

val appVersion = (project.findProperty("appVersion") as String?) ?: "0.6.0-a0"

/**
 * versionCode 从版本号算出来,不再取 CI 的运行序号。
 *
 * 运行序号的计数域是「每仓库 × 每工作流」:仓库换账号重建、或者工作流文件改名,它会**重置回 1**
 * ——这两件事在本项目都是计划中的。已装用户随后会撞上 INSTALL_FAILED_VERSION_DOWNGRADE,
 * 看到的症状是「点了更新什么也没发生」。重跑某个旧 tag 的工作流则会给更旧的版本铸出更高的号,
 * 把用户推回旧版并从此装不上新版。
 *
 * 编码:每段各占固定位数再拼起来 —— major×10^8 + minor×10^6 + patch×10^3 + 预发布序号。
 * 正式版的预发布序号用 999,排在同版本所有预发布之后:
 *
 *	0.6.22-m25 → 6022025    0.6.22 → 6022999    0.6.23-m1 → 6023001
 *	0.6.100-m1 → 6100001    0.7.0-m1 → 7000001
 *
 * 各段必须留够位数、互不串位。第一版只给了 patch 两位,照本项目"每次发版加 patch"的节奏走到
 * 0.6.100 时算出来会落进 0.7.x 的号段,已装 0.6.100 的用户反而再也装不上 0.7.0 ——
 * 正好造成这段代码本来要防的那个 INSTALL_FAILED_VERSION_DOWNGRADE。
 * 上限:Android 要求 versionCode < 2100000000,这套编码到主版本 20 才撑满,超了会直接报错而不是悄悄串位。
 */
fun versionCodeOf(v: String): Int {
    (project.findProperty("versionCode") as String?)?.toIntOrNull()?.let { return it } // 想手动指定也行
    val m = Regex("""^(\d+)\.(\d+)\.(\d+)(?:-[A-Za-z]*(\d+))?""").find(v.trim())
        ?: throw GradleException("版本号 \"$v\" 认不出来,算不出 versionCode(要 主.次.修订[-预发布号])")
    val (major, minor, patch) = listOf(1, 2, 3).map { m.groupValues[it].toInt() }
    val pre = m.groupValues[4].toIntOrNull()?.coerceIn(0, 998) ?: 999
    if (major > 20 || minor > 99 || patch > 999) {
        throw GradleException("版本号 \"$v\" 超出 versionCode 的编码范围(主 ≤20、次 ≤99、修订 ≤999);要放宽得先想清楚各段会不会串位")
    }
    return major * 100_000_000 + minor * 1_000_000 + patch * 1_000 + pre
}

android {
    namespace = "com.maoyangui.godusevpn"
    compileSdk = 34

    defaultConfig {
        applicationId = "com.maoyangui.godusevpn"
        minSdk = 26
        targetSdk = 34
        versionCode = versionCodeOf(appVersion)
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
        // 自建密钥:CI 从机密里放到 keystore.jks。没有它就不出正式包(见文件末尾的守门检查)
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
            // 没有 keystore 时**不再退回 debug 签名**。CI 跑机上的 debug 密钥是每次现生成的,
            // 于是每个版本的签名都不一样:已装用户的应用内升级 100% 失败(签名不一致装不上),
            // 手动装也一样,只能先卸载 —— 而且这件事是静默发生的,发出去才会被用户发现。
            // 宁可这一步直接失败。真正的守门在下面的 taskGraph 检查里,这里只是不给错的默认值。
            signingConfig = if (rootProject.file("keystore.jks").exists()) signingConfigs.getByName("release") else null
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

// 打正式包却没有签名密钥时,直接失败而不是悄悄出一个装不上的包。
// (debug 签名的正式包对已装用户等于「再也升不了级」—— 签名对不上,系统不让覆盖安装,
//  只能先卸载;而这件事在发出去之前没人会发现。)
//
// 挂在打包任务自己身上,不去猜任务图:AGP 给 :app 生成的、名字里带 Release 的任务远不止打包
// (testReleaseUnitTest、lintVitalRelease、packageReleaseUnitTestForUnitTest…),按名字匹配任务图
// 会把本机的 gradle test / check / build 一起拦下来 —— 第一版就是这么误伤的。
tasks.matching { it.name == "assembleRelease" || it.name == "bundleRelease" }.configureEach {
    doFirst {
        if (!rootProject.file("keystore.jks").exists()) {
            throw GradleException(
                "打正式包需要 android/keystore.jks(CI 从机密里放,本机自己生成一份)。" +
                    "没有它只能退回 debug 签名,而 debug 密钥每台机器 / 每次 CI 都不一样 —— " +
                    "已装用户会因为签名不一致装不上新版,且只能卸载重装。要本地试打就用 assembleDebug。"
            )
        }
    }
}
