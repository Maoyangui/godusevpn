package com.maoyangui.godusevpn

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Intent
import android.os.Build
import android.util.Log
import androidx.core.content.FileProvider
import com.maoyangui.godusevpn.mobile.Engine
import com.maoyangui.godusevpn.mobile.EventListener
import com.maoyangui.godusevpn.mobile.Mobile
import org.json.JSONObject
import org.json.JSONTokener
import java.io.File
import java.util.concurrent.CopyOnWriteArrayList

/**
 * 进程里唯一的引擎:守护进程(设置、订阅、规则、状态机)跑在 Go 里,和 Windows / Linux 是同一份代码。
 * 页面经 MainActivity 的 JS 桥调 [Engine.call],事件经 [listeners] 广播给页面与通知栏。
 */
class App : Application() {
    companion object {
        const val TAG = "godusevpn"
        const val CHANNEL = "godusevpn.vpn"
        lateinit var instance: App
        @Volatile var engine: Engine? = null
        val listeners = CopyOnWriteArrayList<(String, String) -> Unit>()
        @Volatile var lastState: JSONObject? = null

        fun engine(): Engine = engine ?: synchronized(App::class.java) {
            engine ?: Mobile.newEngine(instance.filesDir.absolutePath, instance.cacheDir.absolutePath, HostProxy, object : EventListener {
                override fun onEvent(name: String, dataJSON: String) {
                    when (name) {
                        "state" -> runCatching { lastState = JSONObject(dataJSON) }
                        "install-update" -> runCatching { instance.installApk(JSONTokener(dataJSON).nextValue() as String) }.onFailure { Log.w(TAG, "install", it) }
                    }
                    for (l in listeners) runCatching { l(name, dataJSON) }.onFailure { Log.w(TAG, "listener", it) }
                }
            }).also { engine = it }
        }
    }

    /**
     * 引擎把新版 APK 下好(校验过 SHA256)后交给系统安装器;装完系统会杀掉本进程,
     * 开机接收器按 MY_PACKAGE_REPLACED 再把 VPN 拉起来。
     *
     * 这条路有两个会**静默失败**的地方,页面停在「正在安装」再也不动,而用户和我们都看不出为什么:
     *  - 「安装未知应用」是按应用授的特殊权限(Android 8 起)。没授权的话系统安装器会直接给一页
     *    「已阻止」,部分电视盒子 ROM 上干脆什么都不弹。所以先自己查一次,没有就把用户领到授权页。
     *  - 这个方法是从 Go 的协程回调进来的,用的是 Application 上下文,在**非主线程**。
     *    Android 10 起的后台 Activity 启动限制下,用户在下载期间切走了应用(电视上下几十兆很常见,
     *    按个 Home 或者屏保介入就算),这次 startActivity 会被系统悄悄拦掉且**不抛异常**。
     *    所以失败与"看起来成功了其实没弹出来"都要告诉页面。
     */
    fun installApk(path: String) {
        if (Build.VERSION.SDK_INT >= 26 && !packageManager.canRequestPackageInstalls()) {
            // 措辞不能写成"已为你打开":这个方法跑在后台线程,授权页很可能被后台启动限制悄悄拦掉
            // 且不抛异常(用户在下载期间按了 Home、或者屏保介入 —— 电视上最典型的场景)。
            // 说成"可能会弹出",没弹的话下面那句指路仍然是对的。
            notifyInstall("需要先允许本应用安装未知应用。系统授权页可能已弹出;没有的话到「系统设置 → 应用 → 特殊权限 → 安装未知应用」里允许本应用,然后再点一次更新")
            runCatching {
                startActivity(Intent(android.provider.Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,
                    android.net.Uri.parse("package:$packageName")).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
            }.onFailure {
                Log.w(TAG, "打不开安装权限设置", it)
                notifyInstall("这台设备不让应用自行安装更新,请到官网手动下载安装包")
            }
            return
        }
        val uri = FileProvider.getUriForFile(this, "$packageName.files", File(path))
        runCatching {
            startActivity(Intent(Intent.ACTION_VIEW).setDataAndType(uri, "application/vnd.android.package-archive")
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_GRANT_READ_URI_PERMISSION))
        }.onFailure {
            Log.w(TAG, "拉不起系统安装器", it)
            notifyInstall("拉不起系统安装器。请打开应用后再点一次更新,或到官网手动下载安装包")
        }
    }

    /** 把安装这条路上的问题告诉页面(只有一行 logcat 的话,用户和我们都查不下去)。 */
    private fun notifyInstall(msg: String) {
        val data = JSONObject().put("message", msg).toString()
        for (l in listeners) runCatching { l("install-error", data) }
    }

    override fun onCreate() {
        super.onCreate()
        instance = this
        // Kotlin 侧崩溃也记到 logs/crash.log(Go 侧的由引擎自己写),诊断包会带上;记完交给系统默认处理
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { th, e ->
            runCatching {
                val dir = File(filesDir, "data/logs").apply { mkdirs() }
                File(dir, "crash.log").appendText("${java.util.Date()} 线程 ${th.name}: ${android.util.Log.getStackTraceString(e)}\n")
            }
            previous?.uncaughtException(th, e)
        }
        if (Build.VERSION.SDK_INT >= 26) {
            val nm = getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(NotificationChannel(CHANNEL, getString(R.string.channel_vpn), NotificationManager.IMPORTANCE_LOW).apply {
                setShowBadge(false)
            })
        }
        // 引擎要早点起来(开机自启与磁贴都要用),但**不能在主线程上起**:
        // Mobile.NewEngine 里有建目录、开 crash.log、读设置与状态、还有一次落盘的设置写入,
        // 而且每次进程启动都跑一遍 —— 包括系统只为投递一条广播而把进程拉起来的时候。
        // 慢速电视盒子上这是实打实的启动 ANR。真正要用它的地方(engine())自己是 synchronized 的,
        // 会等这一轮建好,顺序不会乱。
        Thread { runCatching { engine() }.onFailure { Log.e(TAG, "引擎起不来", it) } }.start()
    }
}
