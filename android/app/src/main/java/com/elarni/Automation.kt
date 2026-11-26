package com.elarni

import android.os.SystemClock
import org.json.JSONArray
import org.json.JSONObject
import java.net.URI

/** Monotonic recovery episodes match the desktop policy, independent of Activity lifetime. */
class Automation(saved: JSONObject) {
    var intent = false
    var online = false
    var sleeping = false
    var attempts = saved.optInt("attempts")
    var exhausted = saved.optBoolean("exhausted") || saved.optBoolean("interrupted") || attempts > 0
    var failures = 0
    var status = "disabled"
    var path = ""
    var episodeStarted: Double? = null
    var retryAt: Double? = null
    var healthAt: Double? = null
    var lastSwitch = -1000.0
    var lastOptimization = -1000.0
    private var rearmAt: Double? = null
    // Exhausted episodes since the last passing check or user action.
    private var exhaustions = 0
    private var rotation = 0
    val cooldown = mutableMapOf<String, Double>()
    // Mobile whitelists pass Russian addresses only: set when a Russian-entry
    // server passes after two foreign-entry ones failed, cleared by a foreign pass.
    var whitelist = false
    private val foreignFailures = mutableSetOf<String>()
    fun observe(id: String, passed: Boolean, russianEntry: Boolean) {
        if (!russianEntry) { if (passed) { whitelist = false; foreignFailures.clear() } else foreignFailures.add(id) }
        else if (passed && foreignFailures.size >= 2) whitelist = true
    }
    /** Russian entries first in whitelist mode, then TCP 443 servers when the failing one is not; otherwise the user's order. */
    fun recoveryOrder(order: List<String>, current: String, tcp443: Set<String>, russianEntry: Set<String>): List<String> {
        fun rank(id: String) = (if (whitelist && id !in russianEntry) 2 else 0) + (if (current !in tcp443 && id !in tcp443) 1 else 0)
        return (0..3).flatMap { value -> order.filter { rank(it) == value } }
    }
    fun episode() = obj("attempts" to attempts, "exhausted" to exhausted, "interrupted" to (retryAt != null || attempts > 0))
    fun rearm(time: Double) {
        rearmAt = null; attempts = 0; failures = 0; exhausted = false; exhaustions = 0
        episodeStarted = null; retryAt = null; healthAt = time; lastOptimization = -1000.0
    }
    fun stop() { intent = false; rearm(0.0); healthAt = null; status = "stopped" }
    fun path(signature: String, available: Boolean, time: Double) {
        if (signature == path && available == online) return
        val initial = path.isEmpty(); path = signature; online = available
        healthAt = null; failures = 0; lastOptimization = -1000.0
        if (available && !exhausted) {
            if (!initial && attempts == 0) rearm(time) else healthAt = time
        }
    }
    fun unattended(time: Double) {
        if (!intent || !exhausted || !online || sleeping) { rearmAt = null; return }
        // TSPU freezes an SNI for about 120 s after a burst of handshakes, so
        // each unattended episode starts past that, and later ones wait longer.
        if (rearmAt == null) rearmAt = time + listOf(150, 300, 600)[minOf(exhaustions, 2)]
        if (time >= rearmAt!!) { val count = exhaustions + 1; rearm(time); exhaustions = count }
    }
    fun healthy(time: Double) {
        attempts = 0; failures = 0; exhausted = false; exhaustions = 0; episodeStarted = null; retryAt = null
        healthAt = time + healthInterval; status = "healthy"
    }
    /** Whether the health check runs at [time]. A server that [passed] its last check and has carried no [traffic]
     *  since needs none: each check wakes the radio, and after any sleep the first tick is due. */
    fun healthDue(time: Double, passed: Boolean, traffic: () -> Boolean): Boolean {
        if (time < (healthAt ?: time)) return false
        if (failures > 0 || !passed || traffic()) return true
        healthAt = time + healthInterval; return false
    }
    fun schedule(time: Double) {
        if (retryAt != null || exhausted || !online || sleeping || !intent) return
        if (episodeStarted == null) episodeStarted = time
        val delays = listOf(1, 2, 4, 8, 16, 30)
        if (attempts >= delays.size || time + delays[attempts] >= episodeStarted!! + 120) {
            exhausted = true; healthAt = null; status = "exhausted"; return
        }
        retryAt = time + delays[attempts]; healthAt = null; status = "retrying"
    }
    fun begin(time: Double): Boolean {
        if (!intent || !online || sleeping || exhausted || retryAt == null || time < retryAt!!) return false
        retryAt = null
        if (attempts >= 6 || time >= (episodeStarted ?: time) + 120) { exhausted = true; status = "exhausted"; return false }
        attempts++; return true
    }
    fun round(order: List<String>, current: String, time: Double): List<String> {
        val eligible = order.filter { (cooldown[it] ?: 0.0) <= time }
        val rest = eligible.filter { it != current }
        val offset = if (rest.isEmpty()) 0 else rotation % rest.size
        val output = (eligible.filter { it == current } + rest.drop(offset) + rest.take(offset)).take(10)
        if (rest.isNotEmpty()) rotation = (offset + output.count { it != current }) % rest.size
        return output
    }
    fun choose(policy: String, order: List<String>, current: String, scores: Map<String, Double>, healthy: Boolean, time: Double): String? {
        val eligible = order.filter { scores.containsKey(it) && (healthy || it != current) }
        if (policy == "fixed") return current.takeIf { it in eligible }
        if (policy == "orderedFallback") return if (healthy && current in eligible) current else eligible.firstOrNull()
        val best = eligible.minWithOrNull(compareBy<String> { scores[it]!! }.thenBy { it != current }) ?: return null
        if (healthy && best != current) {
            val old = scores[current] ?: return current
            if (time - lastSwitch < 60 || old - scores[best]!! < 20 || old - scores[best]!! < old * 0.2) return current
        }
        return best
    }
    companion object {
        /** Seconds between health probes; instrumentation shortens it instead of waiting out production timing. */
        @Volatile var healthInterval = 30.0
        /** Seconds between liveness checks while traffic stalls or the last one failed; instrumentation shortens it. */
        @Volatile var livenessInterval = 60.0
        val defaultTargets: JSONArray get() = JSONArray(listOf(
            obj("id" to "gstatic", "url" to "https://www.gstatic.com/generate_204", "expectedStatus" to 204, "expectedBody" to ""),
            obj("id" to "cloudflare", "url" to "https://cp.cloudflare.com/generate_204", "expectedStatus" to 204, "expectedBody" to "")))
        /** Liveness targets when failover names none; instrumentation points them at its fixture. */
        @Volatile var livenessTargets: JSONArray = defaultTargets
        fun clock() = SystemClock.elapsedRealtime() / 1000.0
        /** DPI cuts UDP transports and unusual ports first; TCP 443 looks like HTTPS. */
        fun tcp443(config: JSONObject) = runCatching {
            val outbound = config.objects("outbounds").first()
            val settings = outbound.mapObject("settings"); val stream = outbound.mapObject("streamSettings")
            val server = (settings.optJSONArray("vnext") ?: settings.optJSONArray("servers"))?.optJSONObject(0)
            outbound.string("protocol") !in listOf("hysteria", "tuic", "shadowquic", "wireguard", "amneziawg") &&
                stream.string("network") !in listOf("kcp", "mkcp", "quic", "hysteria") &&
                stream.mapObject("tlsSettings").strings("alpn") != listOf("h3") && (server ?: settings).opt("port")?.toString() == "443"
        }.getOrDefault(false)
        fun defaults() = obj("schemaVersion" to 1, "enabled" to false, "policy" to "fixed", "subscriptionID" to "", "nodeIDs" to JSONArray(), "targets" to JSONArray())
        fun validate(value: JSONObject, catalog: Catalog) {
            try {
                val ids = value.strings("nodeIDs"); val targets = value.objects("targets"); val enabled = value.getBoolean("enabled")
                require(value.getInt("schemaVersion") == 1 && value.get("enabled") is Boolean)
                require(value.getString("policy") in listOf("fixed", "lowestLatency", "orderedFallback"))
                require(value.getString("subscriptionID").length <= 128 && ids.size <= 1000 && ids.distinct().size == ids.size && ids.all { it.length in 1..128 })
                require(targets.size <= 2 && targets.map { it.getString("id") }.distinct().size == targets.size)
                val hosts = mutableSetOf<String>()
                targets.forEach {
                    val url = it.getString("url"); val uri = URI(url)
                    require(Regex("^[A-Za-z0-9._-]{1,64}$").matches(it.getString("id")))
                    require(Catalog.safeURL(url) != null && url.toByteArray().size <= 4096 && url.none { c -> c.code <= 32 || c.code == 127 })
                    require(uri.port == -1 || uri.port in 1..65535)
                    require(it.getInt("expectedStatus") in 200..299 && it.getString("expectedBody").toByteArray().size <= 4096)
                    require(hosts.add(uri.host.lowercase().trimEnd('.')))
                }
                // Backups may come from any subscription; subscriptionID names the selected server's.
                require(value.opt("russianEntryNodeIDs").let { it == null || it is JSONArray } && value.strings("russianEntryNodeIDs").all(ids::contains))
                if (enabled) {
                    require(ids.isNotEmpty() && targets.isNotEmpty() && catalog.selected in ids)
                    require(ids.all { id -> catalog.nodes.any { it.string("id") == id && it.string("format") == "xrayProfile" } })
                }
            } catch (_: Exception) { throw AppFailure("invalid_automation") }
        }
    }
}

