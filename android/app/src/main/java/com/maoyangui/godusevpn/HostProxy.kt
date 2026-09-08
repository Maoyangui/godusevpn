package com.maoyangui.godusevpn

import android.net.VpnService
import android.util.Log
import com.maoyangui.godusevpn.mobile.Host
import com.maoyangui.godusevpn.mobile.InterfaceListener

/**
 * Go 引擎看到的宿主。TUN 只能由正在运行的 VpnService 建,所以开 TUN 时先把服务拉起来再转交;
 * 其它方法不依赖服务,直接用 Application 的上下文做。
 */
object HostProxy : Host {
    private fun svc(): GodVpnService? = GodVpnService.instance

    /** protect 只看这个 uid 有没有拿到 VPN 授权,不依赖服务是否在跑;服务还没起来(内核启动时先下规则集)就用一个空实例调。 */
    private val protector by lazy { object : VpnService() {} }

    override fun openTun(specJSON: String): Int {
        val s = svc() ?: GodVpnService.start(App.instance, 8000)
        if (s == null) {
            Log.e(App.TAG, "VPN 服务没起来,开不了 TUN")
            return -1
        }
        return s.openTun(specJSON)
    }

    override fun protect(fd: Int): Boolean = runCatching { (svc() ?: protector).protect(fd) }.getOrDefault(false)

    override fun closeTun() { svc()?.closeTun() }

    override fun findConnectionOwner(ipProtocol: Int, sourceAddress: String, sourcePort: Int, destinationAddress: String, destinationPort: Int): Int =
        NetInfo.findConnectionOwner(App.instance, ipProtocol, sourceAddress, sourcePort, destinationAddress, destinationPort)

    override fun packageNamesByUid(uid: Int): String =
        App.instance.packageManager.getPackagesForUid(uid)?.joinToString(",") ?: ""

    override fun networkInterfaces(): String = NetInfo.interfacesJSON(App.instance)

    override fun startDefaultInterfaceMonitor(listener: InterfaceListener): Boolean = NetInfo.startMonitor(App.instance, listener)

    override fun stopDefaultInterfaceMonitor() = NetInfo.stopMonitor(App.instance)

    override fun log(level: String, message: String) {
        when (level.uppercase()) {
            "ERROR", "FATAL", "PANIC" -> Log.e(App.TAG, message)
            "WARN" -> Log.w(App.TAG, message)
            "DEBUG", "TRACE" -> Log.d(App.TAG, message)
            else -> Log.i(App.TAG, message)
        }
    }
}
