#!/usr/bin/env python3
"""Compare two screenshots of the same screen, taken over different draw paths.

The legacy path renders lossless RLE bitmaps; EGFX renders RemoteFX, which is
lossy. So the two are not expected to be identical, and "they differ" is not by
itself a finding. What this reports is whether they differ like a lossy codec or
like a bug: noise spread thinly across the image, or a region that simply was not
drawn.

Run the same thing twice, once with -egfx and once without, and compare. Also
compare two runs of the *same* path: that is the control, and it establishes the
noise floor. Against a live Windows desktop it comes out as a handful of pixels
inside the taskbar clock, because the only thing that changes between runs is the
time.

    python3 scripts/compare-shots.py with.png without.png

Needs Pillow.
"""

import sys

try:
    from PIL import Image, ImageChops
except ImportError:
    sys.exit("compare-shots.py needs Pillow: pip install pillow")


def describe_block(name, bbox, size):
    if bbox is None:
        print("  %s: nothing" % name)
        return
    w, h = size
    print("  %s: x %.1f%%-%.1f%%, y %.1f%%-%.1f%%" % (
        name, 100.0 * bbox[0] / w, 100.0 * bbox[2] / w,
        100.0 * bbox[1] / h, 100.0 * bbox[3] / h))


def main(argv):
    if len(argv) != 3:
        sys.exit(__doc__)

    a = Image.open(argv[1]).convert("RGB")
    b = Image.open(argv[2]).convert("RGB")
    print("%s\nvs\n%s" % (argv[1], argv[2]))
    if a.size != b.size:
        print("sizes differ: %s and %s" % (a.size, b.size))
        return 1

    diff = ImageChops.difference(a, b)
    pixels = list(diff.getdata())
    n = len(pixels)
    worst = max(max(p) for p in pixels)
    mean = sum(sum(p) for p in pixels) / (n * 3)
    # "differs at all" is the wrong question for a lossy codec: almost every
    # pixel shifts by one or two. The count that matters is how many moved
    # enough to be seen.
    visible = sum(1 for p in pixels if max(p) > 16)

    print("  mean channel difference: %.3f of 255" % mean)
    print("  worst channel difference: %d" % worst)
    print("  pixels differing at all:  %d of %d (%.3f%%)" % (
        sum(1 for p in pixels if p != (0, 0, 0)), n,
        100.0 * sum(1 for p in pixels if p != (0, 0, 0)) / n))
    print("  pixels differing visibly: %d of %d (%.3f%%)" % (visible, n, 100.0 * visible / n))
    print("  where they are:")
    describe_block("overall", diff.getbbox(), a.size)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
