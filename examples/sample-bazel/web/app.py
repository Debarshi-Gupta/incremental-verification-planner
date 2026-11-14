from api import service


def handle(quantity):
    return service.total(3, quantity)
