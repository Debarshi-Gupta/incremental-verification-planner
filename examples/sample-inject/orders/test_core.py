from orders.core import apply_discount, total


def test_total():
    assert total([{"amount": 5}, {"amount": 7}]) == 12


def test_apply_discount():
    discounted = apply_discount([{"amount": 10}], 0.5)
    assert discounted[0]["amount"] == 5
