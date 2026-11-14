from app.api import api_health


def test_api_health():
    assert api_health() == {"status": "ok"}
