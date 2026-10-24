package pdf

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func encode(t *testing.T, w, h int, asPNG bool) Image {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if asPNG {
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		return Image{Data: buf.Bytes(), MimeType: "image/png"}
	}
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return Image{Data: buf.Bytes(), MimeType: "image/jpeg"}
}

func pages(data []byte) int { return bytes.Count(data, []byte("/Type /Page\n")) }

func TestFromImages(t *testing.T) {
	imgs := []Image{encode(t, 30, 40, false), encode(t, 40, 30, true)}

	plain, err := FromImages(imgs, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(plain, []byte("%PDF")) || pages(plain) != 2 {
		t.Fatalf("want a 2-page PDF, got %d pages", pages(plain))
	}

	withContext, err := FromImages(imgs, "Carteira de vacinação do bebê")
	if err != nil {
		t.Fatal(err)
	}
	if pages(withContext) != 3 {
		t.Fatalf("want 2 photo pages + 1 context page, got %d", pages(withContext))
	}
}

func TestFromImagesRejectsUnsupported(t *testing.T) {
	if _, err := FromImages([]Image{{Data: []byte("x"), MimeType: "image/webp"}}, ""); err == nil {
		t.Fatal("expected an error for webp")
	}
	if Supported("application/pdf") || !Supported("image/jpeg") {
		t.Fatal("Supported() is wrong")
	}
}
