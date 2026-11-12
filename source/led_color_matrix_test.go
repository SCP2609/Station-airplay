package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStockRedMatrixMaintainsWhiteAndPCMIntensity(t *testing.T) {
	dir := t.TempDir()
	data := `{"type":"matrix","matrix":[[0.229906108,-0.0894722889,-0.0280530642],[0.0275730587,5.49507443,0.407274652],[-0.000074085751,0.0153779975,1.00720711]]}`
	if err := os.WriteFile(filepath.Join(dir, "red.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := loadLEDColorMatrix(dir, "red")
	if err != nil {
		t.Fatal(err)
	}
	in := []string{"#FFFFFF", "#505050", "#21424F", "#000000", "invalid"}
	got := m.colors(in)
	r, g, b := parseHex(t, got[0])
	if r > 8 || g != 255 || b < 40 || b > 48 {
		t.Fatalf("white calibration %s", got[0])
	}
	_, g2, _ := parseHex(t, got[1])
	if g2 != 80 {
		t.Fatalf("lost PCM intensity %s", got[1])
	}
	r, g, b = parseHex(t, got[2])
	if r != 0 || g != 79 || b < 14 || b > 18 {
		t.Fatalf("cyan calibration %s", got[2])
	}
	if got[3] != "#000000" || got[4] != "invalid" {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(in, []string{"#FFFFFF", "#505050", "#21424F", "#000000", "invalid"}) {
		t.Fatal("input mutated")
	}
}

func TestMatrixProfileValidation(t *testing.T) {
	if _, err := loadLEDColorMatrix(t.TempDir(), "../../red"); err == nil {
		t.Fatal("unsafe profile accepted")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "red.json"), []byte(`{"type":"matrix","matrix":[[1,2,3]]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadLEDColorMatrix(dir, "red"); err == nil {
		t.Fatal("malformed matrix accepted")
	}
}
