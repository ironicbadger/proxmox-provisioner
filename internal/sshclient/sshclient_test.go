package sshclient

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestStreamOutputSplitsCarriageReturnsAndNewlines(t *testing.T) {
	t.Parallel()

	input := "alpha\rbeta\ngamma\rdelta"
	var buffer bytes.Buffer
	var got []StreamLine
	var wg sync.WaitGroup
	errs := make(chan error, 1)

	wg.Add(1)
	streamOutput(strings.NewReader(input), &buffer, func(line StreamLine) {
		got = append(got, line)
	}, &wg, errs)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("unexpected stream error: %v", err)
	}

	if buffer.String() != input {
		t.Fatalf("buffer mismatch: got %q want %q", buffer.String(), input)
	}

	want := []StreamLine{
		{Text: "alpha", Replace: true},
		{Text: "beta", Replace: false},
		{Text: "gamma", Replace: true},
		{Text: "delta", Replace: false},
	}
	if len(got) != len(want) {
		t.Fatalf("event count mismatch: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d mismatch: got %#v want %#v", i, got[i], want[i])
		}
	}
}
