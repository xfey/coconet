package projectlink

import "errors"

const Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var base58Indexes = func() [128]int8 {
	var result [128]int8
	for index := range result {
		result[index] = -1
	}
	for index, character := range []byte(Alphabet) {
		result[character] = int8(index)
	}
	return result
}()

// EncodeBase58 encodes arbitrary bytes with the fixed Coconet Base58 alphabet.
// Each leading zero byte is preserved as a leading '1'.
func EncodeBase58(input []byte) string {
	if len(input) == 0 {
		return ""
	}
	work := append([]byte(nil), input...)
	zeros := 0
	for zeros < len(work) && work[zeros] == 0 {
		zeros++
	}
	encoded := make([]byte, 0, len(work)*138/100+1)
	for start := zeros; start < len(work); {
		var remainder int
		for index := start; index < len(work); index++ {
			value := int(work[index]) + remainder*256
			work[index] = byte(value / 58)
			remainder = value % 58
		}
		encoded = append(encoded, Alphabet[remainder])
		for start < len(work) && work[start] == 0 {
			start++
		}
	}
	for index := 0; index < zeros; index++ {
		encoded = append(encoded, Alphabet[0])
	}
	reverse(encoded)
	return string(encoded)
}

// DecodeBase58 decodes the fixed ASCII alphabet and rejects whitespace,
// Unicode and visually ambiguous characters.
func DecodeBase58(input string) ([]byte, error) {
	if input == "" {
		return []byte{}, nil
	}
	digits := make([]byte, len(input))
	for index := range input {
		character := input[index]
		if character >= byte(len(base58Indexes)) || base58Indexes[character] < 0 {
			return nil, errors.New("invalid Base58 input")
		}
		digits[index] = byte(base58Indexes[character])
	}
	zeros := 0
	for zeros < len(digits) && digits[zeros] == 0 {
		zeros++
	}
	decoded := make([]byte, 0, len(input)*733/1000+1)
	for start := zeros; start < len(digits); {
		var remainder int
		for index := start; index < len(digits); index++ {
			value := int(digits[index]) + remainder*58
			digits[index] = byte(value / 256)
			remainder = value % 256
		}
		decoded = append(decoded, byte(remainder))
		for start < len(digits) && digits[start] == 0 {
			start++
		}
	}
	for index := 0; index < zeros; index++ {
		decoded = append(decoded, 0)
	}
	reverse(decoded)
	return decoded, nil
}

func reverse(value []byte) {
	for left, right := 0, len(value)-1; left < right; left, right = left+1, right-1 {
		value[left], value[right] = value[right], value[left]
	}
}
