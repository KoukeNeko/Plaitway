import os
import sys

# The tests import one another by name (`import support`), whichever way they are discovered:
# from this directory, or as the package `tests` from the one above it.
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
