package com.maoyangui.godusevpn

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.VpnService
import org.json.JSONObject

/**
 * 开机 / 升级后:引擎自己会按上次状态重连(state.json 里 wanted);这里把进程拉起来、确认 VPN 授权还在,
 * 并且在广播的窗口期里就把前台服务起好(Android 12 起后台不能再起前台服务,等引擎开 TUN 时再起就晚了)。
 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (VpnService.prepare(context) != null) return // 授权被收回,只能等用户打开界面
        // onReceive 跑在主线程,而广播只有十秒。App.engine() 第一次调用会把整个守护进程建起来
        // (建目录、开日志、读设置与订阅缓存、落一次盘),慢速电视盒子上很容易超时 ANR。
        // 用 goAsync() 把广播的生命周期延长,活挪到后台线程做。
        val pending = goAsync()
        Thread {
            try {
                val engine = App.engine()
                val wanted = runCatching {
                    JSONObject(engine.state()).optJSONObject("view")?.optJSONObject("state")?.optBoolean("wanted") == true
                }.getOrDefault(false)
                if (wanted) GodVpnService.start(context)
            } finally {
                pending.finish()
            }
        }.start()
    }
}
