package com.example.service;

import org.junit.Test;

import static org.junit.Assert.assertEquals;

public class ReceiptServiceTest {
    @Test
    public void computesTotal() {
        assertEquals(10, new ReceiptService().total(2, 5));
    }
}
