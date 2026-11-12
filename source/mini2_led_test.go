package main

import (
	"reflect"
	"testing"
)

func TestSwapRedBlue(t *testing.T) {
	in := []string{"#FF8000", "#000000", "#12AB34", "red", "#abc", ""}
	want := []string{"#0080FF", "#000000", "#34AB12", "red", "#abc", ""}
	if got := swapRedBlue(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("swapRedBlue = %v, want %v", got, want)
	}
}

func TestSwapRedBlueDoesNotAliasInput(t *testing.T) {
	in := []string{"#FF0000"}
	_ = swapRedBlue(in)
	if in[0] != "#FF0000" {
		t.Fatalf("input mutated: %v", in)
	}
}