/** Restarts of a core that stopped by itself while recovery is off: two in ten minutes at most, so one that keeps
 *  stopping cannot loop. Times are [Automation.clock] seconds. */
class CrashRestarts {
    private val times = ArrayDeque<Double>()
    /** Whether a restart may run at [time]; one that may is counted. */
    fun allow(time: Double): Boolean {
        while (times.isNotEmpty() && time - times.first() >= 600) times.removeFirst()
        return (times.size < 2).also { if (it) times.addLast(time) }
    }
}

/** A start by boot or always-on that failed because the network could not look its server up yet: both come as the device
 *  starts, often before Wi-Fi or mobile data works, and with recovery off nothing else tries again. The next try comes
 *  2, 4, 8 and 16 s after a failure and then every 30 s, at once when a network comes, until 2 minutes after the first
 *  failure. Times are [Automation.clock] seconds. */
class StartRetry {
    private var since: Double? = null
    private var attempts = 0
    /** When the next try is due, or null while none waits. */
    var retryAt: Double? = null
        private set
    /** Counts a start that failed at [time]; true when another try waits, due at [retryAt]. */
    fun failed(time: Double): Boolean {
        val first = since ?: time.also { since = it }
        val next = time + delays[minOf(attempts, delays.lastIndex)]
        if (next > first + WINDOW) { reset(); return false }
        attempts++; retryAt = next
        return true
    }
    fun due(time: Double) = retryAt.let { it != null && time >= it }
    /** A network came: the waiting try runs now, within the same 2 minutes. */
    fun networkAvailable(time: Double) { retryAt = retryAt?.let { minOf(it, time) } }
    fun reset() { since = null; attempts = 0; retryAt = null }
    companion object {
        const val WINDOW = 120.0
        private val delays = listOf(2.0, 4.0, 8.0, 16.0, 30.0)
        /** Without a network, a download for the start or the core's own start fails as well. */
        private val offlineFailures = setOf("connection_failed", "fetch_failed")
        /** Whether a start that failed with [error] may work once the network is up: the server's name had no answer
         *  (Engine marks it), a download's lookup failed or a network change cancelled it, or no network is there at all
         *  ([offline]). A refused setting, a missing VPN consent or a damaged catalog fails the same way again. */
        fun retryable(error: Exception, offline: Boolean): Boolean {
            val code = (error as? AppFailure)?.code ?: return false
            return error.cause is java.net.UnknownHostException || code == "dns_failed" || code == "fetch_cancelled" || offline && code in offlineFailures
        }
    }
}

