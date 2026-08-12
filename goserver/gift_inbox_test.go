package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
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

func TestGiftInboxCoordinatesConcurrentAcceptAcrossHandles(t *testing.T) {
	root := t.TempDir()
	first, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := openGiftInbox(filepath.Join(root, "."))
	if err != nil {
		t.Fatal(err)
	}

	const accepts = 40
	records := make(chan giftInboxRecord, accepts)
	errorsSeen := make(chan error, accepts)
	var group sync.WaitGroup
	for index := 0; index < accepts; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			inbox := first
			if index%2 != 0 {
				inbox = second
			}
			record, err := inbox.Accept("room", "SEND_GIFT", giftEvent{GiftID: index + 1})
			if err != nil {
				errorsSeen <- err
				return
			}
			records <- record
		}(index)
	}
	group.Wait()
	close(records)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatalf("concurrent Accept = %v", err)
	}
	sequences := make([]int, 0, accepts)
	for record := range records {
		sequences = append(sequences, int(record.LocalSequence))
	}
	sort.Ints(sequences)
	for index, sequence := range sequences {
		if sequence != index+1 {
			t.Fatalf("sorted sequence[%d] = %d, want %d; all=%v", index, sequence, index+1, sequences)
		}
	}
}

func TestGiftInboxSharedHandlesCannotExceedRecordCapacity(t *testing.T) {
	root := t.TempDir()
	pending := filepath.Join(root, "gift-inbox", "pending")
	if err := os.MkdirAll(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	for sequence := 1; sequence < maxGiftInboxRecords; sequence++ {
		name := filepath.Join(pending, giftInboxFilename(uint64(sequence), strings.Repeat("a", 32)))
		if err := os.WriteFile(name, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, inbox := range []*giftInbox{first, second} {
		go func(inbox *giftInbox) {
			<-start
			_, err := inbox.Accept("room", "SEND_GIFT", giftEvent{GiftName: "boundary"})
			results <- err
		}(inbox)
	}
	close(start)
	successes := 0
	capacityErrors := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, errGiftInboxCapacity) {
			capacityErrors++
		} else {
			t.Fatalf("Accept error = %v", err)
		}
	}
	if successes != 1 || capacityErrors != 1 {
		t.Fatalf("results = %d success, %d capacity errors; want 1, 1", successes, capacityErrors)
	}
	if health := first.Health(); health.PendingCount != maxGiftInboxRecords || !health.CapacityError {
		t.Fatalf("health = %+v, want full capacity", health)
	}
}

func TestGiftInboxEnforcesExactPersistedByteBoundary(t *testing.T) {
	for _, test := range []struct {
		name        string
		extraByte   int64
		wantAllowed bool
	}{
		{name: "exact boundary allowed", extraByte: 0, wantAllowed: true},
		{name: "one byte over rejected", extraByte: 1, wantAllowed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			inbox, err := openGiftInbox(root)
			if err != nil {
				t.Fatal(err)
			}
			gift := giftEvent{GiftName: "boundary"}
			fixture := giftInboxRecord{SchemaVersion: 1, LocalSequence: 2, IngestionID: strings.Repeat("a", 32), RoomID: "room", Command: "SEND_GIFT", ReceivedAt: 1_800_000_000, Gift: gift}
			encoded, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			acceptedBytes := int64(len(encoded) + 1)
			fillerSize := maxGiftInboxBytes - acceptedBytes + test.extraByte
			filler := filepath.Join(inbox.pendingPath, giftInboxFilename(1, strings.Repeat("b", 32)))
			file, err := os.Create(filler)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(fillerSize); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = inbox.Accept("room", "SEND_GIFT", gift)
			if test.wantAllowed && err != nil {
				t.Fatalf("Accept at exact byte boundary = %v", err)
			}
			if !test.wantAllowed && !errors.Is(err, errGiftInboxCapacity) {
				t.Fatalf("Accept one byte over boundary = %v, want capacity error", err)
			}
			var total int64
			entries, err := os.ReadDir(inbox.pendingPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				info, err := entry.Info()
				if err != nil {
					t.Fatal(err)
				}
				total += info.Size()
			}
			if total > maxGiftInboxBytes {
				t.Fatalf("persisted bytes = %d, exceeds %d", total, maxGiftInboxBytes)
			}
		})
	}
}

