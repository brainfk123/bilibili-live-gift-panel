package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/cosstore"
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/publish"
)

func main() {
	input := publish.Input{}
	flag.StringVar(&input.Tag, "tag", "", "stable release tag")
	flag.StringVar(&input.AssetPath, "asset", "", "path to gift-panel-windows-x64.exe")
	flag.StringVar(&input.ChecksumPath, "checksum", "", "path to SHA-256 checksum file")
	flag.StringVar(&input.ChangelogPath, "changelog", "", "path to changelog JSON file")
	flag.Parse()
	if input.Tag == "" || input.AssetPath == "" || input.ChecksumPath == "" || input.ChangelogPath == "" {
		log.Print("--tag, --asset, --checksum, and --changelog are required")
		os.Exit(2)
	}
	store, err := cosstore.New(os.Getenv("COS_BUCKET"), os.Getenv("COS_REGION"), os.Getenv("COS_SECRET_ID"), os.Getenv("COS_SECRET_KEY"), nil)
	if err != nil {
		log.Print("COS configuration is invalid")
		os.Exit(1)
	}
	input.PublishedAt = time.Now().UTC()
	if err := publish.Run(context.Background(), store, input); err != nil {
		log.Print("publish failed")
		os.Exit(1)
	}
	prefix := "releases/" + input.Tag + "/"
	fmt.Println(input.Tag)
	for _, key := range []string{prefix + "gift-panel-windows-x64.exe", prefix + "gift-panel-windows-x64.exe.sha256", prefix + "gift-panel-changelog.json", prefix + "release.json", "channels/stable/latest.json"} {
		fmt.Println(strings.TrimSpace(key))
	}
}
