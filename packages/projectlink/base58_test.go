package projectlink

import (
	"bytes"
	"testing"
)

func TestBase58Vectors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw     []byte
		encoded string
	}{
		{[]byte{}, ""},
		{[]byte{0}, "1"},
		{[]byte{0, 0, 1}, "112"},
		{[]byte("Hello World"), "JxF12TrwUP45BMd"},
		{[]byte{0xff, 0xff, 0xff, 0xff}, "7YXq9G"},
	} {
		if got := EncodeBase58(test.raw); got != test.encoded {
			t.Fatalf("EncodeBase58(%x) = %q, want %q", test.raw, got, test.encoded)
		}
		decoded, err := DecodeBase58(test.encoded)
		if err != nil || !bytes.Equal(decoded, test.raw) {
			t.Fatalf("DecodeBase58(%q) = %x, %v", test.encoded, decoded, err)
		}
	}
}

func TestBase58RejectsInvalidCharacters(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"0", "O", "I", "l", "abc ", "é", "abc\n"} {
		if _, err := DecodeBase58(value); err == nil {
			t.Fatalf("DecodeBase58 accepted %q", value)
		}
	}
}
