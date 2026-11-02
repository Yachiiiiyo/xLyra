package marketproto

import (
	"strconv"
	"strings"
)

// Version is a MAJOR.MINOR.PATCH triple with no prerelease or build suffix.
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion parses a three-part decimal version. Each part is a non-negative
// integer without a leading zero (the value 0 itself is allowed). A '-' or '+'
// suffix is rejected.
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, errCode(CodeBadVersion, "version must be MAJOR.MINOR.PATCH")
	}
	nums := [3]int{}
	for i, part := range parts {
		n, err := parseVersionPart(part)
		if err != nil {
			return Version{}, err
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}, nil
}

func parseVersionPart(s string) (int, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, errCode(CodeBadVersion, "invalid version")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errCode(CodeBadVersion, "invalid version")
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errCode(CodeBadVersion, "invalid version")
	}
	return n, nil
}

// Compare returns -1 when a < b, 0 when equal, and 1 when a > b.
func Compare(a, b Version) int {
	if c := cmpInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	return cmpInt(a.Patch, b.Patch)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
