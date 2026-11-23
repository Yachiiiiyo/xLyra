package textmatch

import (
	"math"
	"net/mail"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"
)

// Validators a rule can name. Each one confirms the shape a regular
// expression cannot check, such as a checksum.
const (
	ValidateLuhn     = "luhn"
	ValidateCNIDCard = "cn_id_card"
	ValidateIPv4     = "ipv4"
	ValidateIPv6     = "ipv6"
	ValidateEmail    = "email"
)

// Validate runs the named validator. An empty name accepts everything.
func Validate(name, candidate string) bool {
	switch name {
	case "":
		return true
	case ValidateLuhn:
		return ValidLuhn(candidate)
	case ValidateCNIDCard:
		return ValidCNIDCard(candidate, time.Now())
	case ValidateIPv4:
		addr, err := netip.ParseAddr(candidate)
		return err == nil && addr.Is4()
	case ValidateIPv6:
		addr, err := netip.ParseAddr(candidate)
		return err == nil && addr.Is6()
	case ValidateEmail:
		return ValidEmail(candidate)
	}
	return false
}

// ValidLuhn checks the digits of candidate, which must have 13 to 19 of
// them, with the Luhn mod 10 algorithm. Other characters are ignored.
func ValidLuhn(candidate string) bool {
	digits := make([]int, 0, 19)
	for _, r := range candidate {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

var (
	cnIDWeights = [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	cnIDChecks  = [11]byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}
)

// ValidCNIDCard checks an 18-character resident identity number: a real
// birth date between 1900 and now, and the GB 11643-1999 check character.
func ValidCNIDCard(candidate string, now time.Time) bool {
	if len(candidate) != 18 {
		return false
	}
	sum := 0
	for i := range 17 {
		c := candidate[i]
		if c < '0' || c > '9' {
			return false
		}
		sum += int(c-'0') * cnIDWeights[i]
	}
	last := candidate[17]
	if last == 'x' {
		last = 'X'
	}
	if last != cnIDChecks[sum%11] {
		return false
	}
	birth, err := time.Parse("20060102", candidate[6:14])
	if err != nil {
		return false
	}
	return birth.Year() >= 1900 && !birth.After(now)
}

// ValidEmail accepts a bare address with a dotted domain.
func ValidEmail(candidate string) bool {
	addr, err := mail.ParseAddress(candidate)
	if err != nil || addr.Name != "" || addr.Address != candidate {
		return false
	}
	at := strings.LastIndexByte(candidate, '@')
	if at <= 0 {
		return false
	}
	domain := candidate[at+1:]
	return strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}

// Entropy is the Shannon entropy of the runes of text, in bits per rune.
func Entropy(text string) float64 {
	total := utf8.RuneCountInString(text)
	if total == 0 {
		return 0
	}
	counts := map[rune]int{}
	for _, r := range text {
		counts[r]++
	}
	entropy := 0.0
	for _, count := range counts {
		p := float64(count) / float64(total)
		entropy -= p * math.Log2(p)
	}
	return entropy
}
