from web import app


def test_handle():
    assert app.handle(4) == 12
