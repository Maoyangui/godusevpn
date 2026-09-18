package com.maoyangui.godusevpn

import android.app.Activity
import android.app.UiModeManager
import android.content.ClipboardManager
import android.content.Intent
import android.content.pm.PackageManager
import android.content.res.Configuration
import android.net.Uri
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import android.util.Log
import android.webkit.ConsoleMessage
import android.webkit.JavascriptInterface
import android.webkit.RenderProcessGoneDetail
import android.webkit.WebChromeClient
import android.webkit.WebResourceError
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Toast
import androidx.activity.addCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import androidx.webkit.WebViewAssetLoader
import androidx.webkit.WebViewCompat
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * 界面就是 web/dist 那套页面,装进 WebView;页面的 window.go.main.App.* 经 api.js 转到这里的 JS 桥。
 * 桥是同步调用:call(name, argsJSON) → {"result":…} 或 {"error":"…"};事件由 Go 推过来再 evaluateJavascript 给页面。
 */
class MainActivity : AppCompatActivity() {
    companion object {
        // 跨 recreate() 存活(recreate 不重启进程):界面崩溃的计数与窗口起点
        @Volatile private var goneCount = 0
        @Volatile private var goneSince = 0L
        // 深链带来的订阅,等页面起来自己来取(见 handleDeepLink)。
        // 必须跟上面两个一样放在 companion 里:冷启动深链的时间窗正好是页面从 assets 装出来的那几百毫秒,
        // 这期间只要发生一次 recreate()(小内存电视上很常见),实例字段就没了,而 intent.data 已经被清空 ——
        // 深链彻底丢失,页面起来调 takePendingImport 也拿不到东西。
        @Volatile private var pendingImport: String? = null
    }

    private lateinit var web: WebView
    // 页面的调用在这里跑;几个线程足够,测速那种慢活也不会互相挡住
    private val bridgePool = java.util.concurrent.Executors.newFixedThreadPool(4)
    private val listener: (String, String) -> Unit = { name, data ->
        runOnUiThread { web.evaluateJavascript("window.__godEvent && window.__godEvent(${JSONObject.quote(name)}, ${JSONObject.quote(data)})", null) }
    }
    private val vpnPermission = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { r ->
        // 不再用一个「有没有挂起的连接请求」的字段来把关:授权框开着的时候 Activity 可能被销毁重建
        // (电视内存小,或者 onRenderProcessGone 走到 recreate()),挂起的请求会投递到**新实例**的回调上,
        // 而新实例的那个字段是 false —— 用户明明点了「允许」,却什么都没发生。
        // 走到这个回调本身就说明我们刚请求过授权,拿到 OK 就该连。
        if (r.resultCode == Activity.RESULT_OK) {
            Thread { runCatching { startConnect() } }.start()
        } else {
            // 遥控器上误触返回键的概率比手机高得多,得让用户知道为什么没连上
            runCatching { Toast.makeText(this, getString(R.string.vpn_denied), Toast.LENGTH_LONG).show() }
        }
    }
    private val notifyPermission = registerForActivityResult(ActivityResultContracts.RequestPermission()) { }

