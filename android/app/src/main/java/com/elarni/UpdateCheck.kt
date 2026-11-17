package com.elarni

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.Build
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.File

/**
 * The update notice and the update: reads the feed the user set once a day, or hourly while it cannot be read, downloads the
 * release's APK and hands it to Android's installer. Android installs it only if it is signed like this app and is not older.
 */
object UpdateCheck {
    /** Instrumentation replaces the network. The app's own sockets bypass its VPN, so a connected fetch goes through the core. */
    @Volatile var fetch: (String) -> String = { url ->
        Native.cancellable("fetchSubscription", obj("url" to url, "proxy" to (Engine.fetchProxy ?: JSONObject.NULL))).getString("body")
    }
    /** Downloads the release's APK into a directory that holds nothing else, checks the release key's signature and returns the file's name. */
    @Volatile var download: (JSONArray, File) -> String = { assets, directory ->
        Native.cancellable("downloadUpdate", obj("assets" to assets, "resourceKind" to "update", "destination" to directory.absolutePath,
            "proxy" to (Engine.fetchProxy ?: JSONObject.NULL))).getString("name")
    }
    /** Hands the APK to Android's installer. A quiet install is dropped, not shown, when Android wants its user to confirm it. */
    @Volatile var install: (Context, File, Boolean) -> Unit = ::commit
    /** Whether the user let Elarni install apps, which Android asks for on a settings page of its own. */
    @Volatile var allowed: (Context) -> Boolean = { it.packageManager.canRequestPackageInstalls() }
    /** Whether a network that is not the tunnel carries data its user does not pay for by the megabyte. The tunnel itself never counts as metered. */
    @Volatile var unmetered: (Context) -> Boolean = { context ->
        val manager = context.getSystemService(ConnectivityManager::class.java)
        @Suppress("DEPRECATION") manager.allNetworks.any { network ->
            manager.getNetworkCapabilities(network)?.let { it.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN) && it.hasCapability(NetworkCapabilities.NET_CAPABILITY_VALIDATED) &&
                it.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED) } == true
        }
    }
    /** Release builds read Elarni's releases until the user changes or clears the feed; debug builds start with it off. */
    const val ELARNI_RELEASES = "https://api.github.com/repos/Shilmoon/elarni/releases/latest"
    const val QUIET = "quiet"
    private const val RECONNECT = "reconnectAfterUpdate"
    private const val CHECKED = "updateChecked"
    private const val FAILED = "failedUpdate"
    /** A newer release's version and page. */
    val available = MutableStateFlow<Pair<String, String>?>(null)
    /** What became of that release's APK: "none", "downloading", "ready", "installing" or "failed". */
    val stage = MutableStateFlow("none")
    /** Whether that release can install itself: its feed lists the files and no download of it was refused. Otherwise the notice links to its page. */
    val installable = MutableStateFlow(false)
    /** Whether the app's screen shows. An update installs by itself only while it does not. */
    @Volatile var foreground = false
    private var checked: Triple<String, Long, Boolean>? = null
    private var assets = JSONArray()
    private var staged: File? = null
    private var refused = ""
    private var retryAt = 0L
    private val lock = Mutex()
    private val work = Mutex()
    private val directory get() = File(ElarniApplication.instance.cacheDir, "update")
    // The release key decided, or the release has no APK: asking again gets the same answer.
    private val refusals = setOf("update_unsigned", "update_signature_invalid", "update_invalid", "update_package_missing", "update_not_newer", "update_checksum_mismatch")

    // A debug build's name ends in -dev, which marks the build type, not a pre-release.
    suspend fun refresh(feed: String, version: String = BuildConfig.VERSION_NAME.substringBefore('-')) = lock.withLock {
        if (feed.isEmpty()) { checked = null; show(null, JSONArray()); return@withLock }
        val now = System.currentTimeMillis()
        checked?.let { (url, at, read) -> if (url == feed && now - at < if (read) 86_400_000 else 3_600_000) return@withLock }
        val body = withContext(Dispatchers.IO) { runCatching { fetch(feed) }.getOrNull() }
        val release = body?.let(::parse)
        checked = Triple(feed, now, release != null)
        val newer = release?.takeIf { (compare(it.first, version) ?: 0) > 0 }
        // The feed names nothing newer, so what an earlier build downloaded is this build or older.
        if (release != null && newer == null) withContext(Dispatchers.IO) { runCatching { directory.deleteRecursively() } }
        show(newer, body?.let(::assets) ?: JSONArray())
    }

    private fun show(release: Pair<String, String>?, files: JSONArray) {
        // Another release starts over; the same one keeps its download.
        if (release?.first != available.value?.first) { stage.value = "none"; staged = null; retryAt = 0 }
        assets = files
        // A recording build stays the build its user was asked to record with.
        installable.value = release != null && files.length() > 0 && !BuildConfig.ELARNI_RECORDING && refused != release.first &&
            prefs(ElarniApplication.instance).getString(FAILED, "") != release.first
        available.value = release
    }

    /** Elarni's own {"version", "url"} or a GitHub releases/latest reply, whose own url is its API address. */
    fun parse(body: String): Pair<String, String>? = runCatching {
        val value = JSONObject(body)
        val github = value.has("tag_name")
        val version = value.opt(if (github) "tag_name" else "version") as? String ?: return null
        val url = value.opt(if (github) "html_url" else "url") as? String ?: return null
        if (compare(version, "0") == null || !isHttps(url)) null else version.removePrefix("v").removePrefix("V") to url
    }.getOrNull()

    /** The release's files as {"name", "url"}: a GitHub reply names each download in browser_download_url, Elarni's own form in url. */
    fun assets(body: String): JSONArray = runCatching {
        val value = JSONObject(body)
        val key = if (value.has("tag_name")) "browser_download_url" else "url"
        val list = value.optJSONArray("assets") ?: return JSONArray()
        JSONArray((0 until minOf(list.length(), 200)).mapNotNull { index ->
            val item = list.optJSONObject(index) ?: return@mapNotNull null
            val name = item.opt("name") as? String ?: return@mapNotNull null
            val url = item.opt(key) as? String ?: return@mapNotNull null
            if (name.isEmpty() || !isHttps(url)) null else obj("name" to name, "url" to url)
        })
    }.getOrDefault(JSONArray())

    fun isHttps(url: String) = url.startsWith("https://") && url.length <= 2048 && Catalog.safeURL(url) != null

    /** Dotted numbers, an optional leading v and an ignored +build; anything else (a pre-release tag) is null. */
    fun compare(a: String, b: String): Int? {
        fun parts(value: String) = Regex("^[vV]?(\\d{1,9}(?:\\.\\d{1,9})*)(?:\\+.*)?$").matchEntire(value)?.groupValues?.get(1)?.split('.')?.map(String::toInt)
        val left = parts(a) ?: return null
        val right = parts(b) ?: return null
        for (i in 0 until maxOf(left.size, right.size)) {
            val difference = left.getOrElse(i) { 0 } - right.getOrElse(i) { 0 }
            if (difference != 0) return difference
        }
        return 0
    }

    /**
     * The refresh job's turn, with no screen. A process the job started reads the feed once a day, or each hour while a
     * download waits for the tunnel to stop; then it downloads and installs as [prepare] does.
     */
    suspend fun unattended(app: ElarniApplication) {
        val settings = Catalog(app.repository.state.value.catalog, snapshot = true).settings
        val feed = settings.string("updateFeedURL")
        if (feed.isEmpty() || !settings.optBoolean("autoUpdate", true) || BuildConfig.ELARNI_RECORDING) return
        val waiting = withContext(Dispatchers.IO) { directory.list()?.isNotEmpty() == true }
        val last = prefs(app).getLong(CHECKED, 0)
        if (checked == null && !waiting && System.currentTimeMillis() - last in 0..86_400_000) return
        refresh(feed)
        if (checked?.third == true) prefs(app).edit().putLong(CHECKED, System.currentTimeMillis()).apply()
        prepare(app, true)
    }

    /**
     * With automatic updates on, downloads the available release and installs it when that asks for nothing and interrupts
     * nothing: no screen of the app shows, no tunnel runs, and Android lets this app update itself without a confirmation.
     */
    suspend fun prepare(app: ElarniApplication, automatic: Boolean) = work.withLock {
        val release = available.value ?: return@withLock
        if (!automatic || !installable.value) return@withLock
        if (stage.value == "none" && System.currentTimeMillis() >= retryAt && unmetered(app)) fetchPackage(release.first)
        // Android 12 is the first to let an app with the user's permission to install apps update itself unasked.
        if (stage.value == "ready" && !foreground && Build.VERSION.SDK_INT >= 31 && allowed(app) && app.repository.state.value.connection == "disconnected")
            start(app, quiet = true, reconnect = false)
    }

    /** The user's Install: downloads first if that is still to do. Android asks to confirm, and a running tunnel comes back after the update. */
    suspend fun installNow(app: ElarniApplication) = work.withLock {
        val release = available.value ?: return@withLock
        if (!installable.value) return@withLock
        if (stage.value == "none") fetchPackage(release.first)
        if (stage.value == "ready") start(app, quiet = false, reconnect = app.repository.state.value.connection in setOf("connected", "connecting"))
    }

    private suspend fun fetchPackage(version: String) {
        stage.value = "downloading"
        val result = withContext(Dispatchers.IO) { runCatching { download(assets, directory) } }
        if (available.value?.first != version) return
        val name = result.getOrNull()
        stage.value = if (name != null) "ready" else "none"
        if (name != null) staged = File(directory, name)
        // A refused release is not asked for again, and its page stays one tap away; a download the network cut is tried again in an hour.
        else if ((result.exceptionOrNull() as? AppFailure)?.code in refusals) { refused = version; installable.value = false }
        else retryAt = System.currentTimeMillis() + 3_600_000
    }

    private suspend fun start(app: ElarniApplication, quiet: Boolean, reconnect: Boolean) {
        val apk = staged ?: return
        stage.value = "installing"
        withContext(Dispatchers.IO) { runCatching {
            // Saved before the install ends this process. The time bounds the mark: an update installed some other way later is not this one.
            if (reconnect) prefs(app).edit().putLong(RECONNECT, System.currentTimeMillis()).commit()
            install(app, apk, quiet)
        } }.onFailure { failed(app) }
    }

    private fun prefs(context: Context) = context.getSharedPreferences("ui", Context.MODE_PRIVATE)

    /** Android refused the APK, and would again: that release is left to its page. */
    fun failed(context: Context) {
        prefs(context).edit().remove(RECONNECT).putString(FAILED, available.value?.first ?: "").apply()
        installable.value = false; stage.value = "failed"
    }

    /** Android asked its user, who has not answered or said no: Install stays on Home. */
    fun waiting(context: Context, asked: Boolean) { if (!asked) prefs(context).edit().remove(RECONNECT).apply(); if (stage.value == "installing") stage.value = "ready" }

    /** Whether the build that replaced this one starts the tunnel again: its user tapped Install while connected, within the last ten minutes. */
    fun reconnects(context: Context): Boolean {
        val marked = prefs(context).getLong(RECONNECT, 0)
        prefs(context).edit().remove(RECONNECT).apply()
        return System.currentTimeMillis() - marked in 0..600_000
    }

    private fun commit(context: Context, apk: File, quiet: Boolean) {
        val installer = context.packageManager.packageInstaller
        // One install at a time: an earlier one waits on a dialog its user left.
        installer.mySessions.forEach { runCatching { installer.abandonSession(it.sessionId) } }
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
        params.setAppPackageName(context.packageName); params.setSize(apk.length())
        // Android still asks when it must: on the first update, or before the user has let Elarni install apps.
        if (Build.VERSION.SDK_INT >= 31) params.setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
        val id = installer.createSession(params)
        installer.openSession(id).use { session ->
            session.openWrite("elarni.apk", 0, apk.length()).use { output -> apk.inputStream().use { it.copyTo(output) }; session.fsync(output) }
            // Android adds the outcome to the intent, so it is mutable; it names this app's receiver only.
            val answer = Intent(context, UpdateReceiver::class.java).putExtra(QUIET, quiet)
            session.commit(PendingIntent.getBroadcast(context, id, answer, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE).intentSender)
        }
    }
}

