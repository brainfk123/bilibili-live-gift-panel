package runtime

import (
	"sort"
	"sync"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

// ViewerRow is process-local display data. It is intentionally not part of
// any repository command or durable runtime state.
type ViewerRow struct {
	UID      int64   `json:"uid"`
	Name     string  `json:"name"`
	Avatar   string  `json:"avatar"`
	Gifts    int     `json:"gifts"`
	GiftCoin float64 `json:"giftCoin"`
}

type ViewerLedger struct {
	mu   sync.RWMutex
	rows map[int64]ViewerRow
}

func NewViewerLedger() *ViewerLedger {
	return &ViewerLedger{rows: make(map[int64]ViewerRow)}
}

func (ledger *ViewerLedger) Record(viewer roomsource.Viewer, gift gameplay.Gift) {
	if ledger == nil || viewer.UID <= 0 || gift.Count <= 0 {
		return
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	row := ledger.rows[viewer.UID]
	row.UID = viewer.UID
	row.Name = viewer.Uname
	row.Avatar = viewer.Avatar
	row.Gifts += gift.Count
	row.GiftCoin += gift.Price * float64(gift.Count)
	ledger.rows[viewer.UID] = row
}

func (ledger *ViewerLedger) Rows() []ViewerRow {
	if ledger == nil {
		return nil
	}
	ledger.mu.RLock()
	defer ledger.mu.RUnlock()
	rows := make([]ViewerRow, 0, len(ledger.rows))
	for _, row := range ledger.rows {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(left, right int) bool { return rows[left].UID < rows[right].UID })
	return rows
}

func (ledger *ViewerLedger) Clear() {
	if ledger == nil {
		return
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	for uid := range ledger.rows {
		ledger.rows[uid] = ViewerRow{}
		delete(ledger.rows, uid)
	}
}
