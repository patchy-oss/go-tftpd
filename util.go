package tftpd

import "fmt"

func toCString(src string) []byte {
	return append([]byte(src), 0x0)
}

func readCString(src []byte) (int, string, error) {
	end := 0
	for ; end < len(src) && src[end] != 0; end++ {
	}

	if end >= len(src) {
		return 0, "", fmt.Errorf("Incorrect c string")
	}

	return end + 1, string(src[:end]), nil
}
