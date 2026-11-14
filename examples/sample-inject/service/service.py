from orders.core import total


def invoice_total(lines):
    return total([{"amount": line} for line in lines])