    /** 服务先起(通知栏显示"连接中",进程转前台),再让引擎连。 */
    private fun startConnect(): String {
        GodVpnService.start(this)
        return App.engine().call("Connect", "[]")
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        web = WebView(this)
        setContentView(web)
        with(web.settings) {
            javaScriptEnabled = true; domStorageEnabled = true; allowFileAccess = false
            mediaPlaybackRequiresUserGesture = false
            cacheMode = WebSettings.LOAD_NO_CACHE // 页面全在 assets 里,不走 HTTP 缓存;否则升级后 WebView 还会用旧版 JS / CSS
        }
        // 路径前缀去掉后剩下的部分相对 assets 根目录:注册 "/" 才能让 /web/index.html 落到 assets/web/index.html
        val loader = WebViewAssetLoader.Builder().addPathHandler("/", WebViewAssetLoader.AssetsPathHandler(this)).build()
        web.webViewClient = object : WebViewClient() {
            override fun shouldInterceptRequest(view: WebView, request: WebResourceRequest) = loader.shouldInterceptRequest(request.url)
            override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest): Boolean {
                val u = request.url
                if (u.host == "appassets.androidplatform.net") return false
                openExternal(u); return true // 外链交给浏览器
            }

            // 页面加载失败原来悄无声息,用户只看到白屏,我们也无从查起
            override fun onReceivedError(view: WebView, request: WebResourceRequest, error: WebResourceError) {
                if (request.isForMainFrame) Log.w(App.TAG, "页面加载失败 ${error.errorCode} ${error.description} ${request.url}")
            }

            // 渲染进程被系统杀掉(电视这类小内存设备最容易碰上):不接管的话整个应用跟着崩。
            // 重建一个 WebView 装回去;连接在服务里跑,不受影响。
            override fun onRenderProcessGone(view: WebView, detail: RenderProcessGoneDetail): Boolean {
                Log.w(App.TAG, "WebView 渲染进程没了(崩溃=${detail.didCrash()}),重建界面")
                if (view !== web) return true
                App.listeners.remove(listener) // 先摘监听:引擎推事件过来时那个 WebView 已经没了
                runCatching { (web.parent as? android.view.ViewGroup)?.removeView(web); web.destroy() }
                // 内存实在不够时重建也会马上再崩,无限重建只会让设备更卡。短时间内崩太多次就把界面关掉,
                // 连接在服务里跑,不受影响;用户下次自己打开时是干净的一轮。recreate() 不重启进程,所以计数存得住。
                val now = android.os.SystemClock.elapsedRealtime()
                if (now - goneSince > 60_000L) { goneSince = now; goneCount = 0 }
                goneCount++
                if (goneCount > 3) {
                    // 这里必须 finish,不能只退到后台。launchMode 是 singleTask:不结束的话从启动器再打开
                    // 命中的是同一个实例、onCreate 不会再跑,而它的内容视图刚刚被 removeView + destroy 掉了 ——
                    // 屏幕上就是永久空白,"下次打开是干净的一轮"根本不成立。而且连着 VPN 时进程是前台服务
                    // 优先级,系统几乎不会回收它,白屏会一直留着;电视上又没有"从最近任务划掉"的手势,
                    // 用户只能进系统设置强行停止,基本等于救不回来。
                    Log.w(App.TAG, "一分钟内界面崩了 $goneCount 次,不再重建,关掉界面(连接不受影响)")
                    runCatching { Toast.makeText(applicationContext, getString(R.string.webview_gone), Toast.LENGTH_LONG).show() }
                    finish()
                    return true
                }
                if (!isFinishing && !isDestroyed) recreate()
                return true
            }
        }
        // 不设 WebChromeClient 的话页面里的 confirm() / prompt() 会被直接当作"取消",删除订阅 / 规则组等确认就没反应。
        // 顺带把页面的 console 落进 logcat:老设备上页面报的错(比如脚本语法不被支持)就从这儿看。
        web.webChromeClient = object : WebChromeClient() {
            override fun onConsoleMessage(m: ConsoleMessage): Boolean {
                val line = "页面 ${m.messageLevel()} ${m.message()} @${m.sourceId()}:${m.lineNumber()}"
                if (m.messageLevel() == ConsoleMessage.MessageLevel.ERROR) Log.w(App.TAG, line) else Log.i(App.TAG, line)
                return true
            }
        }
        // WebView 的版本决定页面能用哪些写法。电视 / 盒子常年停在很旧的版本,出问题时这一行是第一手线索。
        Log.i(App.TAG, "WebView: " + (runCatching { WebViewCompat.getCurrentWebViewPackage(this)?.let { it.packageName + " " + it.versionName } }.getOrNull() ?: "取不到"))
        if (isTV()) { // 电视没有触摸,WebView 不主动要焦点的话遥控器方向键根本进不到页面里
            web.isFocusableInTouchMode = true
            web.requestFocus()
        }
        web.addJavascriptInterface(Bridge(), "GodusevpnBridge")
        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)
        web.loadUrl("https://appassets.androidplatform.net/web/index.html")
        App.listeners.add(listener)
        askNotificationPermission()
        handleDeepLink(intent)
        // 返回键:先让页面收面板 / 退回上一页,页面说没什么可退的(首页)就把应用放到后台,连接不受影响
        onBackPressedDispatcher.addCallback(this) {
            web.evaluateJavascript("window.__godBack ? window.__godBack() : false") { if (it != "true") moveTaskToBack(true) }
        }
    }

    /**
     * 电视 / 盒子:页面据此切横版布局并开遥控器焦点导航(页面里的方向键导航整个挂在 body.tv 上,
     * 这里判错就等于电视上根本没法用遥控器操作)。
     *
     * 原来只认 UI_MODE_TYPE_TELEVISION 和 leanback,而**国行电视(小米、创维、海信这些)不走 Google 认证,
     * 多半两个都不声明**,只声明 android.hardware.type.television。所以这里三条都认,再加一条没有触摸屏的兜底。
     */
    private fun isTV(): Boolean {
        val ui = getSystemService(UiModeManager::class.java)
        if (ui?.currentModeType == Configuration.UI_MODE_TYPE_TELEVISION) return true
        val pm = packageManager
        // FEATURE_TELEVISION 那个常量已废弃,直接写字符串,免得编译期告警
        if (pm.hasSystemFeature(PackageManager.FEATURE_LEANBACK) || pm.hasSystemFeature("android.hardware.type.television")) return true
        // 兜底:连触摸屏都没有的设备,只可能是电视或盒子
        return resources.configuration.touchscreen == Configuration.TOUCHSCREEN_NOTOUCH && !pm.hasSystemFeature(PackageManager.FEATURE_TOUCHSCREEN)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleDeepLink(intent)
    }

    /**
     * godusevpn://import?url=<订阅地址>[&name=<订阅名>],落地页的一键导入。
     *
     * 冷启动时**推不过去**:onCreate 里 loadUrl 是异步的,页面从 assets 装出来要几百毫秒,
     * 而这个 post 在下一轮主线程循环就跑了 —— 那时 window.__godEvent 还不存在,
     * `window.__godEvent &&` 直接短路,什么都不发生,连一行日志都没有。
     * 于是"应用没开着时从落地页点一键导入"必然失败,而电视上遥控器打字几乎不可能,
     * 这条路等于是唯一能加订阅的通路。
     *
     * 所以改成:存一份,同时推一次。页面起来后主动来取(takePendingImport),两条路哪条先到都行。
     * 取走即清空,免得 recreate() 带着同一个 intent 重进 onCreate 时把用户正在填的表单又冲掉一遍。
     */
    private fun handleDeepLink(intent: Intent?) {
        val u = intent?.data ?: return
        val url = u.getQueryParameter("url") ?: return
        val payload = JSONObject().put("url", url).put("name", u.getQueryParameter("name") ?: "").toString()
        pendingImport = payload
        intent.data = null // 这个 intent 已经消费过了:recreate() / 从最近任务恢复时别再触发一次
        web.post { web.evaluateJavascript("window.__godEvent && window.__godEvent('import', ${JSONObject.quote(payload)})", null) }
    }

    override fun onDestroy() {
        App.listeners.remove(listener)
        bridgePool.shutdownNow()
        super.onDestroy()
    }

    /**
     * 连接前要先拿到系统的 VPN 授权(第一次会弹系统对话框)。
     *
     * launch 一定要兜住:VpnService.prepare() 返回的是一个**显式**指向 com.android.vpndialogs 的 Intent,
     * 系统并不检查那个组件在不在。不走 Google 认证的国行电视 / 盒子常把它裁掉或禁掉,launch 会抛
     * ActivityNotFoundException —— 而它跑在 runOnUiThread 的 Runnable 里,桥那层的 try/catch 在别的线程上,
     * 罩不到,整个应用直接闪退,而且每次点连接都闪退。同一个文件里 openExternal 早就为同类问题做了防护,
     * 这里是唯一漏掉的一处。
     */
    private fun connect(): String {
        val prep = VpnService.prepare(this)
        if (prep != null) {
            runOnUiThread {
                // 捕 Exception 而不只是 ActivityNotFoundException:授权框开着时 Activity 被销毁,
                // ActivityResultRegistry 会把 key 反注册掉,这时 launch 抛的是 IllegalStateException。
                runCatching { vpnPermission.launch(prep) }.onFailure {
                    Log.w(App.TAG, "打不开系统 VPN 授权界面", it)
                    runCatching { Toast.makeText(this, getString(R.string.no_vpn_dialog), Toast.LENGTH_LONG).show() }
                }
            }
            return App.engine().call("GetState", "[]")
        }
        return startConnect()
    }

    /**
     * Android 13 起通知要用户点头才显示。前台服务通知是「断开」按钮的所在,电视上尤其重要 ——
     * 抽屉深处的入口够不着时,通知栏是另一条路。申请动作本身也要兜住:电视 ROM 上可能根本没有权限对话框。
     */
    private fun askNotificationPermission() {
        if (Build.VERSION.SDK_INT < 33) return
        if (checkSelfPermission(android.Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED) return
        runCatching { notifyPermission.launch(android.Manifest.permission.POST_NOTIFICATIONS) }
            .onFailure { Log.w(App.TAG, "申请通知权限失败", it) }
    }


    // 交给系统浏览器打开。电视盒子之类的设备可能根本没有浏览器,startActivity 会直接抛异常 ——
    // 它跑在 runOnUiThread 的 Runnable 里,桥那层的 try/catch 罩不到,整个应用会被带崩。
    // 这里兜住并提示;也只放行 http(s),别的协议不该从页面里冒出来。
    private fun openExternal(u: Uri) {
        if (u.scheme != "http" && u.scheme != "https") return
        try {
            startActivity(Intent(Intent.ACTION_VIEW, u))
        } catch (e: Exception) {
            Toast.makeText(this, getString(R.string.no_browser, u.toString()), Toast.LENGTH_LONG).show()
        }
    }

    inner class Bridge {
        @JavascriptInterface
        fun isTV(): Boolean = this@MainActivity.isTV()

        /** 页面起来后主动来取深链带来的订阅(冷启动时事件推得比页面早,推不过去)。取走即清空。 */
        @JavascriptInterface
        fun takePendingImport(): String {
            val p = pendingImport ?: return ""
            pendingImport = null
            return p
        }

        /**
         * 异步调用:立刻返回,活干完了再把结果回调给页面。
         * 同步调用会占着页面线程(测全部节点这类要好几秒),面板就"卡一下才弹出来"。
         */
        @JavascriptInterface
        fun callAsync(reqId: String, name: String, argsJSON: String) {
            bridgePool.execute {
                val payload = call(name, argsJSON)
                runOnUiThread {
                    if (!isFinishing && !isDestroyed) {
                        web.evaluateJavascript("window.__godResolve && window.__godResolve(${JSONObject.quote(reqId)}, ${JSONObject.quote(payload)})", null)
                    }
                }
            }
        }

        @JavascriptInterface
        fun call(name: String, argsJSON: String): String {
            return try {
                val result = when (name) {
                    "Connect" -> connect()
                    "ReadClipboard" -> JSONObject.quote((getSystemService(ClipboardManager::class.java).primaryClip?.getItemAt(0)?.coerceToText(this@MainActivity) ?: "").toString())
                    "OpenURL" -> { val u = Uri.parse(JSONArray(argsJSON).optString(0)); runOnUiThread { this@MainActivity.openExternal(u) }; "null" }
                    // 系统的 VPN 设置页:把本应用设成「始终开启」+「阻止未经 VPN 的连接」,进程被杀了也不漏(应用自己做不到跨进程)
                    "OpenVpnSettings" -> { runOnUiThread { runCatching { startActivity(Intent(android.provider.Settings.ACTION_VPN_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }.onFailure { Toast.makeText(this@MainActivity, "打不开系统 VPN 设置", Toast.LENGTH_SHORT).show() } }; "null" }
                    "ExportDiag" -> {
                        val path: String = App.engine().call("ExportDiag", "[]").trim('"')
                        share(File(path)); JSONObject.quote(path)
                    }
                    "HideWindow", "Minimize" -> { runOnUiThread { moveTaskToBack(true) }; "null" }
                    "OpenLogs" -> "null" // 没有文件管理器可开;页面在 Android 上不显示这个按钮
                    "QuitApp" -> { quit(); "null" }
                    else -> App.engine().call(name, argsJSON)
                }
                "{\"result\":$result}"
            } catch (e: Exception) {
                Log.w(App.TAG, "bridge $name: ${e.message ?: e}") // 业务错误(内核未运行之类)页面会提示,这里不必打堆栈
                JSONObject().put("error", e.message ?: e.toString()).toString()
            }
        }
    }

    /** 关于 → 退出:断开连接、停掉前台服务、关掉界面并结束进程(引擎随进程一起结束)。 */
    private fun quit() {
        Thread {
            runCatching { App.engine().call("Disconnect", "[]") }
            runCatching { stopService(Intent(this, GodVpnService::class.java)) }
            runOnUiThread { finishAndRemoveTask() }
            Thread.sleep(400)
            android.os.Process.killProcess(android.os.Process.myPid())
        }.start()
    }

    private fun share(f: File) {
        val uri = FileProvider.getUriForFile(this, "$packageName.files", f)
        startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).setType("application/zip").putExtra(Intent.EXTRA_STREAM, uri).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION), getString(R.string.share_diag)))
    }
}
