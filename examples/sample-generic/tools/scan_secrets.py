import sys


def main(paths):
    for path in paths:
        with open(path, encoding="utf-8") as handle:
            for number, line in enumerate(handle, 1):
                if "BEGIN RSA PRIVATE KEY" in line:
                    print(f"{path}:{number}: private key material")
                    return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
