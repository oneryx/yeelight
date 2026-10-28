// Icon renders assets/icon.svg to resources/icon.png, 1024×1024 with
// transparent corners, with MyGo's own renderer.
//
//	go run ./tools/icon
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"

	"github.com/egoist/mygo/ui"
)

const (
	size   = 1024
	half   = 412 // half the side of the rounded square, inside macOS's icon grid
	radius = 185
)

func main() {
	b, err := os.ReadFile("assets/icon.svg")
	if err != nil {
		log.Fatal(err)
	}
	svg, err := ui.ParseSVG(b)
	if err != nil {
		log.Fatal(err)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Box(c).Fill().Draw(func(p *ui.Painter, r ui.Rect) {
			p.Image(svg, ui.Rect{W: size, H: size}, ui.Contain)
		})
	}, size, size)
	tt.Frame()
	src := tt.Image()

	// Cut the rounded square, antialiased over a pixel, so its edge doesn't
	// blend with what was under the SVG.
	out := image.NewNRGBA(src.Bounds())
	for y := range size {
		for x := range size {
			qx := math.Max(math.Abs(float64(x)+0.5-size/2)-(half-radius), 0)
			qy := math.Max(math.Abs(float64(y)+0.5-size/2)-(half-radius), 0)
			a := math.Max(0, math.Min(1, 0.5-(math.Hypot(qx, qy)-radius)))
			if a == 0 {
				continue
			}
			c := src.RGBAAt(x, y)
			out.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(a * 255)})
		}
	}

	f, err := os.Create("resources/icon.png")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		log.Fatal(err)
	}
}
