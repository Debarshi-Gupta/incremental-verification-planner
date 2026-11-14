from service.service import invoice_total


def test_invoice_total():
    assert invoice_total([3, 4]) == 7
