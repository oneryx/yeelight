// Yeelight is the native app: a window showing the same page as the Docker
// image, served by the same handler through MyGo's mygo:// scheme.
package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	"github.com/oneryx/yeelight/web"
)

func main() {
	opts := web.Options{DefaultAddress: os.Getenv("YEELIGHT_HOST")}
	if dir, err := mygo.App.Path(mygo.PathUserData); err == nil {
		opts.AddressFile = filepath.Join(dir, "host")
		opts.SchemesFile = filepath.Join(dir, "schemes.json")
	}
	// Under mygo dev, edits to the page show on reload.
	if mygo.IsDev() {
		if _, err := os.Stat("web/static"); err == nil {
			opts.Static = os.DirFS("web/static")
		}
	}
	s := web.New(opts)
	if err := mygo.Protocol.Handle("mygo", s.Handler()); err != nil {
		log.Fatal(err)
	}

	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:           "Yeelight",
			URL:             "mygo://localhost/",
			BackgroundColor: "light-dark(#ffffff, #1c1c1e)",
			Width:           380,
			Height:          720,
			MinWidth:        340,
			MinHeight:       700,
			StateKey:        "main",
		})
		ctx, cancel := context.WithCancel(context.Background())
		win.OnClosed(cancel)
		go s.Run(ctx)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
