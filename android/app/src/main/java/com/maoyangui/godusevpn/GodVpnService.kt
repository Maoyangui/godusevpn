package com.maoyangui.godusevpn

import android.app.Notification
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import android.util.Log
import androidx.core.app.NotificationCompat
import org.json.JSONObject

/**
 * VpnService:把 Go 引擎要的 TUN 建出来(参数是引擎给的 JSON),前台通知显示状态并提供"断开"。
 * 引擎本身在 App 进程里常驻,服务只是 TUN 的载体。
 */
class GodVpnService : VpnService() {
    companion object {
        @Volatile var instance: GodVpnService? = null
        const val ACTION_DISCONNECT = "com.maoyangui.godusevpn.DISCONNECT"
        private const val NOTIFY_ID = 1

        /**
         * 把服务拉起来(前台通知从"连接中"开始),最多等 waitMs 毫秒拿到实例。
         * 连接一开始就起,而不是等到开 TUN:进程在后台也不会被收掉,开机自启也要在广播的窗口期内起前台服务。
         */
        fun start(ctx: Context, waitMs: Long = 0): GodVpnService? {
            instance?.let { return it }
            try {
                val intent = Intent(ctx, GodVpnService::class.java)
                if (Build.VERSION.SDK_INT >= 26) ctx.startForegroundService(intent) else ctx.startService(intent)
            } catch (e: Exception) {
                Log.e(App.TAG, "start vpn service", e); return null
            }
            val deadline = System.currentTimeMillis() + waitMs
            while (instance == null && System.currentTimeMillis() < deadline) Thread.sleep(50)
            return instance
        }
    }

    private var tun: ParcelFileDescriptor? = null
    private val listener: (String, String) -> Unit = { name, data -> if (name == "state") runCatching { updateNotification(JSONObject(data)) } }

    override fun onCreate() {
        super.onCreate()
        instance = this
        startForeground(NOTIFY_ID, buildNotification(getString(R.string.st_connecting), null))
        App.listeners.add(listener)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_DISCONNECT) {
            Thread { runCatching { App.engine().call("Disconnect", "[]") } }.start()
        }
        return START_STICKY
    }

    override fun onDestroy() {
        App.listeners.remove(listener)
        instance = null
        closeTun()
        super.onDestroy()
    }

    override fun onRevoke() { // 别的 VPN 抢了
        Thread { runCatching { App.engine().call("Disconnect", "[]") } }.start()
        super.onRevoke()
    }

    /** 按引擎给的 TunSpec 建 VPN,返回 fd(所有权交给引擎,自己留一个句柄用于关闭)。 */
    fun openTun(specJSON: String): Int {
        closeTun()
        val spec = JSONObject(specJSON)
        val b = Builder().setSession(getString(R.string.app_name)).setMtu(spec.optInt("mtu", 9000).coerceIn(1280, 65535))
        for (a in spec.optJSONArray("inet4Address").strings()) addr(b, a)
        for (a in spec.optJSONArray("inet6Address").strings()) addr(b, a)
        for (d in spec.optJSONArray("dns").strings()) runCatching { b.addDnsServer(d) }
        if (spec.optBoolean("autoRoute")) {
            for (r in spec.optJSONArray("inet4Routes").strings()) route(b, r)
            for (r in spec.optJSONArray("inet6Routes").strings()) route(b, r)
        }
        // 注意:不能把自己排除在 VPN 之外。内核的出站套接字都经 protect 绕过隧道;而 TCP 走的系统协议栈要把回包送回 TUN,
        // 靠的是本应用的套接字用 VPN 的路由表 —— 排除了自己,回包就从 WiFi 漏走,表现为 DNS 通、TCP 全断(真机实测)。
        for (p in spec.optJSONArray("includePackages").strings()) runCatching { b.addAllowedApplication(p) }
        for (p in spec.optJSONArray("excludePackages").strings()) runCatching { b.addDisallowedApplication(p) }
        if (spec.optBoolean("httpProxyEnabled") && Build.VERSION.SDK_INT >= 29) {
            runCatching { b.setHttpProxy(android.net.ProxyInfo.buildDirectProxy(spec.optString("httpProxyServer"), spec.optInt("httpProxyServerPort"))) }
        }
        if (Build.VERSION.SDK_INT >= 29) b.setMetered(false)
        b.setBlocking(false)
        val pfd = try { b.establish() } catch (e: Exception) { Log.e(App.TAG, "establish", e); null } ?: return -1
        tun = pfd
        return pfd.fd
    }

    private fun addr(b: Builder, cidr: String) {
        val (ip, len) = cidr.split('/').let { Pair(it[0], it.getOrNull(1)?.toIntOrNull() ?: if (it[0].contains(':')) 128 else 32) }
        runCatching { b.addAddress(ip, len) }.onFailure { Log.w(App.TAG, "addAddress $cidr: $it") }
    }

    private fun route(b: Builder, cidr: String) {
        val (ip, len) = cidr.split('/').let { Pair(it[0], it.getOrNull(1)?.toIntOrNull() ?: 0) }
        runCatching { b.addRoute(ip, len) }.onFailure { Log.w(App.TAG, "addRoute $cidr: $it") }
    }

    /** 关掉自己留着的那份 fd,系统的 VPN 随之消失。内核关 TUN 时经宿主接口调;状态变成断开 / 出错时再兜一次底。 */
    fun closeTun() {
        val t = tun ?: return
        tun = null
        runCatching { t.close() }
        Log.i(App.TAG, "TUN 已关闭")
    }

    private fun buildNotification(text: String, sub: String?): Notification {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val disc = PendingIntent.getService(this, 1, Intent(this, GodVpnService::class.java).setAction(ACTION_DISCONNECT), PendingIntent.FLAG_IMMUTABLE)
        return NotificationCompat.Builder(this, App.CHANNEL)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text + (sub?.let { " · $it" } ?: ""))
            .setContentIntent(open)
            .addAction(0, getString(R.string.act_disconnect), disc)
            .setOngoing(true).setOnlyAlertOnce(true).setSilent(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build()
    }

    private fun updateNotification(state: JSONObject) {
        val view = state.optJSONObject("view") ?: return
        val status = view.optJSONObject("state")?.optString("status") ?: ""
        val text = when (status) {
            "connected" -> getString(R.string.st_connected)
            "degraded" -> getString(R.string.st_degraded)
            "preparing", "starting" -> getString(R.string.st_connecting)
            "error" -> getString(R.string.st_error)
            else -> getString(R.string.st_disconnected)
        }
        val node = view.optString("autoNow").ifEmpty { view.optString("node") }
        if (status == "disconnected" || status == "error" || status == "stopping") closeTun() // 内核不在跑就别让 VPN 接口留着吞流量
        val n = buildNotification(text, if (status == "connected") node else null)
        if (Build.VERSION.SDK_INT >= 29) startForeground(NOTIFY_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE) else startForeground(NOTIFY_ID, n)
        if (status == "disconnected" && !view.optJSONObject("state")!!.optBoolean("wanted")) {
            stopForeground(STOP_FOREGROUND_REMOVE)
            stopSelf()
        }
    }
}

private fun org.json.JSONArray?.strings(): List<String> {
    if (this == null) return emptyList()
    val out = ArrayList<String>(length())
    for (i in 0 until length()) optString(i).takeIf { it.isNotEmpty() }?.let { out.add(it) }
    return out
}
