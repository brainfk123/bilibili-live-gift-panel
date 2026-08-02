package main

import "encoding/json"

func marshalJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

func unmarshalJSON(data []byte, value any) error {
	return json.Unmarshal(data, value)
}
