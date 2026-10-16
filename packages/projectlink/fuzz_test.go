package projectlink

import "testing"

func FuzzBase58(f *testing.F) {
	for _, value := range []string{"", "1", "112", "JxF12TrwUP45BMd", "0OIl"} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, input string) {
		decoded, err := DecodeBase58(input)
		if err != nil {
			return
		}
		if encoded := EncodeBase58(decoded); encoded != input {
			t.Fatalf("successful decode was not canonical: %q", encoded)
		}
	})
}
