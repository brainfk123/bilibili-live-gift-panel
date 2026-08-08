package llamacpp

import "unicode/utf8"

func completeUTF8Prefix(value []byte) (complete []byte, remainder []byte) {
	index := 0
	complete = make([]byte, 0, len(value))
	for index < len(value) {
		if !utf8.FullRune(value[index:]) {
			break
		}
		runeValue, size := utf8.DecodeRune(value[index:])
		if runeValue == utf8.RuneError && size == 1 {
			complete = utf8.AppendRune(complete, utf8.RuneError)
		} else {
			complete = append(complete, value[index:index+size]...)
		}
		index += size
	}
	if index < len(value) {
		remainder = append([]byte(nil), value[index:]...)
	}
	return complete, remainder
}
