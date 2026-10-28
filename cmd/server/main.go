// Command server serves the page that controls the light, for the Docker
// image.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/oneryx/yeelight/web"
)

const port = "10000"

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check the local server is healthy and exit (for Docker HEALTHCHECK)")
	dev := flag.Bool("dev", false, "serve the page's files from web/static on disk, to edit them without rebuilding")
	flag.Parse()
	if *healthcheck {
		os.Exit(checkHealth())
	}

	opts := web.Options{DefaultAddress: os.Getenv("YEELIGHT_HOST")}
	// The light's address and the schemes entered on the page are kept in
	// YEELIGHT_DATA.
	if dir := os.Getenv("YEELIGHT_DATA"); dir != "" {
		opts.AddressFile = filepath.Join(dir, "address")
		opts.SchemesFile = filepath.Join(dir, "schemes.json")
	}
	if *dev {
		opts.Static = os.DirFS("web/static")
	}
	s := web.New(opts)
	go s.Run(context.Background())

	log.Println("Listening on :" + port)
	log.Fatal(http.ListenAndServe(":"+port, s.Handler()))
}

// checkHealth returns the process exit code for -healthcheck. It doesn't
// check the light, which may simply be switched off at the wall.
func checkHealth() int {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
