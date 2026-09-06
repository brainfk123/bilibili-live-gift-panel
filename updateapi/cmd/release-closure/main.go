// release-closure validates a prepared exact-tag GitHub Release fixture.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/mirror"
)

func main() {
	metadata := flag.String("metadata", "", "local GitHub Release API JSON")
	assets := flag.String("assets", "", "local release asset directory")
	tag := flag.String("tag", "", "exact release tag")
	flag.Parse()
	if flag.NArg() != 0 || *metadata == "" || *assets == "" || *tag == "" {
		fmt.Fprintln(os.Stderr, "release closure validation failed")
		os.Exit(1)
	}
	body, err := os.ReadFile(*metadata)
	if err != nil {
		fail()
	}
	if err := mirror.ValidateLocalReleaseClosure(body, *assets, *tag); err != nil {
		fail()
	}
	fmt.Println("release closure valid")
}

func fail() {
	fmt.Fprintln(os.Stderr, "release closure validation failed")
	os.Exit(1)
}
