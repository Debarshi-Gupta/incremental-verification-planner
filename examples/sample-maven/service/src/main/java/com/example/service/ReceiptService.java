package com.example.service;

import com.example.core.Calc;

public class ReceiptService {
    public int total(int unitPrice, int quantity) {
        return Calc.multiply(unitPrice, quantity);
    }
}
