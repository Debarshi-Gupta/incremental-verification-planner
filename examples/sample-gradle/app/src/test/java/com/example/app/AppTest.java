package com.example.app;

import org.junit.Test;

import static org.junit.Assert.assertEquals;

public class AppTest {
    @Test
    public void runsApp() {
        assertEquals(3, new App().run(1, 2));
    }
}
