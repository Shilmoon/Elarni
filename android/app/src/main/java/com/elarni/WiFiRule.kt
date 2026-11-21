package com.elarni

import android.Manifest
import android.app.AlarmManager
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.location.LocationManager
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.net.wifi.WifiInfo
import android.net.wifi.WifiManager
import android.os.Build
import android.os.SystemClock
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Job
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withTimeoutOrNull

/**
 * Connect on untrusted Wi-Fi: joining a Wi-Fi network that is not on the trusted list, or whose name Android hides, connects,
 * once per join.
 * Android reports every Wi-Fi change at once only to a callback in a running process. While the VPN service runs, this
 * process has one; otherwise WiFiWatchService has it, in a small process of its own that starts this one when Wi-Fi
 * changes. Should Android stop that service all the same, a PendingIntent starts this process.
 * Android sends a network callback's PendingIntent once, for the first matching network, then drops the request,
 * so Elarni asks for one change at a time: the next Wi-Fi join while off Wi-Fi, leaving Wi-Fi while on it. It reports
 * leaving as mobile data turning foreground: with mobile data off, or from one Wi-Fi network straight to another,
 * a killed process hears nothing until the 15-minute check.
 */
object WiFiRule {
    private const val JOIN = "com.elarni.wifi.JOIN"
    private const val LEAVE = "com.elarni.wifi.LEAVE"
    const val CHECK = "com.elarni.wifi.CHECK"
    const val CHANGE = "com.elarni.wifi.CHANGE"
    private fun request(transport: Int, vararg capabilities: Int) = NetworkRequest.Builder().addTransportType(transport)
        .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET).addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
        .apply { capabilities.forEach { addCapability(it) } }.build()
    val wifi get() = request(NetworkCapabilities.TRANSPORT_WIFI)
    // Mobile data runs in the background while Wi-Fi is up and turns foreground when Wi-Fi goes.
    private val leave get() = request(NetworkCapabilities.TRANSPORT_CELLULAR, NetworkCapabilities.NET_CAPABILITY_FOREGROUND)
    // Android fills in the network, so these stay mutable; each names its receiver.
    private fun wake(context: Context, action: String) = PendingIntent.getBroadcast(context, 7, Intent(context, WiFiWatch::class.java).setAction(action),
        PendingIntent.FLAG_MUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    private val lock = Mutex()
    private var retry: Job? = null
    private var live: ConnectivityManager.NetworkCallback? = null
    private var held: Boolean? = null
    /** Who reports a change at once: this process's callback, and the watch service. Tests leave one out, or both to
     *  reach the reports a killed process gets. */
    @Volatile internal var reports = setOf("process", "service")
    @Volatile private var synced: Boolean? = null
    /** The change Android reports next: "join", "leave", or null while neither can be asked for. */
    @Volatile var watching: String? = null
        private set

    /** Android shows Wi-Fi names only to an app allowed to use location all the time, with location on. */
    fun namesAllowed(context: Context) = listOf(Manifest.permission.ACCESS_FINE_LOCATION, Manifest.permission.ACCESS_BACKGROUND_LOCATION)
        .all { context.checkSelfPermission(it) == PackageManager.PERMISSION_GRANTED } &&
        context.getSystemService(LocationManager::class.java).isLocationEnabled

    /** Each process starts watching once; turning the rule on counts the current network as a new join. */
    fun sync(app: ElarniApplication, catalog: Catalog) {
        val on = catalog.settings.optBoolean("connectOnUntrustedWiFi")
        if (synced == on) return
        if (synced == false && on) app.getSharedPreferences("wifi-rule", Context.MODE_PRIVATE).edit().remove("join").apply()
        synced = on
        app.repository.scope.launch { arm(app) }
    }

    /** Handles the Wi-Fi network joined now, if any, and asks Android for the next change. */
    suspend fun arm(app: ElarniApplication, attempt: Int = 0, check: Boolean = false): Unit = lock.withLock {
        app.repository.awaitReady()
        // A retry runs this itself; any other call takes its place.
        val self = currentCoroutineContext()[Job]
        retry?.takeIf { it != self }?.cancel()
        val connectivity = app.getSystemService(ConnectivityManager::class.java)
        val alarms = app.getSystemService(AlarmManager::class.java)
        fun drop(action: String) = runCatching { connectivity.unregisterNetworkCallback(wake(app, action)) }
        fun ask(action: String, request: NetworkRequest) = runCatching { connectivity.registerNetworkCallback(request, wake(app, action)) }.isSuccess
        // A broadcast may start this process and run this before the setting is published.
        val on = Catalog(app.repository.state.value.catalog).settings.optBoolean("connectOnUntrustedWiFi")
        synced = on
        if (!on || "process" !in reports) { live?.let { runCatching { connectivity.unregisterNetworkCallback(it) } }; live = null }
        else if (live == null) live = object : ConnectivityManager.NetworkCallback() {
            // Registering reports the network joined now, which the code below handles anyway; that second look is harmless.
            override fun onAvailable(network: Network) { app.repository.scope.launch { arm(app) } }
            override fun onLost(network: Network) { app.repository.scope.launch { arm(app) } }
        }.takeIf { runCatching { connectivity.registerNetworkCallback(wifi, it) }.isSuccess }
        hold(app, check)
        if (!on) {
            drop(JOIN); drop(LEAVE); alarms.cancel(wake(app, CHECK)); watching = null
            return@withLock
        }
        // A backstop for the changes Android cannot report: one Wi-Fi network straight to another, or a request it refused.
        alarms.setInexactRepeating(AlarmManager.ELAPSED_REALTIME, SystemClock.elapsedRealtime() + AlarmManager.INTERVAL_FIFTEEN_MINUTES,
            AlarmManager.INTERVAL_FIFTEEN_MINUTES, wake(app, CHECK))
        @Suppress("DEPRECATION") val networks = connectivity.allNetworks.mapNotNull { network -> connectivity.getNetworkCapabilities(network)?.let { network to it } }
        val joined = networks.firstOrNull { wifi.canBeSatisfiedBy(it.second) }?.first
        if (joined == null) {
            drop(LEAVE)
            watching = if (ask(JOIN, wifi)) "join" else null
            return@withLock
        }
        drop(JOIN)
        joined(app, joined)
        if (networks.none { leave.canBeSatisfiedBy(it.second) }) watching = if (ask(LEAVE, leave)) "leave" else null
        else {
            // Mobile data stays foreground for about 30 s after Wi-Fi comes back; asking now would report at once.
            drop(LEAVE); watching = null
            if (attempt < 24) retry = app.repository.scope.launch { delay(5000); arm(app, attempt + 1) }
        }
    }

    /** Whether a join connects: a name Android hides ([name] null) counts as untrusted, and an empty list trusts nothing. */
    internal fun connects(name: String?, trusted: List<String>) = name == null || name !in trusted

    /** Starts or stops the watch service. The VPN service keeps this process and its callback while it runs. The
     *  rule needs no names to act (a hidden name counts as untrusted), so the service runs without them too. The
     *  15-minute check asks again for a service that should run: Android may have stopped it without a word to
     *  this process. */
    @Synchronized fun hold(app: ElarniApplication, again: Boolean = false) {
        // Not read yet: arm() calls again once it is.
        val on = synced ?: return
        val wanted = on && "service" in reports && !app.vpnServiceRuns
        if (wanted == held && !(wanted && again)) return
        val service = Intent(app, WiFiWatchService::class.java)
        // Android may refuse a start from the background; the PendingIntent reports and the 15-minute check remain.
        if (wanted) held = true.takeIf { runCatching { app.startForegroundService(service) }.isSuccess }
        else {
            // Stopping a service whose start is still on its way crashes its process, so a start this process asked
            // for is undone by the service itself.
            if (held != true || runCatching { app.startForegroundService(service.setAction(WiFiWatchService.STOP)) }.isFailure) app.stopService(service)
            held = false
        }
    }

    private suspend fun joined(app: ElarniApplication, network: Network) {
        val trusted = Catalog(app.repository.state.value.catalog).settings.strings("trustedWiFiNetworks")
        // Network handles repeat after a restart; the boot count tells the joins apart.
        val join = "${android.provider.Settings.Global.getInt(app.contentResolver, android.provider.Settings.Global.BOOT_COUNT, 0)}:${network.networkHandle}"
        val seen = app.getSharedPreferences("wifi-rule", Context.MODE_PRIVATE)
        if (seen.getString("join", null) == join) return
        // With no trusted network every Wi-Fi connects, so the name is not read. Without location access Android hides it,
        // and Settings shows what to allow; until then each join connects.
        val name = if (trusted.isEmpty()) null else when (val read = name(app, network)) {
            // Gone while reading: the next network is a join of its own.
            Ssid.Lost -> return
            Ssid.Unknown -> null
            is Ssid.Named -> read.name
        }
        seen.edit().putString("join", join).apply()
        if (connects(name, trusted)) app.control(ACTION_CONNECT)
    }

    /** A Wi-Fi name as Android gives it: the name, the network gone while reading, or a name it keeps hidden. */
    private sealed interface Ssid {
        data class Named(val name: String) : Ssid
        data object Lost : Ssid
        data object Unknown : Ssid
    }

    private suspend fun name(app: ElarniApplication, network: Network): Ssid {
        val connectivity = app.getSystemService(ConnectivityManager::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            // Only a callback that asks for location information sees the name; other reads get it redacted.
            val result = CompletableDeferred<Ssid>()
            val callback = object : ConnectivityManager.NetworkCallback(FLAG_INCLUDE_LOCATION_INFO) {
                override fun onCapabilitiesChanged(changed: Network, capabilities: NetworkCapabilities) {
                    if (changed == network) result.complete(named((capabilities.transportInfo as? WifiInfo)?.ssid))
                }
                override fun onLost(lost: Network) { if (lost == network) result.complete(Ssid.Lost) }
            }
            // Android caps an app's callbacks; without one the name stays unknown.
            if (runCatching { connectivity.registerNetworkCallback(wifi, callback) }.isFailure) return Ssid.Unknown
            return try { withTimeoutOrNull(5000) { result.await() } ?: Ssid.Unknown } finally { connectivity.unregisterNetworkCallback(callback) }
        }
        if (connectivity.getNetworkCapabilities(network)?.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) != true) return Ssid.Lost
        @Suppress("DEPRECATION") val info = app.getSystemService(WifiManager::class.java).connectionInfo
        return named(info?.ssid)
    }

    // UTF-8 names come quoted; other bytes come as hex, which the trusted list can hold too.
    private fun clean(ssid: String?) = ssid?.takeIf { it != WifiManager.UNKNOWN_SSID }?.removeSurrounding("\"")?.takeIf { it.isNotEmpty() }
    private fun named(ssid: String?) = clean(ssid)?.let { Ssid.Named(it) } ?: Ssid.Unknown

    /** Tells [changed] the joined Wi-Fi network's name, or null without one or without leave to read it, at once and on
     *  every change, until the returned function is called. */
    fun watchName(context: Context, changed: (String?) -> Unit): () -> Unit {
        val connectivity = context.getSystemService(ConnectivityManager::class.java)
        var shown: Network? = null
        fun read(network: Network, capabilities: NetworkCapabilities) {
            shown = network
            changed(clean(if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) (capabilities.transportInfo as? WifiInfo)?.ssid
                else @Suppress("DEPRECATION") context.getSystemService(WifiManager::class.java).connectionInfo?.ssid))
        }
        fun lost(network: Network) { if (network == shown) { shown = null; changed(null) } }
        // Only a callback that asks for location information sees the name; before Android 12 there is none, and none is needed.
        val callback = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) object : ConnectivityManager.NetworkCallback(FLAG_INCLUDE_LOCATION_INFO) {
            override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) = read(network, capabilities)
            override fun onLost(network: Network) = lost(network)
        } else object : ConnectivityManager.NetworkCallback() {
            override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) = read(network, capabilities)
            override fun onLost(network: Network) = lost(network)
        }
        if (runCatching { connectivity.registerNetworkCallback(wifi, callback) }.isFailure) return {}
        return { runCatching { connectivity.unregisterNetworkCallback(callback) } }
    }
}

