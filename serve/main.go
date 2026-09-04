// Command serve is a static file server for the demo page, so the wasm can
// be loaded over http rather than file:// (which blocks instantiateStreaming).
package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", ":8813", "listen address")
	dir := flag.String("dir", "docs", "directory to serve")
	flag.Parse()
	log.Printf("serving %s on http://localhost%s", *dir, *addr)
	log.Fatal(http.ListenAndServe(*addr, http.FileServer(http.Dir(*dir)))) //nolint:gosec
}
