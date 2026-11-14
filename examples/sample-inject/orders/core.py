def total(items):
    return sum(item["amount"] for item in items)


def apply_discount(items, rate):
    return [dict(item, amount=item["amount"] * (1 - rate)) for item in items]
