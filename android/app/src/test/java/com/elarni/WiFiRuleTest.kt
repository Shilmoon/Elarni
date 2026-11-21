package com.elarni

import org.junit.Assert.*
import org.junit.Test

class WiFiRuleTest {
    @Test fun aNameOnTheTrustedListStays() {
        assertFalse(WiFiRule.connects("Home", listOf("Home", "Office 5G")))
        assertTrue(WiFiRule.connects("Cafe", listOf("Home", "Office 5G")))
        assertTrue("Names match exactly", WiFiRule.connects("home", listOf("Home")))
    }

    @Test fun aNameAndroidHidesCountsAsUntrusted() {
        assertTrue(WiFiRule.connects(null, listOf("Home")))
    }

    @Test fun anEmptyTrustedListConnectsWithoutAName() {
        assertTrue(WiFiRule.connects(null, emptyList()))
        assertTrue(WiFiRule.connects("Home", emptyList()))
    }
}
