package tftpd

import (
	"reflect"
	"testing"
)

var cStringTestData = []struct {
	str     string
	cString []byte
}{
	{"hello world!", []byte{104, 101, 108, 108, 111, 32, 119, 111, 114, 108, 100, 33, 0}},
	{"", []byte{0}},
}

func TestToCString(t *testing.T) {
	for _, v := range cStringTestData {
		converted := toCString(v.str)
		if !reflect.DeepEqual(converted, v.cString) {
			t.Fatalf("C strings are not equal. %v and %v\n", converted, v.cString)
		}
	}
}

func TestReadCString(t *testing.T) {
	for _, v := range cStringTestData {
		n, correctStr, err := readCString(v.cString)
		if err != nil {
			t.Fatalf("Error should be nil, got: %v\n", err)
		}
		if n != len(v.cString) {
			t.Fatalf("Incorrect number of read bytes. Got %v, should be %v\n", n, len(v.cString))
		}
		if correctStr != v.str {
			t.Fatalf("Incorrect reading of C string %v. Got '%v', should be '%v'\n", v.cString, correctStr, v.str)
		}
	}

	incorrect := cStringTestData[0].cString[:len(cStringTestData[0].cString)-1]
	_, _, err := readCString(incorrect)
	if err == nil {
		t.Fatalf("Error shouldn't be nil\n")
	}
}