/** Runs while Connect on untrusted Wi-Fi is on and the VPN service is not, in a process of its own that loads nothing
 *  else of the app: Android reports every Wi-Fi change to it at once, and it passes each on to WiFiWatch, which starts
 *  the app's process if need be. Android shows its notification for as long as it runs. */
class WiFiWatchService : Service() {
    private val changes = object : ConnectivityManager.NetworkCallback() {
        // Registering reports the network joined now: a look the app's process takes anyway.
        override fun onAvailable(network: Network) = report()
        override fun onLost(network: Network) = report()
    }
    private var watching = false
    private fun report() = sendBroadcast(Intent(this, WiFiWatch::class.java).setAction(WiFiRule.CHANGE))
    override fun onBind(intent: Intent?) = null
    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val manager = getSystemService(NotificationManager::class.java)
        // Its own channel, so that hiding it keeps the connection's notification.
        manager.createNotificationChannel(NotificationChannel(CHANNEL, getString(R.string.connect_on_untrusted_wi_fi), NotificationManager.IMPORTANCE_LOW))
        val notification = Notification.Builder(this, CHANNEL).setSmallIcon(R.drawable.ic_elarni).setContentTitle("Elarni")
            .setContentText(getString(R.string.watching_for_untrusted_wi_fi)).setOngoing(true).setOnlyAlertOnce(true).setCategory(Notification.CATEGORY_SERVICE)
            .setContentIntent(PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)).build()
        // Every start asked for the foreground, so every start takes it, the one that stops too. Android restarts this
        // service after it killed the process, and may refuse it the foreground then.
        try { if (Build.VERSION.SDK_INT >= 34) startForeground(5, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE) else startForeground(5, notification) }
        catch (_: Exception) { stopSelf(startId); return START_NOT_STICKY }
        // Only the latest start may stop the service: stopping it under a later start would crash the process too.
        if (intent?.action == STOP) { stopForeground(STOP_FOREGROUND_REMOVE); stopSelf(startId); return START_NOT_STICKY }
        if (!watching) watching = runCatching { getSystemService(ConnectivityManager::class.java).registerNetworkCallback(WiFiRule.wifi, changes) }.isSuccess
        return START_STICKY
    }
    override fun onDestroy() {
        if (watching) runCatching { getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(changes) }
        super.onDestroy()
    }
    companion object { const val STOP = "com.elarni.wifi.STOP"; const val CHANNEL = "wifi-watch" }
}

/** Android sends this for a watched network change, at the backup check, and after an app update; the watch service
 *  sends it for every Wi-Fi change. */
class WiFiWatch : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val result = goAsync(); val app = context.applicationContext as ElarniApplication
        app.repository.scope.launch { try { WiFiRule.arm(app, check = intent.action == WiFiRule.CHECK) } finally { result.finish() } }
    }
}
