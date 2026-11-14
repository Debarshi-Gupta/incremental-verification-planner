from app.core import core_total


def test_core_total():
    assert core_total([1, 2, 3]) == 6
