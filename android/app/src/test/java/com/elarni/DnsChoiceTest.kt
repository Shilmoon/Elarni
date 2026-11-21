package com.elarni

import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

class DnsChoiceTest {
    private fun saved(flag: Boolean, mode: String) = JSONObject().put("useDNSFromJSON", flag).put("profileDNSMode", mode).put("socksPort", 10890)
    private fun pair(value: JSONObject) = value.getBoolean("useDNSFromJSON") to value.getString("profileDNSMode")

    @Test fun eachSavedPairBecomesOneChoice() {
        // The stored pair, the pair after the load, and whether the load must save it.
        val cases = listOf(
            Triple(saved(false, "inherit"), false to "default", true),
            Triple(saved(false, "routing"), true to "routing", true),
            Triple(saved(false, "default"), false to "default", false),
            Triple(saved(true, "inherit"), true to "inherit", false),
            Triple(saved(true, "routing"), true to "routing", false),
            Triple(saved(true, "default"), false to "default", true),
        )
        for ((value, expected, changed) in cases) {
            val before = value.toString()
            assertEquals(before, changed, DnsChoice.migrate(value))
            assertEquals(before, expected, pair(value))
            assertEquals("Other settings stay", 10890, value.getInt("socksPort"))
            val once = value.toString()
            assertFalse("A second load changes nothing: $before", DnsChoice.migrate(value))
            assertEquals(once, value.toString())
            DnsChoice.derive(value)
            assertEquals("Saving after the load keeps the choice: $before", once, value.toString())
        }
    }

    // An edit merges the new mode into settings whose flag still follows the old one.
    @Test fun anEditedModeIsNotTurnedBackByTheOldFlag() {
        for ((mode, flag) in listOf("inherit" to true, "routing" to true, "default" to false)) {
            val edited = saved(false, "default").put("profileDNSMode", mode)
            DnsChoice.derive(edited)
            assertEquals(flag to mode, pair(edited))
        }
        val off = saved(true, "inherit").put("profileDNSMode", "default")
        DnsChoice.derive(off)
        assertEquals(false to "default", pair(off))
    }
}
