package com.maoyangui.godusevpn

import android.app.Activity
import android.content.ClipboardManager
import android.content.Intent
import android.net.Uri
import android.net.VpnService
import android.os.Bundle
import android.util.Log
import android.webkit.JavascriptInterface
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
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
        if (r.resultCode == Activity.RESULT_OK && pendingConnect) Thread { runCatching { App.engine().call("Connect", "[]") } }.start()
        pendingConnect = false
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        web = WebView(this)
        setContentView(web)
        with(web.settings) {
            javaScriptEnabled = true; domStorageEnabled = true; allowFileAccess = false
            mediaPlaybackRequiresUserGesture = false
        }
        val loader = WebViewAssetLoader.Builder().addPathHandler("/web/", WebViewAssetLoader.AssetsPathHandler(this)).build()
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
        return App.engine().call("Connect", "[]")
    }

    inner class Bridge {
        @JavascriptInterface
        fun call(name: String, argsJSON: String): String {
            return try {
                val result = when (name) {
                    "Connect" -> connect()
                    "ReadClipboard" -> JSONObject.quote((getSystemService(ClipboardManager::class.java).primaryClip?.getItemAt(0)?.coerceToText(this@MainActivity) ?: "").toString())
                    "OpenURL" -> { runOnUiThread { startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(JSONArray(argsJSON).optString(0)))) }; "null" }
                    "ExportDiag" -> {
                        val path = App.engine().call("ExportDiag", "[]").trim('"')
                        share(File(path)); JSONObject.quote(path)
                    }
                    "HideWindow", "Minimize" -> { runOnUiThread { moveTaskToBack(true) }; "null" }
                    else -> App.engine().call(name, argsJSON)
                }
                "{\"result\":$result}"
            } catch (e: Exception) {
                Log.w(App.TAG, "bridge $name", e)
                JSONObject().put("error", e.message ?: e.toString()).toString()
            }
        }
    }

    private fun share(f: File) {
        val uri = FileProvider.getUriForFile(this, "$packageName.files", f)
        startActivity(Intent.createChooser(Intent(Intent.ACTION_SEND).setType("application/zip").putExtra(Intent.EXTRA_STREAM, uri).addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION), getString(R.string.share_diag)))
    }
}
