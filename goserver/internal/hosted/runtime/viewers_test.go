package runtime

import (
	"testing"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

func TestViewerLedgerKeepsIdentityOnlyInDetachedDisplayRows(t *testing.T) {
	ledger := NewViewerLedger()
	ledger.Record(roomsource.Viewer{UID: 123, Uname: "secret", Avatar: "https://secret", Ephemeral: true}, gameplay.Gift{GiftID: 1, Count: 2, Price: 1000})

	rows := ledger.Rows()
	if len(rows) != 1 || rows[0].UID != 123 || rows[0].Name != "secret" || rows[0].Avatar != "https://secret" || rows[0].Gifts != 2 || rows[0].GiftCoin != 2000 {
		t.Fatalf("rows = %#v", rows)
	}
	rows[0].Name = "mutated"
	if got := ledger.Rows()[0].Name; got != "secret" {
		t.Fatalf("Rows aliases ledger: %q", got)
	}
}
