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
	"github.com/brainfk123/bilibili-live-gift-panel/updateapi/internal/release"
)

func main() {
	err := run(os.Args[1:], func() (publish.Store, error) {
		return cosstore.New(os.Getenv("COS_BUCKET"), os.Getenv("COS_REGION"), os.Getenv("COS_SECRET_ID"), os.Getenv("COS_SECRET_KEY"), nil)
	}, os.Stdout)
	if err != nil {
		log.Print(publishFailureMessage(err))
		os.Exit(1)
	}
}

func publishFailureMessage(err error) string {
	if errors.Is(err, publish.ErrPromotionIndeterminate) {
		return "publish failed: channel promotion outcome is indeterminate; verify the selected channel pointer and restore the approved backup if required"
	}
	if summary := cosstore.SafeErrorSummary(err); summary != "" {
		return "publish failed: Tencent COS " + summary
	}
	return "publish failed"
}

func run(args []string, newStore func() (publish.Store, error), output io.Writer) error {
	input := publish.Input{}
	var channelValue string
	var publishedAt string
	flags := flag.NewFlagSet("publish", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&channelValue, "channel", "", "update channel: stable or legacy-rushrush")
	flags.StringVar(&input.Tag, "tag", "", "release tag")
	flags.StringVar(&publishedAt, "published-at", "", "GitHub Release publication timestamp")
	flags.StringVar(&input.AssetPath, "asset", "", "path to gift-panel-windows-x64.exe")
	flags.StringVar(&input.ChecksumPath, "checksum", "", "path to SHA-256 checksum file")
	flags.StringVar(&input.ChangelogPath, "changelog", "", "path to changelog JSON file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if channelValue == "" || input.Tag == "" || publishedAt == "" || input.AssetPath == "" || input.ChecksumPath == "" || input.ChangelogPath == "" {
		return fmt.Errorf("--channel, --tag, --published-at, --asset, --checksum, and --changelog are required")
	}
	channel, pointerKey, err := parsePublicationChannel(channelValue, input.Tag)
	if err != nil {
		return err
	}
	parsedPublishedAt, err := time.Parse(time.RFC3339, publishedAt)
	if err != nil {
		return errors.New("--published-at must use RFC3339")
	}
	input.Channel = channel
	store, err := newStore()
	if err != nil {
		return errors.New("COS configuration is invalid")
	}
	input.PublishedAt = parsedPublishedAt.UTC()
	outcome, err := publish.Publish(context.Background(), store, input)
	if err != nil {
		return fmt.Errorf("publish failed: %w", err)
	}
	prefix := "releases/" + input.Tag + "/"
	fmt.Fprintln(output, input.Tag)
	for _, key := range []string{prefix + "gift-panel-windows-x64.exe", prefix + "gift-panel-windows-x64.exe.sha256", prefix + "gift-panel-changelog.json", prefix + "release.json", pointerKey} {
		fmt.Fprintln(output, key)
	}
	fmt.Fprintln(output, outcome)
	return nil
}

func parsePublicationChannel(value, tag string) (release.Channel, string, error) {
	if _, err := release.ParseStableTag(tag); err != nil {
		return "", "", errors.New("--tag must use canonical vMAJOR.MINOR.PATCH syntax")
	}
	switch release.Channel(value) {
	case release.ChannelStable:
		if tag == "v0.4.11" {
			return "", "", errors.New("stable channel cannot publish the legacy bridge tag")
		}
		return release.ChannelStable, "channels/stable/latest.json", nil
	case release.ChannelLegacyRushRush:
		if tag != "v0.4.11" {
			return "", "", errors.New("legacy channel requires exact tag v0.4.11")
		}
		return release.ChannelLegacyRushRush, "channels/legacy-rushrush/latest.json", nil
	default:
		return "", "", errors.New("--channel must be stable or legacy-rushrush")
	}
}
