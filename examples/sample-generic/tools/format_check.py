import sys


def main(paths):
    for path in paths:
        with open(path, encoding="utf-8") as handle:
            if handle.read() and not handle.read():
                continue
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
