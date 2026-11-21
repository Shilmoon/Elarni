package com.elarni

import org.junit.Assert.*
import org.junit.Test

class PingSelectionTest {
    private val time = 1_800_000_000.0
    private val node = obj("id" to "a", "configurationRevision" to "r1")
    private fun failed(error: String, age: Double = 10.0, revision: String = "r1") =
        obj("nodeId" to "a", "status" to "failed", "error" to error, "timestamp" to time - age, "protocol" to "proxy", "configurationRevision" to revision)
    private fun passed(latency: Long, age: Double = 10.0) =
        obj("nodeId" to "a", "status" to "success", "latencyMs" to latency, "timestamp" to time - age, "protocol" to "proxy", "configurationRevision" to "r1")

    @Test fun aCurrentTestThatFailedToReachTheServerCounts() {
        for (code in listOf("ping_failed", "operation_failed", "connection_failed")) assertTrue(code, pingUnreachable(node, failed(code), time))
        assertTrue("A result without a delay", pingUnreachable(node, passed(-1), time))
    }

    @Test fun aTestThatDidNotRunAgainstTheServerDoesNotCount() {
        for (code in listOf("not_connected", "unsupported_ping_method", "unsupported_profile", "per_app_target_missing", "assets_unavailable",
            "probe_busy", "probe_cancelled", "cancelled", "not_found", "invalid_input", "invalid_target", "invalid_assets", "storage_corrupt", "storage_unavailable"))
            assertFalse(code, pingUnreachable(node, failed(code), time))
    }

    @Test fun anOldOrFutureTestDoesNotCount() {
        assertTrue(pingUnreachable(node, failed("ping_failed", age = 300.0), time))
        assertFalse(pingUnreachable(node, failed("ping_failed", age = 301.0), time))
        assertFalse(pingUnreachable(node, failed("ping_failed", age = 4 * 3600.0), time))
        assertFalse("A clock that went back", pingUnreachable(node, failed("ping_failed", age = -5.0), time))
    }

    @Test fun aPassedTestDoesNotCount() {
        assertFalse(pingUnreachable(node, passed(40), time))
        assertFalse(pingUnreachable(node, passed(0), time))
    }

    @Test fun aTestOfAnEarlierConfigurationDoesNotCount() {
        assertFalse(pingUnreachable(node, failed("ping_failed", revision = "r0"), time))
        // A node without a revision is tested under its ID.
        val plain = obj("id" to "a")
        assertTrue(pingUnreachable(plain, failed("ping_failed", revision = "a"), time))
        assertFalse(pingUnreachable(plain, failed("ping_failed", revision = "r1"), time))
    }
}
