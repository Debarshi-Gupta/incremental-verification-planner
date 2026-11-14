package com.example.service;

import org.junit.Test;

import static org.junit.Assert.assertEquals;

public class ReceiptIT {
    @Test
    public void issuesReceiptEndToEnd() {
        assertEquals(12, new ReceiptService().total(3, 4));
    }
}
