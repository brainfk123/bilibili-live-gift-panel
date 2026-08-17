package roomsource

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"bilibili-live-gift-panel/internal/hosted/biligateway"
)

func TestEventFromGatewayNormalizesPaidEventsWithoutViewerIdentity(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    *PaidEvent
	}{
		{
			name:    "send gift nested blind gift and captain",
			payload: `{"cmd":"SEND_GIFT","data":{"rnd":"gift-1","giftId":31036,"num":2,"price":1000,"timestamp":1786896000,"uid":987654321,"uname":"secret","blind_gift":{"blind_gift_id":"31037"},"sender_uinfo":{"guard":{"level":3},"medal":{"name":"粉丝牌"}}}}`,
			want:    &PaidEvent{GiftID: 31036, BlindGiftID: 31037, Count: 2, UnitPrice: 1000, GuardLevel: 3, HasFanMedal: true, OccurredAtMillis: 1786896000000},
		},
		{
			name:    "normal gift fan medal",
			payload: `{"cmd":"SEND_GIFT","data":{"rnd":"gift-2","gift_id":1,"num":1,"price":1000,"timestamp":1786896000,"uid":987654321,"uname":"secret","medal_info":{"medal_name":"粉丝牌"}}}`,
			want:    &PaidEvent{GiftID: 1, Count: 1, UnitPrice: 1000, HasFanMedal: true, OccurredAtMillis: 1786896000000},
		},
		{
			name:    "guard captain",
			payload: `{"cmd":"GUARD_BUY","data":{"order_id":"guard-3","guard_level":3,"num":2,"price":198000,"timestamp":1786896000,"uid":987654321,"username":"secret"}}`,
			want:    &PaidEvent{Count: 2, UnitPrice: 198000, GuardLevel: 3, OccurredAtMillis: 1786896000000},
		},
		{
			name:    "guard admiral",
			payload: `{"cmd":"GUARD_BUY","data":{"order_id":"guard-2","guard_level":2,"num":1,"price":1998000,"start_time":1786896000}}`,
			want:    &PaidEvent{Count: 1, UnitPrice: 1998000, GuardLevel: 2, OccurredAtMillis: 1786896000000},
		},
		{
			name:    "guard governor",
			payload: `{"cmd":"GUARD_BUY","data":{"order_id":"guard-1","guard_level":1,"price":19998000,"timestamp":1786896000}}`,
			want:    &PaidEvent{Count: 1, UnitPrice: 19998000, GuardLevel: 1, OccurredAtMillis: 1786896000000},
		},
		{
			name:    "super chat fan medal",
			payload: `{"cmd":"SUPER_CHAT_MESSAGE_JPN","data":{"id":987,"price":50,"start_time":1786896000,"uid":987654321,"user_info":{"uname":"secret","guard_level":0,"medal_info":{"medal_name":"粉丝牌"}}}}`,
			want:    &PaidEvent{Count: 1, UnitPrice: 50000, HasFanMedal: true, OccurredAtMillis: 1786896000000},
		},
		{
			name:    "maximum bounded gift",
			payload: `{"cmd":"SEND_GIFT","data":{"rnd":"gift-max","giftId":1,"num":1000,"price":1000000000000}}`,
			want:    &PaidEvent{GiftID: 1, Count: 1000, UnitPrice: 1000000000000},
		},
		{name: "unknown guard level", payload: `{"cmd":"SEND_GIFT","data":{"rnd":"gift-unknown","giftId":1,"num":1,"price":1000,"guard_level":9}}`},
		{name: "gift count above processing bound", payload: `{"cmd":"SEND_GIFT","data":{"rnd":"gift-too-many","giftId":1,"num":1001,"price":1000}}`},
		{name: "gift price above aggregate bound", payload: `{"cmd":"SEND_GIFT","data":{"rnd":"gift-too-expensive","giftId":1,"num":1,"price":1000000000001}}`},
		{name: "super chat conversion above aggregate bound", payload: `{"cmd":"SUPER_CHAT_MESSAGE","data":{"id":988,"price":1000000001}}`},
		{name: "negative blind gift ID", payload: `{"cmd":"SEND_GIFT","data":{"rnd":"gift-negative-blind","giftId":1,"blind_gift_id":-1,"num":1,"price":1000}}`},
		{name: "negative guard count", payload: `{"cmd":"GUARD_BUY","data":{"order_id":"guard-negative","guard_level":3,"num":-1,"price":198000}}`},
		{name: "unknown command", payload: `{"cmd":"MYSTERY_BUY","data":{"id":1,"price":10}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := eventFromGateway("42", biligateway.Event{Type: "application", Data: []byte(test.payload)})
			if !reflect.DeepEqual(event.Paid, test.want) {
				t.Fatalf("Paid = %#v, want %#v", event.Paid, test.want)
			}
			if string(event.Data) != test.payload {
				t.Fatalf("raw Data changed: %q", event.Data)
			}
			encoded, err := json.Marshal(event.Paid)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "987654321") || strings.Contains(string(encoded), "secret") {
				t.Fatalf("Paid contains viewer identity: %s", encoded)
			}
		})
	}
}

func TestCloneEventDetachesPaidEventValue(t *testing.T) {
	original := Event{Paid: &PaidEvent{GiftID: 1, Count: 1, UnitPrice: 1000}}
	clone := cloneEvent(original)
	clone.Paid.Count = 99
	if original.Paid.Count != 1 {
		t.Fatalf("clone mutation changed original PaidEvent: %#v", original.Paid)
	}
}