/** Android's answer to an install, and the first broadcast to the build that replaced this one. */
class UpdateReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == Intent.ACTION_MY_PACKAGE_REPLACED) {
            if (!UpdateCheck.reconnects(context)) return
            val result = goAsync(); val app = context.applicationContext as ElarniApplication
            // Android lets this broadcast start the VPN's foreground service from the background.
            app.repository.scope.launch {
                try { app.repository.requestConnect(false) } catch (error: Exception) { app.repository.report(error) } finally { result.finish() }
            }
            return
        }
        when (intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_SUCCESS -> Unit
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                val confirm = if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java)
                    else @Suppress("DEPRECATION") intent.getParcelableExtra(Intent.EXTRA_INTENT)
                // An install that was to ask for nothing does not open a dialog over whatever its user is doing.
                val asked = !intent.getBooleanExtra(UpdateCheck.QUIET, false) && confirm != null &&
                    runCatching { context.startActivity(confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }.isSuccess
                if (!asked) runCatching { context.packageManager.packageInstaller.abandonSession(intent.getIntExtra(PackageInstaller.EXTRA_SESSION_ID, -1)) }
                UpdateCheck.waiting(context, asked)
            }
            PackageInstaller.STATUS_FAILURE_ABORTED -> UpdateCheck.waiting(context, false)
            else -> UpdateCheck.failed(context)
        }
    }
}
