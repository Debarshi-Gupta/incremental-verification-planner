package com.example.core;

import org.junit.Test;

import static org.junit.Assert.assertEquals;

public class CalcTest {
    @Test
    public void multipliesOperands() {
        assertEquals(6, Calc.multiply(2, 3));
    }
}
