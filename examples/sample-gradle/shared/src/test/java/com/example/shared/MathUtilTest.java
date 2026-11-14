package com.example.shared;

import org.junit.Test;

import static org.junit.Assert.assertEquals;

public class MathUtilTest {
    @Test
    public void addsOperands() {
        assertEquals(4, MathUtil.add(2, 2));
    }
}
