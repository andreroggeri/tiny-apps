"""Find the page in a photo of a document and straighten it."""

import cv2
import numpy as np

# Detection runs on a copy this size (longest side); the crop uses the
# full-resolution photo.
WORK_SIZE = 800

# A page smaller than this share of the photo is probably something else.
MIN_AREA = 0.2
# A page bigger than this is already cropped: leave the photo alone.
MAX_AREA = 0.9


class NoPage(Exception):
    """No page found, or the photo is already cropped."""


def find_page(img: np.ndarray) -> np.ndarray:
    """Return the page's 4 corners in img's pixels, or raise NoPage."""
    h, w = img.shape[:2]
    scale = WORK_SIZE / max(h, w)
    small = cv2.resize(img, (round(w * scale), round(h * scale)), interpolation=cv2.INTER_AREA)
    area = small.shape[0] * small.shape[1]

    # Edges find the page on plain backgrounds, even a bright one. On busy
    # backgrounds (a couch, a patterned table) they find nothing, or an
    # outline that swallows a piece of the background; there, the page is
    # still the biggest bright patch.
    mask = _bright_mask(small)
    quad = _by_edges(small, area)
    if quad is None or _swallows(quad, mask):
        quad = _by_brightness(mask, area)
    if quad is None:
        raise NoPage("no page found")
    if cv2.contourArea(quad.astype(np.float32)) > MAX_AREA * area:
        raise NoPage("already cropped")
    return quad / scale


def _by_edges(small: np.ndarray, area: int):
    gray = cv2.GaussianBlur(cv2.cvtColor(small, cv2.COLOR_BGR2GRAY), (5, 5), 0)
    edges = cv2.dilate(cv2.Canny(gray, 50, 150), np.ones((3, 3), np.uint8), iterations=2)
    contours, _ = cv2.findContours(edges, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
    for c in sorted(contours, key=cv2.contourArea, reverse=True)[:5]:
        approx = cv2.approxPolyDP(c, 0.02 * cv2.arcLength(c, True), True)
        if len(approx) == 4 and cv2.isContourConvex(approx) and cv2.contourArea(approx) > MIN_AREA * area:
            return approx.reshape(4, 2).astype(float)
    return None


def _bright_mask(small: np.ndarray) -> np.ndarray:
    """255 where the photo is brighter than its background, with text and
    lines on the page filled in."""
    value = cv2.GaussianBlur(cv2.cvtColor(small, cv2.COLOR_BGR2HSV)[:, :, 2], (9, 9), 0)
    _, mask = cv2.threshold(value, 0, 255, cv2.THRESH_BINARY + cv2.THRESH_OTSU)
    return cv2.morphologyEx(mask, cv2.MORPH_CLOSE, np.ones((15, 15), np.uint8), iterations=2)


def _by_brightness(mask: np.ndarray, area: int):
    contours, _ = cv2.findContours(mask, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
    if not contours:
        return None
    blob = max(contours, key=cv2.contourArea)
    if cv2.contourArea(blob) < MIN_AREA * area:
        return None
    hull = cv2.convexHull(blob)
    for eps in (0.02, 0.03, 0.05, 0.08):
        approx = cv2.approxPolyDP(hull, eps * cv2.arcLength(hull, True), True)
        if len(approx) == 4:
            return approx.reshape(4, 2).astype(float)
    return cv2.boxPoints(cv2.minAreaRect(blob)).astype(float)


def _swallows(quad: np.ndarray, mask: np.ndarray) -> bool:
    """Whether the outline took in a piece of dark background. Dark ink on
    the page (a stamp at the edge) doesn't count: only what lies outside
    the bright patch's convex outline does."""
    contours, _ = cv2.findContours(mask, cv2.RETR_EXTERNAL, cv2.CHAIN_APPROX_SIMPLE)
    if not contours:
        return False
    hull = np.zeros(mask.shape, np.uint8)
    cv2.fillPoly(hull, [cv2.convexHull(max(contours, key=cv2.contourArea))], 1)
    inside = np.zeros(mask.shape, np.uint8)
    cv2.fillPoly(inside, [quad.astype(np.int32)], 1)
    return np.count_nonzero(inside & (1 - hull)) > 0.03 * np.count_nonzero(inside)


def _order(pts: np.ndarray) -> np.ndarray:
    """Top-left, top-right, bottom-right, bottom-left."""
    s = pts.sum(axis=1)
    d = np.diff(pts, axis=1).ravel()
    return np.array([pts[s.argmin()], pts[d.argmin()], pts[s.argmax()], pts[d.argmax()]], np.float32)


def straighten(img: np.ndarray, corners: np.ndarray) -> np.ndarray:
    tl, tr, br, bl = _order(corners)
    width = round(max(np.linalg.norm(tr - tl), np.linalg.norm(br - bl)))
    height = round(max(np.linalg.norm(bl - tl), np.linalg.norm(br - tr)))
    dst = np.array([[0, 0], [width, 0], [width, height], [0, height]], np.float32)
    m = cv2.getPerspectiveTransform(np.array([tl, tr, br, bl]), dst)
    return cv2.warpPerspective(img, m, (width, height))


def crop(data: bytes) -> bytes:
    """Return the page as a JPEG, or raise NoPage. Raises ValueError for
    data that isn't an image."""
    img = cv2.imdecode(np.frombuffer(data, np.uint8), cv2.IMREAD_COLOR)
    if img is None:
        raise ValueError("not an image")
    page = straighten(img, find_page(img))
    ok, out = cv2.imencode(".jpg", page, [cv2.IMWRITE_JPEG_QUALITY, 90])
    if not ok:
        raise ValueError("could not encode the crop")
    return out.tobytes()
