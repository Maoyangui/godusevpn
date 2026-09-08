package com.maoyangui.godusevpn

import android.app.Activity
import android.app.UiModeManager
import android.content.ClipboardManager
import android.content.Intent
import android.content.pm.PackageManager
import android.content.res.Configuration
import android.net.Uri
import android.net.VpnService
import android.os.Bundle
import android.util.Log
import android.webkit.JavascriptInterface
import android.webkit.WebResourceRequest
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.addCallback
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.FileProvider
import androidx.webkit.WebViewAssetLoader
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * 界面就是 web/dist 那套页面,装进 WebView;页面的 window.go.main.App.* 经 api.js 转到这里的 JS 桥。
 * 桥是同步调用:call(name, argsJSON) → {"result":…} 或 {"error":"…"};事件由 Go 推过来再 evaluateJavascript 给页面。
 */
class MainActivity : AppCompatActivity() {
    private lateinit var web: WebView
    private var pendingConnect = false
    private val listener: (String, String) -> Unit = { name, data ->
        runOnUiThread { web.evaluateJavascript("window.__godEvent && window.__godEvent(${JSONObject.quote(name)}, ${JSONObject.quote(data)})", null) }
    }
    private val vpnPermission = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { r ->
        if (r.resultCode == Activity.RESULT_OK && pendingConnect) Thread { runCatching { startConnect() } }.start()
        pendingConnect = false
    }

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
                startActivity(Intent(Intent.ACTION_VIEW, u)); return true // 外链交给浏览器
            }
        }
        web.addJavascriptInterface(Bridge(), "GodusevpnBridge")
        WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG)
        web.loadUrl("https://appassets.androidplatform.net/web/index.html")
        App.listeners.add(listener)
        handleDeepLink(intent)
        // 返回键:先让页面收面板 / 退回上一页,页面说没什么可退的(首页)就把应用放到后台,连接不受影响
        onBackPressedDispatcher.addCallback(this) {
            web.evaluateJavascript("window.__godBack ? window.__godBack() : false") { if (it != "true") moveTaskToBack(true) }
        }
    }

    /** 电视 / 盒子:页面据此切横版布局并开遥控器焦点导航。 */
    private fun isTV(): Boolean {
        val ui = getSystemService(UiModeManager::class.java)
        return ui?.currentModeType == Configuration.UI_MODE_TYPE_TELEVISION || packageManager.hasSystemFeature(PackageManager.FEATURE_LEANBACK)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleDeepLink(intent)
    }

    private fun handleDeepLink(intent: Intent?) {
        val u = intent?.data ?: return
        val url = u.getQueryParameter("url") ?: return
        web.post { web.evaluateJavascript("window.__godEvent && window.__godEvent('import', ${JSONObject.quote(JSONObject.quote(url))})", null) }
    }

    override fun onDestroy() {
        App.listeners.remove(listener)
        super.onDestroy()
    }

    /** 连接前要先拿到系统的 VPN 授权(第一次会弹系统对话框)。 */
    private fun connect(): String {
        val prep = VpnService.prepare(this)
        if (prep != null) {
            pendingConnect = true
            runOnUiThread { vpnPermission.launch(prep) }
            return App.engine().call("GetState", "[]")
        }
        return startConnect()
    }

    inner class Bridge {
        @JavascriptInterface
        fun isTV(): Boolean = this@MainActivity.isTV()

        @JavascriptInterface
        fun call(name: String, argsJSON: String): String {
            return try {
                val result = when (name) {
                    "Connect" -> connect()
                    "ReadClipboard" -> JSONObject.quote((getSystemService(ClipboardManager::class.java).primaryClip?.getItemAt(0)?.coerceToText(this@MainActivity) ?: "").toString())
                    "OpenURL" -> { runOnUiThread { startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(JSONArray(argsJSON).optString(0)))) }; "null" }
                    "ExportDiag" -> {
                        val path: String = App.engine().call("ExportDiag", "[]").trim('"')
                        share(File(path)); JSONObject.quote(path)
                    }
                    "HideWindow", "Minimize" -> { runOnUiThread { moveTaskToBack(true) }; "null" }
                    else -> App.engine().call(name, argsJSON)
                }
                "{\"result\":$result}"
            } catch (e: Exception) {
                Log.w(App.TAG, "bridge $name: ${e.message ?: e}") // 业务错误(内核未运行之类)页面会提示,这里不必打堆栈
                JSONObject().put("error", e.message ?: e.toString()).toString()
            }
        }
    }

    private fun share(f: File) {
        val uri = FileProvider.getUriForFile(this, "$packageName.files", f)
        startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).setType("application/zip").putExtra(Intent.EXTRA_STREAM, uri).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION), getString(R.string.share_diag)))
    }
}
