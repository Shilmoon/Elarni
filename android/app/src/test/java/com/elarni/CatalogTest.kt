package com.elarni

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class CatalogTest {
    private fun read(vararg headers: Pair<String, String>): Pair<JSONObject, JSONObject> {
        val metadata = JSONObject(); val links = JSONObject()
        Catalog.readHeaders(obj(*headers), metadata, links)
        return metadata to links
    }

    @Test fun subscriptionUserinfoKeepsOnlyUnambiguousCounters() {
        val (metadata, _) = read("subscription-userinfo" to "upload=1; download = 2;total=30000000000; expire=1700000000; plan=9")
        assertEquals(1L, metadata.getLong("upload"))
        assertEquals(2L, metadata.getLong("download"))
        assertEquals("Counters beyond 32 bits", 30000000000L, metadata.getLong("total"))
        assertEquals(1700000000L, metadata.getLong("expire"))
        assertFalse(metadata.has("plan"))
        val (odd, _) = read("subscription-userinfo" to "upload=5; upload=6; download=-1; total=many; expire=1=2")
        assertFalse("A repeated field is ambiguous", odd.has("upload"))
        assertFalse(odd.has("download"))
        assertFalse(odd.has("total"))
        assertFalse(odd.has("expire"))
        assertEquals(0, read().first.length())
    }

    @Test fun providerTextDropsControlCharactersAndBlanks() {
        val (metadata, _) = read("announce" to "  Maintenance\u0007 tonight\n ", "banner-text" to " \t ", "banner-button-text" to "Renew")
        assertEquals("Maintenance tonight", metadata.getString("announcement"))
        assertFalse(metadata.has("bannerText"))
        assertEquals("Renew", metadata.getString("bannerButtonText"))
        assertNull(Catalog.display("x".repeat(16385)))
        assertEquals("Plain title", Catalog.display(" Plain title "))
    }

    @Test fun providerLinksMustBePlainWebAddresses() {
        val (_, links) = read("support-url" to "https://support.example/help", "profile-web-page-url" to "javascript:alert(1)", "banner-button-url" to "https://a.example/old")
        assertEquals("https://support.example/help", links.getString("support"))
        assertFalse(links.has("account"))
        assertEquals("https://a.example/old", links.getString("banner"))
        assertEquals("https://b.example/premium", read("banner-button-url" to "https://a.example/old", "premium-url" to "https://b.example/premium").second.getString("banner"))
        for (unsafe in listOf("intent://scan/#Intent;scheme=zxing;end", "tg://resolve?domain=x", "file:///sdcard/a", "https://user:pass@example.com/", "https://example.com/#top", "https:///path", ""))
            assertFalse(unsafe, read("support-url" to unsafe).second.has("support"))
    }

    @Test fun flagsAndUpdateIntervalAcceptOnlyClearValues() {
        val (metadata, _) = read("routing-enable" to "0", "hide-settings" to "TRUE", "x-hwid-active" to "maybe", "profile-update-interval" to " 12 ")
        assertEquals(false, metadata.get("routingEnabled"))
        assertEquals(true, metadata.get("hideSettings"))
        assertFalse(metadata.has("deviceRegistered"))
        assertEquals(12, metadata.getInt("updateIntervalHours"))
        assertEquals("A week at most", 168, read("profile-update-interval" to "500").first.getInt("updateIntervalHours"))
        for (ignored in listOf("0", "1.5", "-3", "1234567", "twelve")) assertFalse(ignored, read("profile-update-interval" to ignored).first.has("updateIntervalHours"))
    }

    @Test fun safeURLAdmitsOnlyHttpAndHttpsWithAHost() {
        assertEquals("https://example.com/a?b=c", Catalog.safeURL("https://example.com/a?b=c"))
        assertEquals("http://127.0.0.1:8080/", Catalog.safeURL("http://127.0.0.1:8080/"))
        for (unsafe in listOf("", "ftp://example.com/", "javascript:alert(1)", "data:text/html,x", "https://", "https://u@example.com/",
            "https://example.com/#f", "https://example.com/\u0000", "https://example.com/ a", "https://example.com/" + "a".repeat(16384)))
            assertNull(unsafe.take(40), Catalog.safeURL(unsafe))
    }

    @Test fun elarniRoutingLinksWinOverOtherApps() {
        assertEquals(listOf("elarni://routing/add/b"), Catalog.addressedRouting(listOf("happ://routing/add/a", "elarni://routing/add/b")))
        assertEquals(listOf("happ://routing/add/a"), Catalog.addressedRouting(listOf("happ://routing/add/a")))
        assertTrue(Catalog.isRoutingLink("incy://routing/off"))
        assertFalse(Catalog.isRoutingLink("https://routing/"))
    }

    @Test fun onlyAPublishedCopyKeepsWhatItRead() {
        fun data() = obj("subscriptions" to JSONArray(listOf(obj("id" to "s", "nodes" to JSONArray(listOf(obj("id" to "a")))))))
        val working = Catalog(data()); val published = Catalog(data(), snapshot = true)
        working.putSource(obj("id" to "t", "nodes" to JSONArray(listOf(obj("id" to "b")))))
        assertEquals("An editor's copy shows each edit", listOf("a", "b"), working.nodes.map { it.getString("id") })
        assertEquals("b", working.node("b").getString("id"))
        assertSame("A published copy reads its servers once", published.nodes, published.nodes)
        assertEquals("a", published.node("a").getString("id"))
        assertNull(published.nodeOrNull("b"))
    }

    // The app says the server changed only when a refresh replaced the one that is gone.
    @Test fun aReplacedSelectionIsOneThatIsGone() {
        fun catalog(selected: String?, vararg ids: String) = Catalog(obj("selectedNodeId" to (selected ?: JSONObject.NULL),
            "subscriptions" to JSONArray(listOf(obj("id" to "s", "nodes" to JSONArray(ids.map { obj("id" to it) }))))))
        assertTrue(catalog("b", "b", "c").replacedSelection("a"))
        assertFalse("The server is still there", catalog("b", "a", "b").replacedSelection("a"))
        assertFalse("The selection did not move", catalog("a", "a", "b").replacedSelection("a"))
        assertFalse("No server took over", catalog(null, "b").replacedSelection("a"))
        assertFalse("Nothing was selected before", catalog("b", "b").replacedSelection(""))
    }

    @Test fun probeKeyNamesTheHandshakeSNI() {
        fun node(security: String, serverName: String? = null, address: String = "1.2.3.4") = obj("config" to obj("outbounds" to JSONArray(listOf(obj(
            "protocol" to "vless", "settings" to obj("vnext" to JSONArray(listOf(obj("address" to address, "port" to 443)))),
            "streamSettings" to obj("security" to security, "realitySettings" to serverName?.let { obj("serverName" to it) }))))))
        assertEquals("www.example.com", Catalog.probeKey(node("reality", "WWW.Example.com")))
        assertEquals("TLS without a name sends the address", "edge.example", Catalog.probeKey(node("tls", address = "Edge.Example")))
        assertNull("A plain server sends no SNI", Catalog.probeKey(node("none")))
        assertEquals("ShadowQUIC keeps its name in its own settings", "cover.example", Catalog.probeKey(obj("config" to obj("outbounds" to JSONArray(listOf(obj(
            "protocol" to "shadowquic", "settings" to obj("address" to "1.2.3.4", "port" to 443, "serverName" to "Cover.Example"))))))))
    }

    @Test fun perAppModesKeepAppsThatNeedNoTunnelOutOfIt() {
        val uids = mapOf("com.a" to 10001, "com.b" to 10002, "com.c" to 10003, "com.shared.one" to 10009, "com.shared.two" to 10009, "com.elarni" to 10000)
        fun apps(mode: String, listed: List<String>, targets: JSONObject = JSONObject()) =
            Catalog.tunnelApps(obj("perAppMode" to mode, "perAppProcesses" to JSONArray(listed), "perAppTargets" to targets), "com.elarni") { uids[it] }
        assertNull("Off: every app enters", apps("off", listOf("com.a")))
        assertEquals("Only listed: just the listed apps enter, whatever their target", true to listOf("com.a", "com.b"),
            apps("proxyOnly", listOf("com.a", "com.gone", "com.b", "com.elarni"), obj("com.b" to "direct")))
        assertNull("With no listed app installed, an allowed list would match no app", apps("proxyOnly", listOf("com.gone", "com.elarni")))
        assertEquals("Bypass: only apps going direct stay out", false to listOf("com.a", "com.b"),
            apps("bypass", listOf("com.a", "com.b", "com.c", "com.gone", "com.elarni"), obj("com.b" to "direct", "com.c" to "routing")))
        for (target in listOf("routing", "block", "node:server", "proxy:socks5://127.0.0.1:1080"))
            assertEquals("A $target target keeps its app in the tunnel", false to emptyList<String>(), apps("bypass", listOf("com.a"), obj("com.a" to target)))
        assertEquals("An app sharing a UID follows the target of an app listed with one", false to emptyList<String>(),
            apps("bypass", listOf("com.shared.one", "com.shared.two"), obj("com.shared.two" to "routing")))
        assertEquals("The first listed target wins for a shared UID", false to listOf("com.shared.one", "com.shared.two"),
            apps("bypass", listOf("com.shared.one", "com.shared.two"), obj("com.shared.one" to "direct", "com.shared.two" to "routing")))
    }

    // Only listed apps with none of them installed took every app into the tunnel, all going direct, and showed Connected.
    @Test fun onlyListedAppsWithNoneInstalledIsRefused() {
        val uids = mapOf("com.a" to 10001, "com.elarni" to 10000)
        fun refused(mode: String, listed: List<String>) =
            Catalog.noListedAppInstalled(obj("perAppMode" to mode, "perAppProcesses" to JSONArray(listed)), "com.elarni") { uids[it] }
        assertTrue("No listed app installed", refused("proxyOnly", listOf("com.gone", "com.also.gone")))
        assertTrue("This app never enters its own tunnel", refused("proxyOnly", listOf("com.gone", "com.elarni")))
        assertFalse("One listed app installed", refused("proxyOnly", listOf("com.gone", "com.a")))
        assertFalse("An empty list keeps every app in the tunnel", refused("proxyOnly", emptyList()))
        assertFalse("Bypass with no listed app installed keeps every app in the tunnel", refused("bypass", listOf("com.gone")))
        assertFalse("Off", refused("off", listOf("com.gone")))
    }

    @Test fun listedAppUidsChangeOnlyWhenAListedAppIsInstalledRemovedOrReinstalled() {
        val settings = obj("perAppMode" to "bypass", "perAppProcesses" to JSONArray(listOf("com.a", "com.later")))
        val started = mutableMapOf("com.a" to 10001, "com.other" to 10002)
        val taken = Catalog.listedUids(settings) { started[it] }
        assertEquals("An app not installed yet has no UID", mapOf("com.a" to 10001, "com.later" to null), taken)
        assertNull("Without per-app routing no UID matters", Catalog.listedUids(JSONObject(settings.toString()).put("perAppMode", "off")) { started[it] })
        fun now(vararg uids: Pair<String, Int>) = Catalog.listedUids(settings) { (started + uids)[it] }
        assertEquals("An update keeps the UID", taken, now("com.a" to 10001))
        assertEquals("An app nobody listed changes nothing", taken, now("com.new" to 10003))
        assertNotEquals("A listed app installed meanwhile", taken, now("com.later" to 10004))
        assertNotEquals("A listed app reinstalled with another UID", taken, now("com.a" to 10005))
        started.remove("com.a")
        assertNotEquals("A listed app removed, whose UID another app may get", taken, Catalog.listedUids(settings) { started[it] })
    }

    @Test fun refreshWrittenUnderAClockThatWasAheadIsDue() {
        val time = 1_800_000_000.0; val interval = 4 * 3600.0; val longest = interval * 1.1
        assertTrue("A time that has passed", Catalog.refreshDue(time - 1, time, longest))
        assertTrue(Catalog.refreshDue(time, time, longest))
        assertFalse("The furthest time the schedule writes", Catalog.refreshDue(time + longest, time, longest))
        assertFalse("A clock corrected by minutes", Catalog.refreshDue(time + longest + 3600, time, longest))
        assertTrue("Written under a clock a year ahead", Catalog.refreshDue(time + 365 * 86400, time, longest))
    }

    @Test fun lastSuccessfulServerIsTheLatestCurrentSuccess() {
        fun node(id: String, at: Double?, revision: String = "r-$id", success: String? = revision, format: String = "xrayProfile") = obj("id" to id, "format" to format,
            "configurationRevision" to revision, "lastSuccessAt" to at, "lastSuccessRevision" to success)
        val older = node("older", 100.0); val latest = node("latest", 300.0)
        assertEquals("The latest success wins", "latest", Catalog.lastSuccessful(listOf(older, latest, node("never", null)), "")?.getString("id"))
        assertNull("The selected server is not offered, nor another in its place", Catalog.lastSuccessful(listOf(older, latest), "latest"))
        assertEquals("A Telegram proxy is not a server to connect to", "older", Catalog.lastSuccessful(listOf(older, node("telegram", 900.0, format = "telegram")), "")?.getString("id"))
        assertEquals("A success with an older configuration", "older", Catalog.lastSuccessful(listOf(older, node("edited", 900.0, revision = "new", success = "old")), "")?.getString("id"))
        assertEquals("A server without a revision of its own goes by its id", "plain",
            Catalog.lastSuccessful(listOf(obj("id" to "plain", "format" to "xrayProfile", "lastSuccessAt" to 50.0, "lastSuccessRevision" to "plain")), "")?.getString("id"))
        assertNull("Nothing worked yet", Catalog.lastSuccessful(listOf(node("never", null), node("unrecorded", 400.0, success = null)), ""))
    }

    @Test fun closedServerGroupsToggleAndForgetRemovedSubscriptions() {
        assertEquals(setOf("s:NL"), Catalog.toggledGroup("s:NL", emptySet(), listOf("s")))
        assertEquals(setOf("s", "s:NL"), Catalog.toggledGroup("s", setOf("s:NL"), listOf("s")))
        assertEquals("Opening removes the group; a removed subscription's groups go with it, the list by country's stay",
            setOf(":DE"), Catalog.toggledGroup("s:NL", setOf("s:NL", ":DE", "gone", "gone:NL"), listOf("s")))
    }

    // Home's update line. Failure modes: one provider that stays unreachable pins the line at its old fetch although the
    // rest just updated; a subscription whose update failed goes uncounted; a subscription never fetched reads as updated.
    @Test fun homeFreshnessCountsTheSubscriptionsThatUpdated() {
        fun source(status: String, fetched: Double?) = obj("url" to "https://provider.example/s", "status" to status).apply { if (fetched != null) put("lastSuccessfulFetch", fetched) }
        assertEquals("The oldest updated list decides", 100.0 to 0, Catalog.freshness(listOf(source("ready", 500.0), source("ready", 100.0))))
        assertEquals("A failed update does not hold the time back; it is counted", 500.0 to 1, Catalog.freshness(listOf(source("ready", 500.0), source("failed", 100.0))))
        assertEquals("Nor does one that stopped at a review", 500.0 to 1, Catalog.freshness(listOf(source("ready", 500.0), source("needsApproval", 100.0))))
        assertEquals("A first download that failed is counted as well", 500.0 to 1, Catalog.freshness(listOf(source("ready", 500.0), source("failed", null))))
        assertEquals("With no list updated, the oldest of them all decides", 100.0 to 2, Catalog.freshness(listOf(source("failed", 500.0), source("failed", 100.0))))
        assertEquals("A subscription still waiting for its first download is not updated yet", null to 0, Catalog.freshness(listOf(source("ready", 500.0), source("pending", null))))
        assertEquals(null to 1, Catalog.freshness(listOf(source("failed", null))))
        assertEquals(null to 0, Catalog.freshness(emptyList()))
    }

    // A provider that wants this device registered, or has too many, is asked again at the regular interval: the
    // user may free a slot on its site or reset the device ID. Ordinary failures keep their short retries.
    @Test fun deviceReplyIsAskedAgainOneIntervalLater() {
        val catalog = Catalog(obj("subscriptions" to JSONArray()))
        for (code in listOf("hwid_required", "hwid_limit")) {
            catalog.putSource(obj("id" to "s", "url" to "https://provider.example/feed", "status" to "ready",
                "fetchOptions" to obj("refreshHours" to "6"), "nextRefreshAt" to 1.0))
            for (attempt in 1..3) {
                val before = now()
                catalog.refreshFailed("s", code)
                val after = now()
                val source = catalog.source("s")
                assertEquals(code, source.getString("refreshError"))
                assertEquals(attempt, source.getInt("failures"))
                val at = source.getDouble("nextRefreshAt")
                assertTrue("$code, attempt $attempt", at >= before + 6 * 3600 && at <= after + 6 * 3600)
                assertEquals(at, catalog.nextRefresh(source)!!, 0.0)
            }
        }
        val before = now()
        catalog.refreshFailed("s", "fetch_failed")
        assertTrue("Other failures keep the short retries", catalog.source("s").getDouble("nextRefreshAt") in before + 1800..now() + 1800)
    }

    @Test fun deviceReplySavedWithNoTimeByAnOlderVersionIsDue() {
        val catalog = Catalog(JSONObject())
        fun parked(code: String) = obj("id" to "s", "url" to "https://provider.example/feed", "status" to "failed", "refreshError" to code,
            "failures" to 1, "fetchOptions" to obj("refreshHours" to "6"), "nextRefreshAt" to JSONObject.NULL)
        for (code in listOf("hwid_required", "hwid_limit")) {
            val time = now()
            assertTrue("$code, never refreshed", catalog.nextRefresh(parked(code))!! <= now())
            assertTrue(catalog.nextRefresh(parked(code))!! >= time)
            assertEquals("An interval after the last success", time - 3600 + 6 * 3600,
                catalog.nextRefresh(parked(code).put("lastSuccessfulFetch", time - 3600))!!, 0.0)
        }
        assertNull("Only the device replies were saved with no time", catalog.nextRefresh(parked("fetch_failed")))
    }

    @Test fun retryDelayAndCanonicalForm() {
        assertEquals(listOf(60, 60, 300, 1800, 1800), listOf(0, 1, 2, 3, 9).map(Catalog::retryDelay))
        assertEquals(Catalog.canonical(JSONObject("""{"b":[1,{"y":2,"x":"s"}],"a":null}""")), Catalog.canonical(JSONObject("""{"a":null,"b":[1,{"x":"s","y":2}]}""")))
        assertNotEquals(Catalog.canonical(JSONArray("[1,2]")), Catalog.canonical(JSONArray("[2,1]")))
    }
}
