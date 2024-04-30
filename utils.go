package jwkset

func EqualSliceKEYOPS(s1, s2 []KEYOPS) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i := range s1 {
		if s1[i] != s2[i] {
			return false
		}
	}
	return true
}

func CloneSliceKEYOPS(s []KEYOPS) []KEYOPS {
	// Preserve nil in case it matters.
	if s == nil {
		return nil
	}
	return append([]KEYOPS{}, s...)
}

func CloneSliceOtherPrimes(s []OtherPrimes) []OtherPrimes {
	// Preserve nil in case it matters.
	if s == nil {
		return nil
	}
	return append([]OtherPrimes{}, s...)
}

func CloneSliceString(s []string) []string {
	// Preserve nil in case it matters.
	if s == nil {
		return nil
	}
	return append([]string{}, s...)
}

func CloneSliceJWK(s []JWK) []JWK {
	// Preserve nil in case it matters.
	if s == nil {
		return nil
	}
	return append([]JWK{}, s...)
}
