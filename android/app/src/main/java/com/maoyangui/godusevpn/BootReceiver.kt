package com.maoyangui.godusevpn

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.net.VpnService

/** 开机 / 升级后:引擎自己会按上次状态重连(state.json 里 wanted);这里只负责把进程拉起来并确认 VPN 授权还在。 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (VpnService.prepare(context) != null) return // 授权被收回,只能等用户打开界面
        App.engine()
    }
}
