from api import service


def test_total():
    assert service.total(2, 5) == 10
