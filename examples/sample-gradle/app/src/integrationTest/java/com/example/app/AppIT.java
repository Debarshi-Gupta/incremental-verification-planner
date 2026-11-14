package com.example.app;

import org.junit.Test;

import static org.junit.Assert.assertEquals;

public class AppIT {
    @Test
    public void runsAppEndToEnd() {
        assertEquals(5, new App().run(2, 3));
    }
}
