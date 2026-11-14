from app.calc import add, total


def test_add(numbers):
    assert add(numbers["a"], numbers["b"]) == 5


def test_total():
    assert total([1, 2, 3]) == 6
