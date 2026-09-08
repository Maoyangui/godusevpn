package com.maoyangui.godusevpn

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.os.Build
import android.util.Log
import mobile.InterfaceListener
import org.json.JSONArray
import org.json.JSONObject
import java.net.InetSocketAddress
import java.net.NetworkInterface

/** 给 Go 引擎的网络信息:接口列表、连接归属、默认网络变化。 */
object NetInfo {
    private var callback: ConnectivityManager.NetworkCallback? = null

    fun findConnectionOwner(ctx: Context, proto: Int, srcAddr: String, srcPort: Int, dstAddr: String, dstPort: Int): Int {
        if (Build.VERSION.SDK_INT < 29) return -1
        return try {
            val cm = ctx.getSystemService(ConnectivityManager::class.java)
            cm.getConnectionOwnerUid(proto, InetSocketAddress(srcAddr, srcPort), InetSocketAddress(dstAddr, dstPort))
        } catch (e: Exception) { -1 }
    }

    /** 每个接口的类型 / DNS / 网关来自 ConnectivityManager 里对应的网络。 */
    fun interfacesJSON(ctx: Context): String {
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        val byName = HashMap<String, Pair<NetworkCapabilities?, LinkProperties?>>()
        for (n in cm.allNetworks) {
            val lp = cm.getLinkProperties(n) ?: continue
            val name = lp.interfaceName ?: continue
            byName[name] = Pair(cm.getNetworkCapabilities(n), lp)
        }
        val arr = JSONArray()
        val ifaces = try { NetworkInterface.getNetworkInterfaces()?.toList() ?: emptyList() } catch (e: Exception) { emptyList() }
        for (nif in ifaces) {
            val o = JSONObject()
            o.put("index", nif.index); o.put("mtu", runCatching { nif.mtu }.getOrDefault(1500)); o.put("name", nif.name)
            o.put("up", runCatching { nif.isUp }.getOrDefault(false)); o.put("loopback", runCatching { nif.isLoopback }.getOrDefault(false))
            o.put("p2p", runCatching { nif.isPointToPoint }.getOrDefault(false)); o.put("multicast", runCatching { nif.supportsMulticast() }.getOrDefault(false))
            val addrs = JSONArray()
            for (ia in nif.interfaceAddresses) {
                val host = ia.address.hostAddress?.substringBefore('%') ?: continue
                addrs.put("$host/${ia.networkPrefixLength}")
            }
            o.put("addresses", addrs)
            val (caps, lp) = byName[nif.name] ?: Pair(null, null)
            o.put("type", when {
                caps == null -> if (nif.name.startsWith("wlan")) "wifi" else if (nif.name.startsWith("rmnet") || nif.name.startsWith("ccmni")) "cellular" else if (nif.name.startsWith("eth")) "ethernet" else "other"
                caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
                caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
                caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
                else -> "other"
            })
            o.put("metered", caps != null && !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED))
            val dns = JSONArray(); lp?.dnsServers?.forEach { dns.put(it.hostAddress?.substringBefore('%')) }; o.put("dns", dns)
            val gws = JSONArray(); lp?.routes?.forEach { r -> r.gateway?.let { if (!it.isAnyLocalAddress) gws.put(it.hostAddress?.substringBefore('%')) } }; o.put("gateways", gws)
            arr.put(o)
        }
        return arr.toString()
    }

    fun startMonitor(ctx: Context, listener: InterfaceListener): Boolean {
        stopMonitor(ctx)
        val cm = ctx.getSystemService(ConnectivityManager::class.java)
        val cb = object : ConnectivityManager.NetworkCallback() {
            private fun report(n: Network?) {
                if (n == null) { listener.update("", -1, false, false); return }
                val lp = cm.getLinkProperties(n); val caps = cm.getNetworkCapabilities(n)
                val name = lp?.interfaceName ?: run { listener.update("", -1, false, false); return }
                val idx = runCatching { NetworkInterface.getByName(name)?.index ?: -1 }.getOrDefault(-1)
                val metered = caps != null && !caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)
                listener.update(name, idx, metered, false)
            }
            override fun onAvailable(network: Network) = report(network)
            override fun onLinkPropertiesChanged(network: Network, lp: LinkProperties) = report(network)
            override fun onLost(network: Network) = report(null)
        }
        return try { cm.registerDefaultNetworkCallback(cb); callback = cb; true } catch (e: Exception) { Log.w(App.TAG, "monitor", e); false }
    }

    fun stopMonitor(ctx: Context) {
        val cb = callback ?: return
        callback = null
        runCatching { ctx.getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(cb) }
    }
}
