package logos

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConvertScalesDown(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	src.Set(10, 10, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	jpeg.Encode(&b, src, nil)
	out, err := Convert(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Size(); got != (image.Point{256, 128}) {
		t.Fatalf("size = %v, want 256x128", got)
	}
	if _, err := Convert([]byte("<svg></svg>")); err == nil {
		t.Fatal("SVG accepted")
	}
}

func TestPlaceholder(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(Placeholder("Radio Noord")))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != Size {
		t.Fatalf("width = %d", img.Bounds().Dx())
	}
	// The corner has the house style's dark blue.
	if r, g, b, _ := img.At(2, 2).RGBA(); r>>8 != 0x14 || g>>8 != 0x21 || b>>8 != 0x3D {
		t.Fatalf("corner = %v", img.At(2, 2))
	}
	for name, want := range map[string]string{
		"Radio Noord": "RN", "NPO Radio 1": "NR", "qmusic": "QM", "": "FM", "538": "53", "Радио Рекорд": "РР", "日本放送": "FM",
	} {
		if got := Initials(name); got != want {
			t.Errorf("Initials(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCacheFallsBack(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/ok.png" {
			png.Encode(w, image.NewRGBA(image.Rect(0, 0, 64, 64)))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := New()
	ok := c.Get(context.Background(), "Station", srv.URL+"/ok.png")
	if ok.Generated || ok.Err != "" {
		t.Fatalf("ok logo = %+v", ok)
	}
	bad := c.Get(context.Background(), "Station", srv.URL+"/missing.png")
	if !bad.Generated || bad.Err == "" {
		t.Fatalf("missing logo = %+v", bad)
	}
	c.Get(context.Background(), "Station", srv.URL+"/ok.png")
	if hits != 2 {
		t.Fatalf("hits = %d, want 2 (cached)", hits)
	}
	if none := c.Get(context.Background(), "Station", ""); !none.Generated {
		t.Fatal("no logo URL did not give a generated logo")
	}
}
