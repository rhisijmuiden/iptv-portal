// Package logos serves station logos in a form every player can show: the
// original logo is downloaded once, scaled down and re-encoded as PNG
// (players such as MyTVOnline cannot show WebP, SVG or huge images), and a
// station without a usable logo gets a generated one with its initials in
// the house style.
package logos

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // decoders for image.Decode
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"

	"github.com/rhisijmuiden/iptv-portal/internal/version"
)

const (
	// Size is the largest width or height of a served logo.
	Size = 256
	// maxDownload is the most read of an original logo.
	maxDownload = 4 << 20
	// maxPixels refuses images that would take too much memory to decode.
	maxPixels = 4096 * 4096
	keepOK    = 24 * time.Hour
	keepError = time.Hour
	// maxEntries bounds the cache; it is emptied when it grows beyond.
	maxEntries = 4096
)

// House style colours (RHIS "nacht" and "koraal").
var (
	nacht  = color.RGBA{0x14, 0x21, 0x3D, 0xFF}
	koraal = color.RGBA{0xFF, 0x6B, 0x57, 0xFF}
)

// Logo is an encoded PNG.
type Logo struct {
	PNG []byte
	// Generated is true when the logo was made from the station's initials.
	Generated bool
	// Err says why the original logo could not be used.
	Err string
}

// Cache downloads, converts and keeps logos.
type Cache struct {
	client *http.Client
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]cached
}

type cached struct {
	logo Logo
	at   time.Time
}

// New returns an empty cache.
func New() *Cache {
	return &Cache{client: &http.Client{Timeout: 15 * time.Second}, now: time.Now, entries: map[string]cached{}}
}

// Get returns the logo for a station: the converted logoURL, or a
// generated one when logoURL is empty or cannot be used.
func (c *Cache) Get(ctx context.Context, name, logoURL string) Logo {
	key := logoURL + "\x00" + name
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	keep := keepOK
	if e.logo.Err != "" {
		keep = keepError
	}
	if ok && c.now().Sub(e.at) < keep {
		return e.logo
	}
	var l Logo
	if logoURL != "" {
		var err error
		if l.PNG, err = c.convert(ctx, logoURL); err != nil {
			l.Err = err.Error()
		}
	}
	if l.PNG == nil {
		l.PNG, l.Generated = Placeholder(name), true
	}
	c.mu.Lock()
	if len(c.entries) >= maxEntries {
		c.entries = map[string]cached{}
	}
	c.entries[key] = cached{logo: l, at: c.now()}
	c.mu.Unlock()
	return l
}

func (c *Cache) convert(ctx context.Context, logoURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, logoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "iptv-portal/"+version.Version)
	req.Header.Set("Accept", "image/png,image/jpeg,image/gif,image/webp,image/*;q=0.8")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server answered %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxDownload {
		return nil, fmt.Errorf("logo is larger than %d MB", maxDownload>>20)
	}
	return Convert(b)
}

// Convert decodes a PNG, JPEG, GIF, WebP or BMP image, scales it to fit
// Size×Size and encodes it as PNG.
func Convert(b []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("unsupported image format: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
		return nil, fmt.Errorf("image of %d×%d pixels is too large", cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("decoding %s: %w", format, err)
	}
	sb := src.Bounds()
	w, h := sb.Dx(), sb.Dy()
	if w > Size || h > Size {
		if w >= h {
			w, h = Size, max(1, h*Size/w)
		} else {
			w, h = max(1, w*Size/h), Size
		}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, xdraw.Src, nil)
	var out bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

var face = func() font.Face {
	f, err := opentype.Parse(gobold.TTF)
	if err != nil {
		panic(err)
	}
	ff, err := opentype.NewFace(f, &opentype.FaceOptions{Size: 96, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	return ff
}()

var faceMu sync.Mutex // font.Face is not safe for concurrent use

// Placeholder draws a logo with the initials of name: white letters on
// the house style's dark blue, with a coral bar below.
func Placeholder(name string) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, Size, Size))
	draw.Draw(img, img.Bounds(), image.NewUniform(nacht), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(Size/2-28, 190, Size/2+28, 200), image.NewUniform(koraal), image.Point{}, draw.Src)

	text := Initials(name)
	faceMu.Lock()
	d := &font.Drawer{Dst: img, Src: image.White, Face: face}
	adv := d.MeasureString(text)
	m := face.Metrics()
	d.Dot = fixed.Point26_6{
		X: fixed.I(Size/2) - adv/2,
		Y: fixed.I(112) + (m.Ascent-m.Descent)/2,
	}
	d.DrawString(text)
	faceMu.Unlock()

	var out bytes.Buffer
	png.Encode(&out, img)
	return out.Bytes()
}

// Initials returns up to two letters for a name: the first letters of the
// first two words ("Radio Noord" gives "RN", "NPO Radio 1" gives "NR"),
// or the first two of a single word.
func Initials(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var rs []rune
	switch len(words) {
	case 0:
		return "FM"
	case 1:
		rs = []rune(words[0])
		if len(rs) > 2 {
			rs = rs[:2]
		}
	default:
		rs = []rune{[]rune(words[0])[0], []rune(words[1])[0]}
	}
	// Go Bold has Latin, Greek and Cyrillic; other scripts fall back.
	for _, r := range rs {
		if r > 0x52F {
			return "FM"
		}
	}
	return strings.ToUpper(string(rs))
}

// Serve writes the logo of a station as PNG.
func (c *Cache) Serve(w http.ResponseWriter, r *http.Request, name, logoURL string) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	l := c.Get(ctx, name, logoURL)
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(l.PNG)))
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodHead {
		w.Write(l.PNG)
	}
}