func TestGiftInboxSharedHandlesCannotJointlyExceedByteCapacity(t *testing.T) {
	root := t.TempDir()
	first, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	gift := giftEvent{GiftName: "shared byte boundary"}
	fixture := giftInboxRecord{SchemaVersion: 1, LocalSequence: 2, IngestionID: strings.Repeat("a", 32), RoomID: "room", Command: "SEND_GIFT", ReceivedAt: 1_800_000_000, Gift: gift}
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	filler := filepath.Join(first.pendingPath, giftInboxFilename(1, strings.Repeat("b", 32)))
	file, err := os.Create(filler)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxGiftInboxBytes - int64(len(encoded)+1)); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, inbox := range []*giftInbox{first, second} {
		go func(inbox *giftInbox) {
			<-start
			_, err := inbox.Accept("room", "SEND_GIFT", gift)
			results <- err
		}(inbox)
	}
	close(start)
	successes := 0
	capacityErrors := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, errGiftInboxCapacity) {
			capacityErrors++
		} else {
			t.Fatalf("Accept error = %v", err)
		}
	}
	if successes != 1 || capacityErrors != 1 {
		t.Fatalf("results = %d success, %d capacity errors; want 1, 1", successes, capacityErrors)
	}
}

func TestGiftInboxTempCleanupDoesNotGlobSiblingRoots(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "[gift]")
	ownPending := filepath.Join(root, "gift-inbox", "pending")
	siblingPending := filepath.Join(parent, "g", "gift-inbox", "pending")
	for _, directory := range []string{ownPending, siblingPending} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ownTemp := filepath.Join(ownPending, "config-own.tmp")
	siblingSentinel := filepath.Join(siblingPending, "config-sibling.tmp")
	if err := os.WriteFile(ownTemp, []byte("remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(siblingSentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openGiftInbox(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ownTemp); !os.IsNotExist(err) {
		t.Fatalf("own temp still exists: %v", err)
	}
	if data, err := os.ReadFile(siblingSentinel); err != nil || string(data) != "keep" {
		t.Fatalf("sibling sentinel changed: data=%q err=%v", data, err)
	}
}

