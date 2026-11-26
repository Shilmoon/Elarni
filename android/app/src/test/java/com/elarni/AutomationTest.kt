package com.elarni

import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class AutomationTest {
    private fun ready() = Automation(JSONObject()).apply { intent = true; online = true }

    @Test fun lowestLatencySwitchesOnlyForAClearGain() {
        val automation = ready()
        val order = listOf("current", "other")
        fun choose(current: Double, other: Double, time: Double = 1000.0) =
            automation.choose("lowestLatency", order, "current", mapOf("current" to current, "other" to other), healthy = true, time = time)
        assertEquals("other", choose(200.0, 100.0))
        assertEquals("Under 20 ms is noise", "current", choose(100.0, 85.0))
        assertEquals("Under 20 % is noise", "current", choose(1000.0, 850.0))
        assertEquals("A tie keeps the server", "current", choose(100.0, 100.0))
        automation.lastSwitch = 950.0
        assertEquals("No switch within 60 s of the last one", "current", choose(200.0, 100.0))
        assertEquals("other", choose(200.0, 100.0, time = 1010.0))
    }

    @Test fun lowestLatencyLeavesAFailingServerAndKeepsAnUnscoredHealthyOne() {
        val automation = ready()
        val order = listOf("current", "slow")
        assertEquals("slow", automation.choose("lowestLatency", order, "current", mapOf("current" to 10.0, "slow" to 900.0), healthy = false, time = 0.0))
        assertEquals("current", automation.choose("lowestLatency", order, "current", mapOf("slow" to 5.0), healthy = true, time = 1000.0))
        assertNull(automation.choose("lowestLatency", order, "current", emptyMap(), healthy = false, time = 0.0))
    }

    @Test fun orderedFallbackTakesTheFirstPassingBackup() {
        val automation = ready()
        val order = listOf("a", "b", "c")
        assertEquals("a", automation.choose("orderedFallback", order, "a", mapOf("a" to 50.0, "b" to 10.0), healthy = true, time = 0.0))
        assertEquals("b", automation.choose("orderedFallback", order, "a", mapOf("a" to 50.0, "b" to 10.0, "c" to 5.0), healthy = false, time = 0.0))
        assertEquals("A backup that failed its probe has no score", "c", automation.choose("orderedFallback", order, "a", mapOf("c" to 5.0), healthy = false, time = 0.0))
        assertEquals("a", automation.choose("fixed", order, "a", mapOf("a" to 50.0), healthy = true, time = 0.0))
        assertNull(automation.choose("fixed", order, "a", mapOf("a" to 50.0, "b" to 1.0), healthy = false, time = 0.0))
    }

    @Test fun roundsSkipCoolingServersAndRotateThroughTheRest() {
        val automation = ready()
        val others = (1..12).map { "s$it" }
        val first = automation.round(listOf("current") + others, "current", 0.0)
        assertEquals(10, first.size)
        assertEquals("current", first.first())
        val second = automation.round(listOf("current") + others, "current", 0.0)
        assertEquals("current", second.first())
        assertEquals("Two rounds probe every backup", others.toSet(), (first + second).toSet() - "current")

        automation.cooldown["s1"] = 60.0
        automation.cooldown["s2"] = 90.0
        assertFalse(automation.round(listOf("current", "s1", "s2", "s3"), "current", 59.0).any { it in listOf("s1", "s2") })
        assertTrue("A cooldown ends at its time", "s1" in automation.round(listOf("current", "s1", "s2", "s3"), "current", 60.0))
    }

    @Test fun retriesBackOffThenExhaustTheEpisode() {
        val automation = ready()
        val retries = mutableListOf<Double>()
        var time = 0.0
        while (true) {
            automation.schedule(time)
            if (automation.exhausted) break
            assertEquals("retrying", automation.status)
            assertFalse("A retry waits for its time", automation.begin(automation.retryAt!! - 0.5))
            time = automation.retryAt!!
            retries.add(time)
            assertTrue(automation.begin(time))
        }
        assertEquals(listOf(1.0, 3.0, 7.0, 15.0, 31.0, 61.0), retries)
        assertEquals(6, automation.attempts)
        assertEquals("exhausted", automation.status)
        assertNull(automation.healthAt)
    }

    @Test fun anEpisodeEndsAfterTwoMinutes() {
        val automation = ready()
        automation.schedule(0.0)
        assertTrue(automation.begin(100.0))
        automation.schedule(100.0)
        assertTrue(automation.begin(automation.retryAt!!))
        automation.schedule(118.0)
        assertTrue("A retry past 120 s ends the episode", automation.exhausted)
    }

    @Test fun nothingRetriesOfflineAsleepOrWithoutIntent() {
        for (setup in listOf<Automation.() -> Unit>({ online = false }, { sleeping = true }, { intent = false })) {
            val automation = ready().apply(setup)
            automation.schedule(0.0)
            assertNull(automation.retryAt)
            assertFalse(automation.begin(10.0))
        }
        val healthy = ready()
        healthy.schedule(0.0); healthy.begin(1.0); healthy.healthy(5.0)
        assertEquals(0, healthy.attempts)
        assertEquals(5.0 + Automation.healthInterval, healthy.healthAt!!, 0.0)
    }

    @Test fun healthCheckWaitsForTrafficAfterAPass() {
        val automation = ready().apply { healthy(0.0) }
        var reads = 0
        assertFalse("Not due yet", automation.healthDue(Automation.healthInterval - 1, passed = true) { reads++; true })
        assertEquals("The counter is read only when a check is due", 0, reads)
        val due = Automation.healthInterval + 1
        assertFalse("A passing server that carried nothing since is not probed", automation.healthDue(due, passed = true) { reads++; false })
        assertEquals(1, reads)
        assertEquals("The next look is one interval later", due + Automation.healthInterval, automation.healthAt!!, 0.0)
        assertTrue("Traffic left since the pass", automation.healthDue(due + Automation.healthInterval, passed = true) { true })
        assertTrue("The last check failed", automation.healthDue(due + Automation.healthInterval, passed = false) { reads++; false })
        assertEquals("A failed check is repeated without reading the counter", 1, reads)
        automation.failures = 1
        assertTrue("A health failure is checked again", automation.healthDue(due + Automation.healthInterval, passed = true) { false })
    }

    @Test fun unattendedRecoveryWaitsLongerEachTime() {
        val automation = Automation(obj("exhausted" to true)).apply { intent = true; online = true }
        var time = 0.0
        val waits = mutableListOf<Double>()
        repeat(4) {
            val start = time
            automation.unattended(time)
            while (automation.exhausted) { time += 1.0; automation.unattended(time) }
            waits.add(time - start)
            automation.exhausted = true
        }
        assertEquals("Past TSPU's ~120 s SNI freeze, then longer", listOf(150.0, 300.0, 600.0, 600.0), waits)

        val offline = Automation(obj("exhausted" to true)).apply { intent = true; online = true }
        offline.unattended(0.0)
        offline.online = false; offline.unattended(100.0)
        offline.online = true; offline.unattended(149.0); offline.unattended(160.0)
        assertTrue("Going offline restarts the wait", offline.exhausted)
        offline.unattended(299.0)
        assertFalse(offline.exhausted)
    }

    @Test fun anInterruptedEpisodeStaysExhaustedAfterARestart() {
        assertFalse(Automation(JSONObject()).exhausted)
        assertTrue(Automation(obj("attempts" to 2)).exhausted)
        assertTrue(Automation(obj("interrupted" to true)).exhausted)
        val running = ready()
        running.schedule(0.0)
        assertTrue(Automation(running.episode()).exhausted)
    }

    // N9-01: the transfer check's schedule, with injected short timings instead of 5 s, 10 s and 15 min.
    private fun <T> shortTransferTimings(body: () -> T): T {
        TransferCheck.delay = 1.0; TransferCheck.recheck = 2.0; TransferCheck.interval = 30.0
        try { return body() } finally { TransferCheck.delay = 5.0; TransferCheck.recheck = 10.0; TransferCheck.interval = 900.0 }
    }

    @Test fun twoStallsInARowConfirmAndTheFirstIsCheckedAgainSoon() = shortTransferTimings {
        val check = TransferCheck()
        var reads = 0
        assertFalse("Nothing runs before a core is up", check.due(100.0) { reads++; 0L })
        check.started(100.0, "a:1")
        assertFalse("Not before the first delay", check.due(100.5) { reads++; 0L })
        assertTrue("The first check needs no traffic", check.due(101.0) { reads++; 0L })
        assertEquals(0, reads)
        assertFalse(check.record(TransferCheck.STALLED, 102.0, 10L))
        assertFalse("One stall is not confirmed", check.confirmed)
        assertFalse(check.due(103.0) { 10L })
        assertTrue("The recheck comes soon, traffic or not", check.due(104.0) { reads++; 10L })
        assertEquals("A recheck reads no counter", 0, reads)
        assertTrue("The second stall in a row confirms", check.record(TransferCheck.STALLED, 105.0, 10L))
        assertTrue(check.confirmed)
        assertFalse("It confirms once", check.record(TransferCheck.STALLED, 140.0, 20L))
        assertTrue(check.confirmed)
        assertFalse(check.record(TransferCheck.OK, 180.0, 30L))
        assertFalse("A passing check ends the stall", check.confirmed)
    }

    @Test fun anInconclusiveCheckNeitherCountsNorEndsARunOfStalls() = shortTransferTimings {
        val check = TransferCheck()
        check.started(0.0, "a:1")
        repeat(3) { check.record(TransferCheck.INCONCLUSIVE, 10.0 + it, 0L) }
        assertEquals(0, check.stalls)
        check.record(TransferCheck.STALLED, 20.0, 0L)
        check.record(TransferCheck.INCONCLUSIVE, 22.0, 0L)
        assertEquals("Inconclusive is no stall and no pass", 1, check.stalls)
        assertFalse("After it the next check waits the long interval", check.due(30.0) { 0L })
        assertTrue("A pending stall's next check needs no traffic", check.due(52.0) { 0L })
        assertTrue(check.record(TransferCheck.STALLED, 53.0, 0L))
    }

    @Test fun laterChecksRunOnlyWhileTheUplinkGrows() = shortTransferTimings {
        val check = TransferCheck()
        check.started(0.0, "a:1")
        check.record(TransferCheck.OK, 1.0, 500L)
        assertFalse(check.due(30.0) { 900L })
        assertFalse("No traffic since the last check", check.due(31.0) { 500L })
        assertFalse("It looks again an interval later", check.due(60.0) { 900L })
        assertTrue("Traffic left since", check.due(61.0) { 900L })
        check.record(TransferCheck.STALLED, 62.0, 900L)
        check.record(TransferCheck.STALLED, 64.0, 900L)
        assertFalse("A confirmed stall is checked again only with traffic too", check.due(94.0) { 900L })
        assertTrue(check.due(124.0) { 901L })
    }

    @Test fun noTrafficChecksAtOnceAndAStopEndsTheSchedule() = shortTransferTimings {
        val check = TransferCheck()
        check.noTraffic()
        assertFalse("Without a core nothing runs", check.due(0.0) { 0L })
        check.started(0.0, "a:1")
        check.record(TransferCheck.OK, 1.0, 0L)
        assertFalse(check.due(2.0) { 0L })
        check.noTraffic()
        assertTrue("Liveness found no traffic: at once", check.due(2.0) { 0L })
        check.record(TransferCheck.INCONCLUSIVE, 3.0, 0L)
        assertFalse("Once", check.due(4.0) { 0L })
        check.record(TransferCheck.STALLED, 5.0, 0L); check.record(TransferCheck.STALLED, 7.0, 0L)
        check.stopped()
        assertFalse(check.confirmed)
        assertFalse(check.due(1000.0) { 1L })
        check.started(1000.0, "a:1")
        assertEquals("A new core starts over", 0, check.stalls)
        assertTrue(check.due(1001.0) { 0L })
    }

    // A check takes up to about 20 s; the tick goes on every 2 s meanwhile and reads the verdict on a later pass.
    @Test fun aCheckRunsBesideTheTickWhichKeepsItsPace() = shortTransferTimings {
        val check = TransferCheck()
        check.started(0.0, "a:1")
        assertTrue(check.due(1.0) { 0L })
        val pending = check.begin()
        val release = java.util.concurrent.CountDownLatch(1)
        val runner = Thread { release.await(); pending.finish(TransferCheck.STALLED) }.apply { start() }
        var ticks = 0
        for (time in generateSequence(3.0) { it + 2.0 }.takeWhile { it <= 41.0 }) {
            assertNull("No verdict while the check runs", check.takeVerdict())
            assertFalse("No second check while one runs", check.due(time) { ticks.toLong() })
            if (time == 21.0) check.noTraffic()
            ticks++
        }
        assertEquals("Twenty ticks went by while the check ran", 20, ticks)
        release.countDown(); runner.join(5000)
        assertEquals("The next tick reads the verdict", TransferCheck.STALLED, check.takeVerdict())
        assertNull("Once", check.takeVerdict())
        assertFalse(check.record(TransferCheck.STALLED, 43.0, 0L))
        assertTrue("Its recheck follows as usual", check.due(45.0) { 0L })
        // A check that outlives its core says nothing about the next one, and the next waits for it to end.
        val old = check.begin()
        check.stopped(); check.started(50.0, "a:1")
        assertFalse(check.due(52.0) { 0L })
        old.finish(TransferCheck.STALLED)
        assertNull("Its verdict is dropped", check.takeVerdict())
        assertEquals(0, check.stalls)
        assertTrue("The new core's check starts", check.due(52.0) { 0L })
    }

    @Test fun recoveryRestartsAServerOncePerStallEpisode() = shortTransferTimings {
        val check = TransferCheck()
        fun confirm(time: Double) { check.record(TransferCheck.STALLED, time, 0L); check.record(TransferCheck.STALLED, time + 2, 0L) }
        check.started(0.0, "a:1"); confirm(1.0)
        assertTrue("The first restart for a stall is allowed", check.mayRestart("a:1"))
        check.restarting("a:1"); check.stopped(); check.started(10.0, "a:1"); confirm(11.0)
        assertTrue(check.confirmed)
        assertFalse("Stalled again after it: no second restart", check.mayRestart("a:1"))
        assertTrue("Another server may restart", check.mayRestart("b:1"))
        check.record(TransferCheck.INCONCLUSIVE, 40.0, 0L)
        assertFalse("An inconclusive check ends nothing", check.mayRestart("a:1"))
        check.record(TransferCheck.OK, 80.0, 0L)
        assertTrue("A passing check ends the episode", check.mayRestart("a:1"))
        check.restarting("a:1"); check.stopped(); check.started(90.0, "b:1")
        assertTrue("Another server ran", check.mayRestart("a:1"))
        check.restarting("a:1"); check.stopped(); check.started(100.0, "a:2")
        assertTrue("A changed server is another server", check.mayRestart("a:1"))
        check.restarting("a:1"); check.pathChanged()
        assertTrue("The network path changed", check.mayRestart("a:1"))
    }

    @Test fun oneTargetThatPassesWinsAndOnlyAnsweringTargetsCount() {
        val ok = TransferCheck.OK; val stalled = TransferCheck.STALLED; val inconclusive = TransferCheck.INCONCLUSIVE
        assertEquals(ok, TransferCheck.combine(listOf(stalled, ok)))
        assertEquals(stalled, TransferCheck.combine(listOf(stalled, stalled)))
        assertEquals("A target that did not answer says nothing", stalled, TransferCheck.combine(listOf(inconclusive, stalled)))
        assertEquals(inconclusive, TransferCheck.combine(listOf(inconclusive, inconclusive)))
        assertEquals(inconclusive, TransferCheck.combine(emptyList()))
    }

    @Test fun aCoreThatStopsByItselfRestartsTwiceInTenMinutesAtMost() {
        val restarts = CrashRestarts()
        assertTrue(restarts.allow(0.0))
        assertTrue(restarts.allow(30.0))
        assertFalse("A third within ten minutes of the first", restarts.allow(599.0))
        assertTrue("Ten minutes after the first, the refused one counted for nothing", restarts.allow(600.0))
        assertFalse("Two in the last ten minutes again", restarts.allow(629.0))
        assertTrue(restarts.allow(630.0))
        val quiet = CrashRestarts()
        assertTrue(quiet.allow(0.0))
        assertTrue("One stop an hour always restarts", quiet.allow(3600.0) && quiet.allow(7200.0) && quiet.allow(10800.0))
    }

    @Test fun aStartAtBootTriesAgainAfterGrowingDelaysForTwoMinutes() {
        val retry = StartRetry()
        assertNull(retry.retryAt)
        val tries = mutableListOf<Double>()
        var time = 0.0
        while (retry.failed(time)) { tries.add(retry.retryAt!!); time = retry.retryAt!! }
        assertEquals("2, 4, 8, 16, then 30 s; the last try may come at the end of the window", listOf(2.0, 6.0, 14.0, 30.0, 60.0, 90.0, 120.0), tries)
        assertNull("Given up", retry.retryAt)
        assertFalse(retry.due(1000.0))
        // A failure that took its lookup's 5 s counts from when it ended, and the window from the first failure.
        val slow = StartRetry()
        assertTrue(slow.failed(5.0)); assertEquals(7.0, slow.retryAt!!, 0.0)
        assertTrue(slow.failed(12.0)); assertEquals(16.0, slow.retryAt!!, 0.0)
        assertFalse(slow.due(15.9)); assertTrue(slow.due(16.0)); assertTrue(slow.due(17.0))
    }

    @Test fun aNetworkThatComesBringsTheWaitingTryForwardWithinTheWindow() {
        val retry = StartRetry()
        retry.networkAvailable(1.0)
        assertNull("Nothing waits, so nothing is due", retry.retryAt)
        retry.failed(0.0); retry.failed(2.0)
        assertEquals(6.0, retry.retryAt!!, 0.0)
        retry.networkAvailable(3.0)
        assertTrue(retry.due(3.0))
        retry.networkAvailable(5.0)
        assertEquals("Never later than it was", 3.0, retry.retryAt!!, 0.0)
        // A network that keeps coming and going cannot stretch the attempts past the window.
        var time = 3.0
        while (retry.failed(time)) { retry.networkAvailable(time + 0.5); time = retry.retryAt!! }
        assertTrue("Last try at $time", time <= StartRetry.WINDOW)
        // A later start counts its own window.
        assertTrue(retry.failed(500.0))
        assertEquals(502.0, retry.retryAt!!, 0.0)
        retry.reset()
        assertNull(retry.retryAt)
        assertTrue(retry.failed(600.0)); assertEquals("A reset starts the delays over", 602.0, retry.retryAt!!, 0.0)
    }

    @Test fun onlyAStartTheNetworkCouldFixWaits() {
        val unanswered = AppFailure("connection_failed", java.net.UnknownHostException("server.example"))
        assertTrue("The server's name had no answer", StartRetry.retryable(unanswered, offline = false))
        assertTrue("A download's lookup failed", StartRetry.retryable(AppFailure("dns_failed"), offline = false))
        assertTrue("A network change cancelled a download", StartRetry.retryable(AppFailure("fetch_cancelled"), offline = false))
        assertFalse("Online, a refused start is the server's", StartRetry.retryable(AppFailure("connection_failed"), offline = false))
        assertFalse(StartRetry.retryable(AppFailure("fetch_failed"), offline = false))
        assertTrue("Without a network, the start could not get anywhere", StartRetry.retryable(AppFailure("connection_failed"), offline = true))
        assertTrue(StartRetry.retryable(AppFailure("fetch_failed"), offline = true))
        for (code in listOf("invalid_settings", "per_app_none_installed", "vpn_consent_required", "no_selection", "storage_unavailable", "vpn_network_unavailable", "unsupported_profile"))
            assertFalse("$code fails the same way again", StartRetry.retryable(AppFailure(code), offline = true))
        assertFalse(StartRetry.retryable(IllegalStateException("unexpected"), offline = true))
    }

    @Test fun onlyAStartAtBootOrByAlwaysOnWaitsForTheNetwork() {
        fun waits(action: String?, restarted: Boolean = false, boot: Boolean = false, alwaysOn: Boolean = false) =
            ElarniVpnService.waitsForNetwork(action, restarted, boot, alwaysOn)
        // With Elarni as the always-on VPN, a server name with a typo must not show Connecting for 2 minutes.
        assertFalse("A Connect from the app, tile or widget", waits(ElarniVpnService.CONNECT, alwaysOn = true))
        assertFalse("A VPN the system ended along with its process, brought back", waits(null, alwaysOn = true))
        assertTrue("Android's own always-on start", waits(android.net.VpnService.SERVICE_INTERFACE))
        assertTrue("The always-on service's sticky restart", waits(null, restarted = true, alwaysOn = true))
        assertFalse("A sticky restart without always-on", waits(null, restarted = true))
        assertTrue("Connect after device restart", waits(ElarniVpnService.CONNECT, boot = true))
        assertFalse(waits(ElarniVpnService.CONNECT))
    }

    @Test fun aWaitingStartsLookupsShareOneDeadlineAndKeepTheFailureItRetriesOn() {
        val pool = java.util.concurrent.Executors.newCachedThreadPool()
        val never = java.util.concurrent.CountDownLatch(1)
        try {
            val loopback = java.net.InetAddress.getByAddress("answered.example", byteArrayOf(127, 0, 0, 1))
            fun query(answer: () -> List<java.net.InetAddress>) = pool.submit<List<java.net.InetAddress>> { answer() }
            val silent = (1..3).map { index -> "silent$index.example" to query { never.await(); emptyList() } }
            val lookups = Lookups(mapOf("answered.example" to query { listOf(loopback) },
                "nxdomain.example" to query { throw java.net.UnknownHostException("nxdomain.example") }) + silent)
            val started = System.nanoTime()
            val answers = lookups.await(timeoutMs = 300)
            val elapsed = (System.nanoTime() - started) / 1_000_000
            assertEquals(listOf(loopback), answers.getValue("answered.example").getOrThrow())
            // The start reads these as names without an answer, which the network coming up may give.
            for (name in listOf("nxdomain.example") + silent.map { it.first }) {
                val failure = answers.getValue(name).exceptionOrNull()
                assertTrue("$name: $failure", failure is Exception && Engine.lookupFailed(failure))
            }
            assertTrue("Three silent names wait 300 ms together, not one after another: $elapsed ms", elapsed in 300L until 800L)
            assertTrue("A query past the deadline is cancelled", silent.all { it.second.isCancelled })
        } finally { never.countDown(); pool.shutdownNow() }
    }

    @Test fun aServerNameWithoutAnAnswerIsALookupFailure() {
        assertTrue("No answer in 5 s", Engine.lookupFailed(java.util.concurrent.TimeoutException()))
        assertTrue("No answer at all", Engine.lookupFailed(java.util.concurrent.ExecutionException(java.net.UnknownHostException("server.example"))))
        assertFalse(Engine.lookupFailed(java.util.concurrent.ExecutionException(SecurityException("no INTERNET"))))
        assertFalse("A blocked host mapping", Engine.lookupFailed(AppFailure("connection_failed")))
    }

    @Test fun aServerThatPassedTrafficRestartsItsCoreAfterTwoFailedChecksInARow() {
        val server = "a:1"
        fun checks(vararg results: Boolean) = DeadCore().let { core -> results.map { core.checked(server, it) } }
        assertEquals("Two failures after a pass", listOf(false, false, true), checks(true, false, false))
        assertEquals("A server that never passed is only reported", listOf(false, false, false, false), checks(false, false, false, false))
        assertEquals("One failure", listOf(false, false), checks(true, false))
        assertEquals("A pass between failures", listOf(false, false, false), checks(false, true, false))
        assertEquals("The failures must follow each other", listOf(false, false, false, false), checks(true, false, true, false))

        val core = DeadCore()
        core.checked(server, true); core.checked(server, false)
        assertTrue(core.checked(server, false))
        // The restart starts a new core, which clears the pass.
        core.reset()
        assertFalse(core.checked(server, false))
        assertFalse("No second restart until a pass", core.checked(server, false))
        assertFalse(core.checked(server, false))
        assertFalse(core.checked(server, true))
        assertFalse(core.checked(server, false))
        assertTrue("A new pass allows the next restart", core.checked(server, false))

        val changed = DeadCore()
        changed.checked(server, true); changed.checked("a:2", false)
        assertFalse("A pass counts only for the server and revision that passed", changed.checked("a:2", false))
    }

    @Test fun recoveryPrefersRussianEntriesInWhitelistModeThenTcp443() {
        val automation = ready()
        val order = listOf("a", "b", "c", "d")
        assertEquals(listOf("c", "a", "b", "d"), automation.recoveryOrder(order, "a", tcp443 = setOf("c"), russianEntry = setOf("d")))
        assertEquals("A failing TCP 443 server keeps the user's order", order, automation.recoveryOrder(order, "c", tcp443 = setOf("c"), russianEntry = setOf("d")))
        automation.observe("a", passed = false, russianEntry = false)
        assertFalse(automation.whitelist)
        automation.observe("b", passed = false, russianEntry = false)
        automation.observe("d", passed = true, russianEntry = true)
        assertTrue(automation.whitelist)
        assertEquals(listOf("d", "c", "a", "b"), automation.recoveryOrder(order, "a", tcp443 = setOf("c"), russianEntry = setOf("d")))
        automation.observe("b", passed = true, russianEntry = false)
        assertFalse("A foreign pass ends whitelist mode", automation.whitelist)
    }

    @Test fun tcp443RecognizesOnlyTlsLikeTcpOnPort443() {
        fun config(protocol: String, port: Int, network: String = "tcp", alpn: List<String>? = null) = obj("outbounds" to JSONArray(listOf(obj(
            "protocol" to protocol, "settings" to obj("vnext" to JSONArray(listOf(obj("address" to "example.com", "port" to port)))),
            "streamSettings" to obj("network" to network, "tlsSettings" to obj("alpn" to alpn?.let { JSONArray(it) }))))))
        assertTrue(Automation.tcp443(config("vless", 443)))
        assertFalse(Automation.tcp443(config("vless", 8443)))
        assertFalse(Automation.tcp443(config("hysteria", 443)))
        // ShadowQUIC has no stream settings; its port is in its own settings.
        assertFalse(Automation.tcp443(obj("outbounds" to JSONArray(listOf(obj("protocol" to "shadowquic", "settings" to obj("address" to "example.com", "port" to 443)))))))
        assertFalse(Automation.tcp443(config("vless", 443, network = "kcp")))
        assertFalse(Automation.tcp443(config("vless", 443, alpn = listOf("h3"))))
        assertTrue(Automation.tcp443(obj("outbounds" to JSONArray(listOf(obj("protocol" to "shadowsocks", "settings" to obj("servers" to JSONArray(listOf(obj("port" to 443))))))))))
        assertFalse("A malformed config is not TCP 443", Automation.tcp443(JSONObject()))
    }

    @Test fun validateAcceptsAWorkingConfigurationAndRefusesUnsafeOnes() {
        val catalog = Catalog(obj("selectedNodeId" to "a", "subscriptions" to JSONArray(listOf(obj("id" to "s", "nodes" to JSONArray(listOf(
            obj("id" to "a", "format" to "xrayProfile"), obj("id" to "b", "format" to "xrayProfile"), obj("id" to "t", "format" to "telegram"))))))))
        fun target(url: String, id: String = "check", status: Int = 204) = obj("id" to id, "url" to url, "expectedStatus" to status, "expectedBody" to "")
        fun config(vararg changes: Pair<String, Any?>) = obj("schemaVersion" to 1, "enabled" to true, "policy" to "lowestLatency", "subscriptionID" to "s",
            "nodeIDs" to JSONArray(listOf("a", "b")), "targets" to JSONArray(listOf(target("https://www.gstatic.com/generate_204")))).apply { changes.forEach { (key, value) -> put(key, value) } }
        Automation.validate(config(), catalog)
        Automation.validate(Automation.defaults(), catalog)
        // Alone, the second spelling of the host below is valid, so its refusal is about the duplicate.
        Automation.validate(config("targets" to JSONArray(listOf(target("https://WWW.gstatic.com./b")))), catalog)
        val refused = mapOf(
            "unknown policy" to config("policy" to "random"),
            "three targets" to config("targets" to JSONArray((1..3).map { target("https://h$it.example/", "t$it") })),
            "one host twice" to config("targets" to JSONArray(listOf(target("https://www.gstatic.com/a", "one"), target("https://WWW.gstatic.com./b", "two")))),
            "credentials in the URL" to config("targets" to JSONArray(listOf(target("https://user:pass@example.com/")))),
            "a script URL" to config("targets" to JSONArray(listOf(target("javascript:alert(1)")))),
            "a failing status" to config("targets" to JSONArray(listOf(target("https://example.com/", status = 404)))),
            "selected server missing" to config("nodeIDs" to JSONArray(listOf("b"))),
            "a Telegram proxy" to config("nodeIDs" to JSONArray(listOf("a", "t"))),
            "a duplicate server" to config("nodeIDs" to JSONArray(listOf("a", "a"))),
            "a Russian entry outside the list" to config("russianEntryNodeIDs" to JSONArray(listOf("t"))),
            "no targets" to config("targets" to JSONArray()))
        for ((reason, value) in refused) {
            val error = runCatching { Automation.validate(value, catalog) }.exceptionOrNull()
            assertEquals(reason, "invalid_automation", (error as? AppFailure)?.code)
        }
    }
}
