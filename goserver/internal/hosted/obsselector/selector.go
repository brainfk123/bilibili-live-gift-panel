// Package obsselector owns the canonical, URL-safe OBS output selector wire format.
package obsselector

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// MaxEncodedLength leaves room for the fixed OBS route and HTTP request-line
// framing inside the production edge's 64 KiB single-buffer budget.
const MaxEncodedLength = 60 << 10

var ErrInvalid = errors.New("obs selector: invalid")

type Selector struct {
	Kind       string   `json:"kind"`
	ID         string   `json:"id"`
	Attributes []string `json:"attributes,omitempty"`
}

func Encode(selector Selector) (string, error) {
	canonical, err := canonicalJSON(selector)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(canonical)
	if len(encoded) == 0 || len(encoded) > MaxEncodedLength {
		return "", ErrInvalid
	}
	return encoded, nil
}

func Decode(encoded string) (Selector, error) {
	if len(encoded) == 0 || len(encoded) > MaxEncodedLength {
		return Selector{}, ErrInvalid
	}
	for _, value := range []byte(encoded) {
		if value != '-' && value != '_' && (value < '0' || value > '9') && (value < 'A' || value > 'Z') && (value < 'a' || value > 'z') {
			return Selector{}, ErrInvalid
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || !utf8.Valid(raw) {
		return Selector{}, ErrInvalid
	}
	var selector Selector
	if json.Unmarshal(raw, &selector) != nil {
		return Selector{}, ErrInvalid
	}
	canonical, err := Encode(selector)
	if err != nil || canonical != encoded {
		return Selector{}, ErrInvalid
	}
	return selector, nil
}

func canonicalJSON(selector Selector) ([]byte, error) {
	if selector.ID == "" || !utf8.ValidString(selector.ID) {
		return nil, ErrInvalid
	}
	switch selector.Kind {
	case "attribute", "gift-target":
		if len(selector.Attributes) != 0 {
			return nil, ErrInvalid
		}
	case "scene":
		if len(selector.Attributes) == 0 {
			return nil, ErrInvalid
		}
		seen := make(map[string]struct{}, len(selector.Attributes))
		for _, id := range selector.Attributes {
			if id == "" || !utf8.ValidString(id) {
				return nil, ErrInvalid
			}
			if _, duplicate := seen[id]; duplicate {
				return nil, ErrInvalid
			}
			seen[id] = struct{}{}
		}
	default:
		return nil, ErrInvalid
	}
	result := append([]byte(`{"kind":`), jsonString(selector.Kind)...)
	result = append(result, []byte(`,"id":`)...)
	result = append(result, jsonString(selector.ID)...)
	if selector.Kind == "scene" {
		result = append(result, []byte(`,"attributes":[`)...)
		for index, id := range selector.Attributes {
			if index > 0 {
				result = append(result, ',')
			}
			result = append(result, jsonString(id)...)
		}
		result = append(result, ']')
	}
	result = append(result, '}')
	return result, nil
}

func jsonString(value string) []byte {
	result := make([]byte, 0, len(value)+2)
	result = append(result, '"')
	const hex = "0123456789abcdef"
	for _, current := range value {
		switch current {
		case '"', '\\':
			result = append(result, '\\', byte(current))
		case '\b':
			result = append(result, '\\', 'b')
		case '\t':
			result = append(result, '\\', 't')
		case '\n':
			result = append(result, '\\', 'n')
		case '\f':
			result = append(result, '\\', 'f')
		case '\r':
			result = append(result, '\\', 'r')
		default:
			if current < 0x20 {
				result = append(result, '\\', 'u', '0', '0', hex[byte(current)>>4], hex[byte(current)&0x0f])
			} else {
				result = utf8.AppendRune(result, current)
			}
		}
	}
	return append(result, '"')
}
