package drivers

import "testing"

func TestIsUUIDLike_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"canonical uuid", "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5", true},
		{"lowercase letters", "abcdef01-2345-6789-abcd-ef0123456789", true},
		{"uppercase letters", "ABCDEF01-2345-6789-ABCD-EF0123456789", true},
		{"resource name", "bmw-staging", false},
		{"empty string", "", false},
		{"too short", "f8b6200c-3bba-48a7-8bf1", false},
		{"too long", "f8b6200c-3bba-48a7-8bf1-7a3e3a885eb5-extra", false},
		{"missing first hyphen", "f8b6200c03bba-48a7-8bf1-7a3e3a885eb5", false},
		{"36 chars but no hyphens", "f8b6200c3bba48a78bf17a3e3a885eb5foo1", false},
		{"spaces", "f8b6200c 3bba 48a7 8bf1 7a3e3a885eb51", false},
		{"zeroes uuid", "00000000-0000-0000-0000-000000000000", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isUUIDLike(c.in); got != c.want {
				t.Errorf("isUUIDLike(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
