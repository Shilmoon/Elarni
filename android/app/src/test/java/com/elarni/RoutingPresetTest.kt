package com.elarni

import org.junit.Assert.*
import org.junit.Test
import java.io.File

class RoutingPresetTest {
    // The desktop's own list, read from its source, so the two cannot drift apart.
    private fun desktopList(): List<String> {
        val source = generateSequence(File(System.getProperty("user.dir")).absoluteFile) { it.parentFile }
            .map { File(it, "lib/routing_editor.dart") }.first { it.isFile }.readText()
        val body = source.substringAfter("const russianSitesPreset = <String>[").substringBefore("];")
        return Regex("'([^']+)'").findAll(body).map { it.groupValues[1] }.toList()
    }

    @Test fun russianSitesMatchTheDesktopPreset() {
        val desktop = desktopList()
        assertTrue("The desktop list was found", desktop.size > 30)
        assertEquals(desktop, RUSSIAN_SITES)
        assertEquals(desktop, withSites("", RUSSIAN_SITES).lines())
    }

    @Test fun theUsersLinesStayFirstAndEachSiteIsAddedOnce() {
        val once = withSites(" domain:example.test \n\n domain:vk.com\r\nfull:a.example", RUSSIAN_SITES)
        assertEquals(listOf("domain:example.test", "domain:vk.com", "full:a.example") + RUSSIAN_SITES.filter { it != "domain:vk.com" }, once.lines())
        assertEquals("Applying it again adds nothing", once, withSites(once, RUSSIAN_SITES))
    }
}