/** Some hosting networks' paths freeze each connection after about 16 KB (11 KB on mobile), so a small probe passes while
 *  pages hang. The transfer check fetches a body of at least 128 KB over one connection and passes at 96 KB received. As on
 *  desktop, the running core is checked 5 s after it came up and again 10 s later when that one stalled, then every 15 min
 *  while its uplink grew, and at once when liveness finds no traffic. Two stalls in a row are [confirmed]; an inconclusive
 *  check neither counts as one nor ends a run of them. A check takes up to about 20 s, so it runs beside the tick, one at a
 *  time: the tick [begin]s it and reads its verdict with [takeVerdict] on a later pass, and never waits for it.
 *  Recovery restarts a server once per stall episode ([mayRestart]). Times are [Automation.clock] seconds. */
class TransferCheck {
    private var dueAt: Double? = null
    private var uplink: Long? = null
    private var now = false
    var stalls = 0
        private set
    val confirmed get() = stalls >= 2
    /** The check in flight, and whether its verdict belongs to a core that stopped since it began. */
    private var inFlight: Pending? = null
    private var stale = false
    /** The server recovery restarted for a confirmed stall; null once a check passes, another server runs or the network
     *  path changes. */
    private var restarted: String? = null
    /** A core of [server] (its id and revision) came up at [time]. */
    fun started(time: Double, server: String) {
        dueAt = time + delay; uplink = null; stalls = 0; now = false; stale = inFlight != null
        if (server != restarted) restarted = null
    }
    /** The core stopped: nothing is checked until the next one. */
    fun stopped() { dueAt = null; uplink = null; stalls = 0; now = false; stale = inFlight != null }
    /** Liveness found no traffic: the next tick checks. */
    fun noTraffic() { if (dueAt != null) now = true }
    /** Whether a check runs at [time]. A 15 min check reads [uplink] and, finding no new traffic, waits another interval.
     *  None runs while one is in flight. */
    fun due(time: Double, uplink: () -> Long): Boolean {
        val at = dueAt ?: return false
        if (inFlight != null) return false
        if (now) return true
        if (time < at) return false
        val last = this.uplink
        if (stalls != 1 && last != null) {
            val value = uplink()
            if (value <= last) { dueAt = time + interval; this.uplink = value; return false }
        }
        return true
    }
    /** Records the [verdict] of a check that ended at [time], with the uplink counter after it; true when it confirmed a stall. */
    fun record(verdict: String, time: Double, uplink: Long): Boolean {
        now = false
        if (verdict == OK) { stalls = 0; restarted = null }
        if (verdict == STALLED) stalls++
        dueAt = time + if (verdict == STALLED && stalls == 1) recheck else interval
        this.uplink = uplink
        return verdict == STALLED && stalls == 2
    }
    /** Starts a check: whatever runs it hands its verdict over through the returned [Pending]. */
    fun begin(): Pending = Pending().also { inFlight = it; stale = false }
    /** The verdict of the check in flight once it ended, for [record]: null while it runs, with none, or when the core it
     *  checked has stopped since. Taking it ends the check. */
    fun takeVerdict(): String? {
        val verdict = inFlight?.take() ?: return null
        inFlight = null
        return verdict.takeUnless { stale }
    }
    /** Whether recovery may restart [server] for its confirmed stall: once per episode. A frozen network still carries small
     *  exchanges, and a restart in a loop would cut those too; moving to another server is not held back. */
    fun mayRestart(server: String) = server != restarted
    /** Recovery restarts [server] for its confirmed stall. */
    fun restarting(server: String) { restarted = server }
    /** The network path changed: a stall seen on the last one says nothing about it. */
    fun pathChanged() { restarted = null }
    /** A check in flight; the thread that runs it calls [finish] once. */
    class Pending {
        private val verdict = java.util.concurrent.atomic.AtomicReference<String?>()
        fun finish(value: String) = verdict.set(value)
        internal fun take(): String? = verdict.getAndSet(null)
    }
    companion object {
        const val OK = "ok"
        const val STALLED = "stalled"
        const val INCONCLUSIVE = "inconclusive"
        /** Seconds from a core's start to its first check, from a first stall to the next, and between later checks;
         *  tests shorten them. */
        @Volatile var delay = 5.0
        @Volatile var recheck = 10.0
        @Volatile var interval = 900.0
        /** The core's 4 s limit without a byte unless a test shortens it. */
        @Volatile var idleMs: Int? = null
        val defaultTargets: JSONArray get() = JSONArray(listOf(
            obj("id" to "googleapis", "url" to "https://ajax.googleapis.com/ajax/libs/jquery/3.7.1/jquery.js"),
            obj("id" to "cdnjs", "url" to "https://cdnjs.cloudflare.com/ajax/libs/jquery/3.7.1/jquery.js")))
        /** Bodies the check fetches, next to [Automation.livenessTargets]; instrumentation points them at its fixture. */
        @Volatile var targets: JSONArray = defaultTargets
        /** One check over its targets' [verdicts]: one ok wins, and it stalled only when each target that answered stalled. */
        fun combine(verdicts: List<String>) = when { OK in verdicts -> OK; STALLED in verdicts -> STALLED; else -> INCONCLUSIVE }
    }
}

/** As on desktop, a server that passed a liveness check since its core started and then failed two in a row gets a new
 *  core. One that never passed (its exit may refuse the check targets) is only reported, so it cannot restart forever;
 *  the restart [reset]s the pass, so only a new one allows the next. */
class DeadCore {
    private var failures = 0
    private var passed: String? = null
    /** A start, a stop or a change of state. */
    fun reset() { failures = 0; passed = null }
    /** Records a check of [server], its id and configuration revision; true when its core should restart now. */
    fun checked(server: String, ok: Boolean): Boolean {
        if (ok) { failures = 0; passed = server; return false }
        return ++failures >= 2 && passed == server
    }
}
