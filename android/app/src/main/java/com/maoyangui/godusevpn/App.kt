package com.maoyangui.godusevpn

import android.app.Application
import android.app.NotificationChannel
import android.app.NotificationManager
import android.os.Build
import android.util.Log
import mobile.Engine
import mobile.EventListener
import mobile.Mobile
import org.json.JSONObject
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
            engine ?: Mobile.newEngine(instance.filesDir.absolutePath, HostProxy, object : EventListener {
                override fun onEvent(name: String, dataJSON: String) {
                    if (name == "state") runCatching { lastState = JSONObject(dataJSON) }
                    for (l in listeners) runCatching { l(name, dataJSON) }.onFailure { Log.w(TAG, "listener", it) }
                }
            }).also { engine = it }
        }
    }

    override fun onCreate() {
        super.onCreate()
        instance = this
        if (Build.VERSION.SDK_INT >= 26) {
            val nm = getSystemService(NotificationManager::class.java)
            nm.createNotificationChannel(NotificationChannel(CHANNEL, getString(R.string.channel_vpn), NotificationManager.IMPORTANCE_LOW).apply {
                setShowBadge(false)
            })
        }
        engine() // 早点起来:开机自启与磁贴都要用
    }
}
