import threading
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

import cv2
import numpy as np

from tesourinha.crop import NoPage, crop, find_page
from tesourinha.server import Handler


def photo(bg=(40, 40, 40), corners=((300, 200), (1100, 260), (1050, 1400), (250, 1350)), size=(1600, 1300)):
    """A white page lying askew on a plain background, with some 'text'."""
    img = np.full((size[0], size[1], 3), bg, np.uint8)
    pts = np.array(corners, np.int32)
    cv2.fillPoly(img, [pts], (235, 235, 230))
    for y in range(400, 1200, 60):
        cv2.line(img, (380, y), (950, y + 20), (60, 60, 60), 6)
    return img


def jpeg(img):
    return cv2.imencode(".jpg", img)[1].tobytes()


def busy(size=(1600, 1300)):
    """A patterned, darker background, like a couch."""
    rng = np.random.default_rng(1)
    noise = rng.integers(60, 140, (size[0] // 20, size[1] // 20, 3), dtype=np.uint8)
    return cv2.resize(noise, (size[1], size[0]), interpolation=cv2.INTER_NEAREST)


class FindPage(unittest.TestCase):
    def assertCorners(self, got, want, tol=25):
        got = sorted(map(tuple, np.round(got).astype(int)))
        want = sorted(want)
        for g, w in zip(got, want):
            self.assertLess(np.hypot(g[0] - w[0], g[1] - w[1]), tol, f"{got} vs {want}")

    def test_plain_background(self):
        corners = ((300, 200), (1100, 260), (1050, 1400), (250, 1350))
        self.assertCorners(find_page(photo(corners=corners)), corners)

    def test_busy_background(self):
        corners = ((300, 200), (1100, 260), (1050, 1400), (250, 1350))
        img = busy()
        cv2.fillPoly(img, [np.array(corners, np.int32)], (235, 235, 230))
        self.assertCorners(find_page(img), corners)

    def test_already_cropped(self):
        img = np.full((1600, 1200, 3), 235, np.uint8)
        for y in range(100, 1500, 60):
            cv2.line(img, (80, y), (1100, y), (60, 60, 60), 6)
        with self.assertRaises(NoPage):
            find_page(img)

    def test_no_page(self):
        with self.assertRaises(NoPage):
            find_page(np.full((800, 600, 3), 90, np.uint8))


class Crop(unittest.TestCase):
    def test_straightens_to_page_size(self):
        out = cv2.imdecode(np.frombuffer(crop(jpeg(photo())), np.uint8), cv2.IMREAD_COLOR)
        h, w = out.shape[:2]
        self.assertAlmostEqual(w, 800, delta=30)
        self.assertAlmostEqual(h, 1145, delta=40)
        # Background gone: the edges are page-coloured.
        self.assertGreater(out[5:-5, 5].mean(), 180)

    def test_not_an_image(self):
        with self.assertRaises(ValueError):
            crop(b"hello")


class Server(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.srv = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=cls.srv.serve_forever, daemon=True).start()
        cls.url = f"http://127.0.0.1:{cls.srv.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()

    def post(self, body):
        req = urllib.request.Request(self.url + "/crop", body, {"Content-Type": "image/jpeg"})
        return urllib.request.urlopen(req)

    def test_crop(self):
        r = self.post(jpeg(photo()))
        self.assertEqual(r.status, 200)
        self.assertEqual(r.headers["Content-Type"], "image/jpeg")

    def test_no_page_is_204(self):
        self.assertEqual(self.post(jpeg(np.full((800, 600, 3), 90, np.uint8))).status, 204)

    def test_bad_body_is_400(self):
        with self.assertRaises(urllib.error.HTTPError) as e:
            self.post(b"hello")
        self.assertEqual(e.exception.code, 400)

    def test_healthz(self):
        self.assertEqual(urllib.request.urlopen(self.url + "/healthz").status, 200)


if __name__ == "__main__":
    unittest.main()
