package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/publish"
)

func main() {
	err := run(os.Args[1:], func() (publish.Store, error) {
		return cosstore.New(os.Getenv("COS_BUCKET"), os.Getenv("COS_REGION"), os.Getenv("COS_SECRET_ID"), os.Getenv("COS_SECRET_KEY"), nil)
	}, time.Now, os.Stdout)
	if err != nil {
		log.Print("publish failed")
		os.Exit(1)
	}
}

func run(args []string, newStore func() (publish.Store, error), now func() time.Time, output io.Writer) error {
	input := publish.Input{}
	flags := flag.NewFlagSet("publish", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&input.Tag, "tag", "", "stable release tag")
	flags.StringVar(&input.AssetPath, "asset", "", "path to gift-panel-windows-x64.exe")
	flags.StringVar(&input.ChecksumPath, "checksum", "", "path to SHA-256 checksum file")
	flags.StringVar(&input.ChangelogPath, "changelog", "", "path to changelog JSON file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if input.Tag == "" || input.AssetPath == "" || input.ChecksumPath == "" || input.ChangelogPath == "" {
		return fmt.Errorf("--tag, --asset, --checksum, and --changelog are required")
	}
	store, err := newStore()
	if err != nil {
		return errors.New("COS configuration is invalid")
	}
	input.PublishedAt = now().UTC()
	if err := publish.Run(context.Background(), store, input); err != nil {
		return errors.New("publish failed")
	}
	prefix := "releases/" + input.Tag + "/"
	fmt.Fprintln(output, input.Tag)
	for _, key := range []string{prefix + "gift-panel-windows-x64.exe", prefix + "gift-panel-windows-x64.exe.sha256", prefix + "gift-panel-changelog.json", prefix + "release.json", "channels/stable/latest.json"} {
		fmt.Fprintln(output, key)
	}
	return nil
}
