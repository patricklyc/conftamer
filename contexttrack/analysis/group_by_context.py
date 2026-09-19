import sys

from contexttrack.inspect import main

if __name__ == "__main__":
    sys.argv.insert(1, "groups")
    main()
