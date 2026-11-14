from utils.report import render


def test_render():
    assert render([1, 2]) == "1\n2"
