package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGiftInboxPersistsFIFOAcknowledgementsWithoutRawConnectionData(t *testing.T) {
	root := t.TempDir()
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}

	first, err := inbox.Accept("123", "SEND_GIFT", giftEvent{GiftID: 1, GiftName: "first", Num: 1, Timestamp: 1_700_000_001, Rnd: "source-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := inbox.Accept("123", "SEND_GIFT", giftEvent{GiftID: 2, GiftName: "second", Num: 2, Timestamp: 1_700_000_002, Rnd: "source-2"})
	if err != nil {
		t.Fatal(err)
	}
	if first.LocalSequence != 1 || second.LocalSequence != 2 {
		t.Fatalf("accepted sequences = %d, %d; want 1, 2", first.LocalSequence, second.LocalSequence)
	}

	persisted, err := os.ReadFile(filepath.Join(root, "gift-inbox", "pending", "00000000000000000001-"+first.IngestionID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SESSDATA", "authorization", "cookie", "full raw frame marker"} {
		if strings.Contains(string(persisted), secret) {
			t.Fatalf("persisted inbox record contains %q", secret)
		}
	}

	inbox, err = openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	next, ok, err := inbox.Next()
	if err != nil || !ok {
		t.Fatalf("first Next = (%+v, %t, %v), want first record", next, ok, err)
	}
	if next.IngestionID != first.IngestionID || next.LocalSequence != 1 || next.Gift.GiftName != "first" {
		t.Fatalf("first Next record = %+v, want first accepted record", next)
	}
	if err := inbox.Acknowledge(first.IngestionID); err != nil {
		t.Fatal(err)
	}
	if err := inbox.Acknowledge(first.IngestionID); err != nil {
		t.Fatalf("repeated acknowledgement = %v, want nil", err)
	}

	inbox, err = openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	next, ok, err = inbox.Next()
	if err != nil || !ok {
		t.Fatalf("second Next = (%+v, %t, %v), want second record", next, ok, err)
	}
	if next.IngestionID != second.IngestionID || next.LocalSequence != 2 || next.Gift.GiftName != "second" {
		t.Fatalf("second Next record = %+v, want second accepted record", next)
	}
}

func TestGiftInboxLeavesCorruptHeadForNextAndRemovesOnlyOwnTemporaryFiles(t *testing.T) {
	root := t.TempDir()
	pending := filepath.Join(root, "gift-inbox", "pending")
	if err := os.MkdirAll(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	corruptID := strings.Repeat("a", 32)
	corruptPath := filepath.Join(pending, "00000000000000000001-"+corruptID+".json")
	corruptData := []byte(`{not json`)
	if err := os.WriteFile(corruptPath, corruptData, 0o600); err != nil {
		t.Fatal(err)
	}
	laterID := strings.Repeat("b", 32)
	laterData := []byte(`{"schemaVersion":1,"localSequence":2,"ingestionId":"` + laterID + `","roomId":"123","command":"SEND_GIFT","receivedAt":1700000002,"gift":{"GiftName":"later"}}`)
	if err := os.WriteFile(filepath.Join(pending, "00000000000000000002-"+laterID+".json"), laterData, 0o600); err != nil {
		t.Fatal(err)
	}
	ownTemp := filepath.Join(pending, "config-interrupted-write.tmp")
	if err := os.WriteFile(ownTemp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreignTemp := filepath.Join(pending, "unrelated.tmp")
	if err := os.WriteFile(foreignTemp, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatalf("open inbox with corrupt committed record = %v, want success", err)
	}
	if _, err := os.Stat(ownTemp); !os.IsNotExist(err) {
		t.Fatalf("own orphan temp still exists: %v", err)
	}
	if _, err := os.Stat(foreignTemp); err != nil {
		t.Fatalf("unrelated temp was removed: %v", err)
	}
	if _, ok, err := inbox.Next(); err == nil || ok {
		t.Fatalf("Next with corrupt first record = (_, %t, %v), want error", ok, err)
	}
	if data, err := os.ReadFile(corruptPath); err != nil || string(data) != string(corruptData) {
		t.Fatalf("corrupt record changed after Next: data=%q err=%v", data, err)
	}
	accepted, err := inbox.Accept("123", "SEND_GIFT", giftEvent{GiftName: "after corruption"})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.LocalSequence != 3 {
		t.Fatalf("sequence after existing records = %d, want 3", accepted.LocalSequence)
	}
}
