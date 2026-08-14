package events

import (
	"errors"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	cursor, err := ParseCursor("42")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cursor.String(), "42"; got != want {
		t.Fatalf("cursor: got %q, want %q", got, want)
	}
}

func TestParseCursorRejectsNegativeAndMalformedValues(t *testing.T) {
	for _, value := range []string{"-1", "one", "1.2"} {
		t.Run(value, func(t *testing.T) {
			_, err := ParseCursor(value)
			if !errors.Is(err, ErrInvalidCursor) {
				t.Fatalf("error: got %v, want %v", err, ErrInvalidCursor)
			}
		})
	}
}

func TestNormalizePageSize(t *testing.T) {
	for _, test := range []struct {
		requested int
		want      int
	}{
		{requested: 0, want: DefaultPageSize},
		{requested: 12, want: 12},
		{requested: MaximumPageSize + 1, want: MaximumPageSize},
	} {
		if got := NormalizePageSize(test.requested); got != test.want {
			t.Fatalf("page size for %d: got %d, want %d", test.requested, got, test.want)
		}
	}
}
