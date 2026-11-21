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

    private fun backupNode(id: String, address: String, hideURL: Boolean = false) = obj("id" to id, "name" to id, "format" to "xrayProfile",
        "config" to obj("outbounds" to JSONArray(listOf(obj("protocol" to "vless", "settings" to obj("address" to address, "port" to 443)))))).apply { if (hideURL) put("hideURL", true) }
    private fun backupSource(id: String, name: String, nodes: List<JSONObject>, url: String? = null, hideURL: Boolean? = null) =
        obj("id" to id, "name" to name, "nodes" to JSONArray(nodes), "metadata" to JSONObject().apply { if (hideURL != null) put("hideURL", hideURL) })
            .apply { if (url != null) put("url", url) }
    private fun backupRoute(id: String, provider: String? = null) = obj("id" to id, "profile" to obj("Name" to id), "original" to obj("Name" to id), "enabled" to true, "edited" to false)
        .apply { if (provider != null) put("providerSubscriptionId", provider) }
    private fun backupCatalog(selected: String?, sources: List<JSONObject>, routes: List<JSONObject> = emptyList(), assignments: JSONObject = JSONObject()) =
        Catalog(obj("subscriptions" to JSONArray(sources), "profiles" to JSONArray(routes), "assignments" to assignments).apply { if (selected != null) put("selectedNodeId", selected) })

    // One subscription whose provider forbids backups, or that was never updated, used to refuse the backup of everything else.
    @Test fun aBackupLeavesOutAndNamesWhatItMayNotHold() {
        val catalog = backupCatalog("forbidden-1", listOf(
            backupSource("local", "My servers", listOf(backupNode("local-1", "local.example")), hideURL = false),
            backupSource("provider", "Allowed provider", listOf(backupNode("provider-1", "allowed.example")), "https://allowed.example/sub", false),
            backupSource("forbidden", "No backups", listOf(backupNode("forbidden-1", "forbidden.example")), "https://forbidden.example/sub", true),
            // A hidden import keeps its own local group, flagged per server.
            backupSource("hidden", "Hidden import", listOf(backupNode("hidden-1", "hidden.example", hideURL = true))),
            // Never updated: whether its provider allows backups is unknown.
            backupSource("fresh", "Never updated", listOf(backupNode("fresh-1", "fresh.example")), "https://fresh.example/sub"),
        ), listOf(backupRoute("route-local"), backupRoute("route-provider", "provider"), backupRoute("route-forbidden", "forbidden"), backupRoute("route-fresh", "fresh")),
            obj("local" to "route-forbidden", "provider" to "route-provider", "forbidden" to "route-forbidden", "fresh" to "route-local"))
        val (document, excluded) = catalog.backup()
        assertEquals(listOf("No backups" to "sharing_disabled", "Hidden import" to "sharing_disabled", "Never updated" to "sharing_refresh_required"),
            excluded.map { it.getString("name") to it.getString("reason") })
        assertEquals(listOf("local", "provider"), document.objects("subscriptions").map { it.getString("id") })
        // Nothing of a left-out subscription is in the file: no server, no link, no routing profile of its provider.
        val text = document.toString()
        for (private in listOf("forbidden.example", "hidden.example", "fresh.example", "route-forbidden", "route-fresh")) assertFalse(private, text.contains(private))
        assertTrue("The selection was a left-out server", document.string("selectedNodeId").isEmpty())
        assertEquals(listOf("route-local", "route-provider"), document.objects("routes").map { it.getString("id") })
        assertEquals("provider", document.objects("routes")[1].getString("providerSubscriptionId"))
        assertEquals(mapOf("provider" to "route-provider"), document.mapObject("assignments").let { a -> a.keys().asSequence().associateWith { a.getString(it) } })
        assertEquals("The catalog itself is unchanged", 5, catalog.sources.size)
        assertEquals("forbidden-1", catalog.selected)
        val kept = backupCatalog("local-1", listOf(backupSource("local", "My servers", listOf(backupNode("local-1", "local.example"))),
            backupSource("forbidden", "No backups", listOf(backupNode("forbidden-1", "forbidden.example")), hideURL = true))).backup()
        assertEquals("A selection the backup holds stays", "local-1", kept.document.string("selectedNodeId"))
    }

    @Test fun aBackupWithNothingLeftToSaveFailsWithTheFirstRefusal() {
        val forbidden = backupSource("forbidden", "No backups", listOf(backupNode("forbidden-1", "forbidden.example")), "https://forbidden.example/sub", true)
        val fresh = backupSource("fresh", "Never updated", listOf(backupNode("fresh-1", "fresh.example")), "https://fresh.example/sub")
        fun refusal(catalog: Catalog) = (runCatching { catalog.backup() }.exceptionOrNull() as? AppFailure)?.code
        // A left-out provider's routing profile is not left to save.
        assertEquals("sharing_disabled", refusal(backupCatalog(null, listOf(forbidden, fresh), listOf(backupRoute("route-forbidden", "forbidden")), obj("forbidden" to "route-forbidden"))))
        assertEquals("sharing_refresh_required", refusal(backupCatalog(null, listOf(fresh, forbidden))))
        // A routing profile of the user's own is worth saving alone.
        val (document, excluded) = backupCatalog(null, listOf(fresh), listOf(backupRoute("route-local")), obj("fresh" to "route-local")).backup()
        assertEquals(0, document.objects("subscriptions").size)
        assertEquals(listOf("route-local"), document.objects("routes").map { it.getString("id") })
        assertEquals(0, document.mapObject("assignments").length())
        assertEquals(listOf("sharing_refresh_required"), excluded.map { it.getString("reason") })
        assertTrue("An empty catalog leaves out nothing", backupCatalog(null, emptyList()).backup().excluded.isEmpty())
    }

    @Test fun retryDelayAndCanonicalForm() {
        assertEquals(listOf(60, 60, 300, 1800, 1800), listOf(0, 1, 2, 3, 9).map(Catalog::retryDelay))
        assertEquals(Catalog.canonical(JSONObject("""{"b":[1,{"y":2,"x":"s"}],"a":null}""")), Catalog.canonical(JSONObject("""{"a":null,"b":[1,{"x":"s","y":2}]}""")))
        assertNotEquals(Catalog.canonical(JSONArray("[1,2]")), Catalog.canonical(JSONArray("[2,1]")))
    }
}
