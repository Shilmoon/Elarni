package com.elarni

import org.junit.Assert.*
import org.junit.Test

class TrafficRateTest {
    @Test fun bytesPerSecondBetweenTwoReadings() {
        assertEquals(TrafficRate(down = 1500, up = 250), TrafficRate.between(longArrayOf(10_000, 1_000, 500), longArrayOf(12_000, 4_000, 1_000)))
        assertEquals("Under a second still counts per second", TrafficRate(2048, 0), TrafficRate.between(longArrayOf(0, 0, 7), longArrayOf(500, 1024, 7)))
    }

    @Test fun aLowerCounterIsANewStartNotNegativeTraffic() {
        assertEquals(TrafficRate(down = 0, up = 100), TrafficRate.between(longArrayOf(1_000, 9_000, 100), longArrayOf(3_000, 200, 300)))
        assertEquals(TrafficRate(0, 0), TrafficRate.between(longArrayOf(1_000, 9_000, 9_000), longArrayOf(3_000, 0, 0)))
    }

    @Test fun noTimeBetweenReadingsGivesNoRate() {
        assertNull(TrafficRate.between(longArrayOf(5_000, 0, 0), longArrayOf(5_000, 1_000, 1_000)))
        assertNull("A clock that went back", TrafficRate.between(longArrayOf(5_000, 0, 0), longArrayOf(4_000, 1_000, 1_000)))
    }
}
