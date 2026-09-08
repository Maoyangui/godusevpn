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

    /** 引擎把新版 APK 下好(校验过 SHA256)后交给系统安装器;装完系统会杀掉本进程,开机接收器按 MY_PACKAGE_REPLACED 再把 VPN 拉起来。 */
    fun installApk(path: String) {
        val uri = FileProvider.getUriForFile(this, "$packageName.files", File(path))
        startActivity(Intent(Intent.ACTION_VIEW).setDataAndType(uri, "application/vnd.android.package-archive")
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_GRANT_READ_URI_PERMISSION))
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
        engine() // 早点起来:开机自启与磁贴都要用
    }
}
