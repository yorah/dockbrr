package main

import "testing"

func TestSelfUpdateSwapFlagsRequired(t *testing.T) {
	if err := runSelfUpdateSwap([]string{"--socket", "/x.sock", "--target", "abc"}); err == nil {
		t.Fatal("expected error when --image is missing")
	}
	if err := runSelfUpdateSwap([]string{"--image", "img", "--target", "abc"}); err == nil {
		t.Fatal("expected error when --socket is missing")
	}
	if err := runSelfUpdateSwap([]string{"--socket", "/x.sock", "--image", "img"}); err == nil {
		t.Fatal("expected error when --target is missing")
	}
}
