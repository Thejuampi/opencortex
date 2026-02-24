package repos

import "testing"

func TestFTSLiteralQuery(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{in: "", want: ""},
		{in: "   ", want: ""},
		{in: "pr-review", want: `"pr-review"`},
		{in: "release plan", want: `"release" "plan"`},
		{in: `hello "world"`, want: `"hello" """world"""`},
	}
	for _, tc := range cases {
		if got := ftsLiteralQuery(tc.in); got != tc.want {
			t.Fatalf("ftsLiteralQuery(%q) => %q, want %q", tc.in, got, tc.want)
		}
	}
}