func TestGiftInboxRejectsCommittedRecordsWithInvalidContract(t *testing.T) {
	validID := strings.Repeat("a", 32)
	otherID := strings.Repeat("b", 32)
	tests := []struct {
		name string
		data string
	}{
		{name: "empty object", data: `{}`},
		{name: "unknown schema", data: `{"schemaVersion":2,"localSequence":1,"ingestionId":"` + validID + `"}`},
		{name: "zero sequence", data: `{"schemaVersion":1,"localSequence":0,"ingestionId":"` + validID + `"}`},
		{name: "invalid record ID", data: `{"schemaVersion":1,"localSequence":1,"ingestionId":"not-hex"}`},
		{name: "mismatched sequence", data: `{"schemaVersion":1,"localSequence":2,"ingestionId":"` + validID + `"}`},
		{name: "mismatched ID", data: `{"schemaVersion":1,"localSequence":1,"ingestionId":"` + otherID + `"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			pending := filepath.Join(root, "gift-inbox", "pending")
			if err := os.MkdirAll(pending, 0o700); err != nil {
				t.Fatal(err)
			}
			head := filepath.Join(pending, giftInboxFilename(1, validID))
			if err := os.WriteFile(head, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			laterID := strings.Repeat("c", 32)
			later := giftInboxRecord{SchemaVersion: 1, LocalSequence: 2, IngestionID: laterID}
			writeGiftInboxFixture(t, filepath.Join(pending, giftInboxFilename(2, laterID)), later)
			inbox, err := openGiftInbox(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok, err := inbox.Next(); err == nil || ok {
				t.Fatalf("Next = (_, %t, %v), want invalid head error", ok, err)
			}
			if err := inbox.Acknowledge(validID); err == nil {
				t.Fatal("Acknowledge tampered head succeeded")
			}
			if _, err := os.Stat(head); err != nil {
				t.Fatalf("invalid committed record removed: %v", err)
			}
		})
	}
}

func TestGiftInboxClaimsHeadForOnlyOneHandleUntilAcknowledged(t *testing.T) {
	root := t.TempDir()
	first, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := first.Accept("room", "SEND_GIFT", giftEvent{GiftName: "once"})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		inbox  *giftInbox
		record giftInboxRecord
		ok     bool
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, inbox := range []*giftInbox{first, second} {
		go func(inbox *giftInbox) {
			<-start
			next, ok, err := inbox.Next()
			results <- result{inbox: inbox, record: next, ok: ok, err: err}
		}(inbox)
	}
	close(start)
	var owner *giftInbox
	claimed := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ok {
			claimed++
			owner = result.inbox
			if result.record.IngestionID != record.IngestionID {
				t.Fatalf("claimed ID = %q, want %q", result.record.IngestionID, record.IngestionID)
			}
		}
	}
	if claimed != 1 {
		t.Fatalf("concurrent Next claims = %d, want 1", claimed)
	}
	if err := owner.Acknowledge(record.IngestionID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := second.Next(); err != nil || ok {
		t.Fatalf("Next after acknowledgement = (_, %t, %v), want empty", ok, err)
	}
}

func TestGiftInboxSameHandleConcurrentNextClaimsHeadOnce(t *testing.T) {
	root := t.TempDir()
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := inbox.Accept("room", "SEND_GIFT", giftEvent{GiftName: "once"}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		ok  bool
		err error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for range 2 {
		go func() {
			<-start
			_, ok, err := inbox.Next()
			results <- result{ok: ok, err: err}
		}()
	}
	close(start)
	claims := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ok {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("same-handle concurrent claims = %d, want 1", claims)
	}
}

func TestGiftInboxReleaseMakesClaimAvailableToAnotherHandle(t *testing.T) {
	root := t.TempDir()
	owner, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	other, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	want, err := owner.Accept("room", "SEND_GIFT", giftEvent{GiftName: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := owner.Next(); err != nil || !ok {
		t.Fatalf("owner Next = (_, %t, %v)", ok, err)
	}
	if err := owner.Release(want.IngestionID); err != nil {
		t.Fatal(err)
	}
	got, ok, err := other.Next()
	if err != nil || !ok {
		t.Fatalf("other Next after Release = (%+v, %t, %v)", got, ok, err)
	}
	if got.IngestionID != want.IngestionID {
		t.Fatalf("released record ID = %q, want %q", got.IngestionID, want.IngestionID)
	}
}

func TestGiftInboxCloseReleasesClaimAndRejectsFurtherOperations(t *testing.T) {
	root := t.TempDir()
	owner, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	other, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	want, err := owner.Accept("room", "SEND_GIFT", giftEvent{GiftName: "abandoned"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := owner.Next(); err != nil || !ok {
		t.Fatalf("owner Next = (_, %t, %v)", ok, err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("repeated Close = %v, want nil", err)
	}
	if _, err := owner.Accept("room", "SEND_GIFT", giftEvent{}); !errors.Is(err, errGiftInboxClosed) {
		t.Fatalf("Accept after Close = %v, want closed error", err)
	}
	if _, _, err := owner.Next(); !errors.Is(err, errGiftInboxClosed) {
		t.Fatalf("Next after Close = %v, want closed error", err)
	}
	if err := owner.Acknowledge(want.IngestionID); !errors.Is(err, errGiftInboxClosed) {
		t.Fatalf("Acknowledge after Close = %v, want closed error", err)
	}
	if err := owner.Release(want.IngestionID); !errors.Is(err, errGiftInboxClosed) {
		t.Fatalf("Release after Close = %v, want closed error", err)
	}
	got, ok, err := other.Next()
	if err != nil || !ok || got.IngestionID != want.IngestionID {
		t.Fatalf("other Next after owner Close = (%+v, %t, %v), want abandoned record", got, ok, err)
	}
}

func TestGiftInboxInvalidReleaseDoesNotFreeClaim(t *testing.T) {
	root := t.TempDir()
	owner, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	other, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := owner.Accept("room", "SEND_GIFT", giftEvent{GiftName: "claimed"})
	if err != nil {
		t.Fatal(err)
	}
	later, err := owner.Accept("room", "SEND_GIFT", giftEvent{GiftName: "later"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := owner.Next(); err != nil || !ok {
		t.Fatalf("owner Next = (_, %t, %v)", ok, err)
	}
	for name, release := range map[string]func() error{
		"other owner":  func() error { return other.Release(claimed.IngestionID) },
		"invalid ID":   func() error { return owner.Release("not-hex") },
		"different ID": func() error { return owner.Release(later.IngestionID) },
	} {
		t.Run(name, func(t *testing.T) {
			if err := release(); err == nil {
				t.Fatal("invalid Release succeeded")
			}
			if _, ok, err := other.Next(); err != nil || ok {
				t.Fatalf("other Next after invalid Release = (_, %t, %v), want unavailable", ok, err)
			}
		})
	}
}

func TestGiftInboxDistinctFilesystemRootsDoNotShareState(t *testing.T) {
	parent := t.TempDir()
	upperRoot := filepath.Join(parent, "GiftRoot")
	lowerRoot := filepath.Join(parent, "giftroot")
	for _, root := range []string{upperRoot, lowerRoot} {
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	upperInfo, err := os.Stat(upperRoot)
	if err != nil {
		t.Fatal(err)
	}
	lowerInfo, err := os.Stat(lowerRoot)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(upperInfo, lowerInfo) {
		t.Skip("filesystem treats case variants as one directory")
	}
	upper, err := openGiftInbox(upperRoot)
	if err != nil {
		t.Fatal(err)
	}
	lower, err := openGiftInbox(lowerRoot)
	if err != nil {
		t.Fatal(err)
	}
	upperRecord, err := upper.Accept("upper", "SEND_GIFT", giftEvent{})
	if err != nil {
		t.Fatal(err)
	}
	lowerRecord, err := lower.Accept("lower", "SEND_GIFT", giftEvent{})
	if err != nil {
		t.Fatal(err)
	}
	if upperRecord.LocalSequence != 1 || lowerRecord.LocalSequence != 1 {
		t.Fatalf("distinct-root sequences = %d, %d; want 1, 1", upperRecord.LocalSequence, lowerRecord.LocalSequence)
	}
}

func TestGiftInboxFilesystemAliasSharesStateWhenSupported(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	alias := filepath.Join(parent, "alias")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("filesystem alias unavailable: %v", err)
	}
	first, err := openGiftInbox(target)
	if err != nil {
		t.Fatal(err)
	}
	second, err := openGiftInbox(alias)
	if err != nil {
		t.Fatal(err)
	}
	one, err := first.Accept("room", "SEND_GIFT", giftEvent{})
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.Accept("room", "SEND_GIFT", giftEvent{})
	if err != nil {
		t.Fatal(err)
	}
	if one.LocalSequence != 1 || two.LocalSequence != 2 {
		t.Fatalf("alias sequences = %d, %d; want 1, 2", one.LocalSequence, two.LocalSequence)
	}
}

func TestGiftInboxRejectsRootIdentityReplacementWhileHandleIsLive(t *testing.T) {
	root := t.TempDir()
	owner, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	oldInboxRoot := filepath.Join(root, "gift-inbox")
	movedInboxRoot := filepath.Join(root, "gift-inbox-moved")
	if err := os.Rename(oldInboxRoot, movedInboxRoot); err != nil {
		t.Skipf("filesystem cannot replace a live inbox root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(oldInboxRoot, "pending"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := openGiftInbox(root); err == nil {
		t.Fatal("openGiftInbox accepted a replaced root while its prior handle remained live")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestGiftInboxReconcilesCapacityAfterDeletionAndReopen(t *testing.T) {
	root := t.TempDir()
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := inbox.Accept("room", "SEND_GIFT", giftEvent{GiftName: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if err := inbox.Acknowledge(first.IngestionID); err != nil {
		t.Fatal(err)
	}
	reopened, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	if health := reopened.Health(); health.PendingCount != 0 || health.CapacityError {
		t.Fatalf("health after deletion and reopen = %+v", health)
	}
	if _, err := reopened.Accept("room", "SEND_GIFT", giftEvent{GiftName: "replacement"}); err != nil {
		t.Fatal(err)
	}
}

func TestGiftInboxUsesGlobalFIFOAcrossRooms(t *testing.T) {
	root := t.TempDir()
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	wantRooms := []string{"room-a", "room-b", "room-a"}
	for _, room := range wantRooms {
		if _, err := inbox.Accept(room, "SEND_GIFT", giftEvent{GiftName: room}); err != nil {
			t.Fatal(err)
		}
	}
	for _, wantRoom := range wantRooms {
		record, ok, err := inbox.Next()
		if err != nil || !ok {
			t.Fatalf("Next = (%+v, %t, %v)", record, ok, err)
		}
		if record.RoomID != wantRoom {
			t.Fatalf("Next room = %q, want %q", record.RoomID, wantRoom)
		}
		if err := inbox.Acknowledge(record.IngestionID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGiftInboxPersistsOnlyNormalizedGiftFields(t *testing.T) {
	type transportEnvelope struct {
		Gift          giftEvent
		SESSDATA      string
		Authorization string
		Cookie        string
		RawFrame      string
	}
	input := transportEnvelope{
		Gift:          giftEvent{GiftName: "normalized"},
		SESSDATA:      "secret-session-marker",
		Authorization: "secret-auth-marker",
		Cookie:        "secret-cookie-marker",
		RawFrame:      "secret-raw-frame-marker",
	}
	root := t.TempDir()
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := inbox.Accept("room", "SEND_GIFT", input.Gift); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(inbox.pendingPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(inbox.pendingPath, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(data))
		for _, marker := range []string{input.SESSDATA, input.Authorization, input.Cookie, input.RawFrame} {
			if strings.Contains(lower, strings.ToLower(marker)) {
				t.Fatalf("persisted normalized record %s contains transport marker %q", entry.Name(), marker)
			}
		}
	}
	wantGiftFields := []string{"AnimationDurationMS", "AnimationGIF", "AnimationOnly", "AnimationWebP", "Avatar", "BlindGiftID", "BlindGiftName", "BlindGiftPrice", "CoinType", "EffectID", "EffectMP4", "EffectMP4JSON", "GiftID", "GiftName", "ImgBasic", "Membership", "Message", "Num", "Price", "Rnd", "Timestamp", "TotalCoin", "UID", "Uname"}
	actualGiftFields := make([]string, 0, reflect.TypeOf(giftEvent{}).NumField())
	for index := 0; index < reflect.TypeOf(giftEvent{}).NumField(); index++ {
		actualGiftFields = append(actualGiftFields, reflect.TypeOf(giftEvent{}).Field(index).Name)
	}
	sort.Strings(actualGiftFields)
	if !reflect.DeepEqual(actualGiftFields, wantGiftFields) {
		t.Fatalf("giftEvent persistence allowlist changed:\n got %v\nwant %v", actualGiftFields, wantGiftFields)
	}
}

func giftInboxFilename(sequence uint64, ingestionID string) string {
	return strings.ReplaceAll(filepath.Base((&giftInbox{pendingPath: "pending"}).recordPath(sequence, ingestionID)), "\\", "")
}

func writeGiftInboxFixture(t *testing.T, path string, record giftInboxRecord) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
