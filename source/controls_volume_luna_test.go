package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// lunaVolumeHost emulates the server-wide volume routes of Luna's gateway.
// The runtime URL handed to a plugin includes /api/runtime/<id>; the volume
// routes themselves sit at the server root.
func lunaVolumeHost(t *testing.T, initial int) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	current := &atomic.Int32{}
	current.Store(int32(initial))
	sets := &atomic.Int32{}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Luna-Token") != "synthetic-token" {
			t.Errorf("volume request without the runtime token: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/volume":
			_ = json.NewEncoder(w).Encode(map[string]int{"percent": int(current.Load())})
		case r.Method == http.MethodPost && r.URL.Path == "/api/volume/set":
			var request struct {
				Percent int `json:"percent"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("set body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sets.Add(1)
			current.Store(int32(request.Percent))
			_ = json.NewEncoder(w).Encode(map[string]int{"percent": request.Percent})
		default:
			t.Errorf("unexpected volume route: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(host.Close)
	return host, current, sets
}

func lunaVolumeAPI(t *testing.T, host *httptest.Server) *yamAPI {
	t.Helper()
	clearRuntimeEnvironment(t)
	t.Setenv("LUNA_API_URL", host.URL+"/api/runtime/fixture")
	t.Setenv("LUNA_API_TOKEN", "synthetic-token")
	api, err := newYAMAPI()
	if err != nil {
		t.Fatal(err)
	}
	return api
}

func TestLunaVolumeReadWriteAndReadback(t *testing.T) {
	host, current, sets := lunaVolumeHost(t, 47)
	api := lunaVolumeAPI(t, host)
	v, err := controlsVolume(context.Background(), api, yamDevice{}, controlsConfig{NativeVolume: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(*lunaControlVolume); !ok {
		t.Fatalf("runtime URL without a dependency selected %T", v)
	}
	if v.Backend() != "Luna /api/volume" {
		t.Fatal("unexpected backend name", v.Backend())
	}
	if err = v.CheckWrite(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := v.GetVolume(context.Background()); err != nil || got != 47 {
		t.Fatalf("get: %v, %v", got, err)
	}
	// There is no startup probe write: the first change comes from the sender.
	if sets.Load() != 0 || current.Load() != 47 {
		t.Fatalf("unexpected startup writes: %d, level %d", sets.Load(), current.Load())
	}
	if got, err := v.SetVolume(context.Background(), 70); err != nil || got != 70 {
		t.Fatalf("set/readback: %v, %v", got, err)
	}
	if current.Load() != 70 || sets.Load() != 1 {
		t.Fatal("write did not reach the station exactly once")
	}
	if _, err = v.SetVolume(context.Background(), 101); err == nil {
		t.Fatal("out-of-range write accepted")
	}
	if sets.Load() != 1 {
		t.Fatal("invalid percent reached the station")
	}
}

func TestLunaVolumeDoesNotHideCoarseReadbackMismatch(t *testing.T) {
	current := 20
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			var request struct {
				Percent int `json:"percent"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("set body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			// Synthetic coarse grid; not a claim about a physical Mini.
			current = (request.Percent / 10) * 10
			_ = json.NewEncoder(w).Encode(map[string]int{"percent": current})
		default:
			_ = json.NewEncoder(w).Encode(map[string]int{"percent": current})
		}
	}))
	defer host.Close()
	v, err := newLunaControlVolume(lunaVolumeAPI(t, host))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.SetVolume(context.Background(), 47); err == nil {
		t.Fatalf("unqualified seven-point mismatch accepted: %v", got)
	}
	if got, err := v.SetVolume(context.Background(), 52); err != nil || got != 50 {
		t.Fatalf("second coarse step rejected or misread: %v, %v", got, err)
	}
}

func TestLunaVolumeRejectsMismatchedReadback(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// A station that ignores the request must not be reported as applied.
			_ = json.NewEncoder(w).Encode(map[string]int{"percent": 70})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"percent": 10})
	}))
	defer host.Close()
	v, err := newLunaControlVolume(lunaVolumeAPI(t, host))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v.SetVolume(context.Background(), 70); err == nil {
		t.Fatal("silently ignored write accepted")
	}
}

func TestLunaVolumeAcceptsAlsaQuantisation(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(w).Encode(map[string]int{"percent": 68})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"percent": 68})
	}))
	defer host.Close()
	v, err := newLunaControlVolume(lunaVolumeAPI(t, host))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.SetVolume(context.Background(), 70); err != nil || got != 68 {
		t.Fatalf("quantised set: %v, %v", got, err)
	}
}

func TestLunaVolumePreflightFailsClosed(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer host.Close()
	v, err := newLunaControlVolume(lunaVolumeAPI(t, host))
	if err != nil {
		t.Fatal(err)
	}
	if err = v.CheckWrite(context.Background()); err == nil {
		t.Fatal("unavailable station passed the preflight")
	}
	if _, err = v.GetVolume(context.Background()); err == nil {
		t.Fatal("unavailable station reported a volume")
	}
}

func TestLunaVolumeRejectsNonInvocationURL(t *testing.T) {
	host, _, _ := lunaVolumeHost(t, 50)
	clearRuntimeEnvironment(t)
	t.Setenv("LUNA_API_URL", host.URL+"/plain")
	t.Setenv("LUNA_API_TOKEN", "synthetic-token")
	api, err := newYAMAPI()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = newLunaControlVolume(api); err == nil {
		t.Fatal("server root without an invocation path accepted")
	}
}

func TestLunaVolumeLive(t *testing.T) {
	if os.Getenv("STATION_AIRPLAY_LIVE_VOLUME") != "1" {
		t.Skip("set STATION_AIRPLAY_LIVE_VOLUME=1 to write to the station volume API")
	}
	api, err := newYAMAPI()
	if err != nil {
		t.Fatal(err)
	}
	ctx, done := context.WithTimeout(context.Background(), 40*time.Second)
	defer done()
	v, err := controlsVolume(ctx, api, yamDevice{}, controlsConfig{NativeVolume: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(*lunaControlVolume); !ok {
		t.Fatalf("live runtime selected %T, want Luna /api/volume", v)
	}
	if err = v.CheckWrite(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := v.GetVolume(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		restore, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := v.SetVolume(restore, int(before)); err != nil {
			t.Errorf("restore %v: %v", before, err)
		}
	}()
	for _, want := range []int{47, 70} {
		got, err := v.SetVolume(ctx, want)
		if err != nil {
			t.Fatalf("set %d: %v", want, err)
		}
		if diff := int(got) - want; diff > 5 || diff < -5 {
			t.Fatalf("set %d applied %v", want, got)
		}
		readback, err := v.GetVolume(ctx)
		if err != nil || readback != got {
			t.Fatalf("readback after %d: %v, %v", want, readback, err)
		}
		t.Logf("set %d -> %.0f (station readback %.0f)", want, got, readback)
	}
}

func TestVolumeBackendConfiguration(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"luna", `{"native_volume":true,"volume_backend":"luna"}`, true},
		{"package", `{"native_volume":true,"volume_backend":"package"}`, true},
		{"luna with dependency", `{"native_volume":true,"volume_backend":"luna","volume_dependency":"station-volume"}`, false},
		{"unknown", `{"native_volume":true,"volume_backend":"native-volume"}`, false},
		{"package needs volume", `{"native_volume":false,"volume_backend":"package"}`, true},
		{"legacy dependency", `{"native_volume":true,"volume_dependency":"station-volume"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := loadControls(path)
			if tc.ok && err != nil {
				t.Fatal(err)
			}
			if !tc.ok && err == nil {
				t.Fatal("invalid backend configuration accepted")
			}
		})
	}
}

// A Luna outage must not silently select a second station-volume owner.
func TestLunaOwnerFailureDoesNotSelectMixer(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer host.Close()
	v, err := controlsVolume(context.Background(), lunaVolumeAPI(t, host), yamDevice{}, controlsConfig{NativeVolume: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(*lunaControlVolume); !ok {
		t.Fatalf("independent volume owner selected: %T", v)
	}
	if _, err := v.SetVolume(context.Background(), 40); err == nil {
		t.Fatal("failed Luna write reported success")
	}
	if _, err := v.GetVolume(context.Background()); err == nil {
		t.Fatal("failed Luna read reported a level")
	}
}

// Only the Mini profile declares directMixer: its firmware lists a Quasar
// volume-manager port that never listens. Any other model that happens to have
// a dead port must keep Luna as the single station-volume owner.
func TestMixerFallbackNeedsTheProfileFlag(t *testing.T) {
	old := volumeManagerListening
	volumeManagerListening = func() bool { return false }
	defer func() { volumeManagerListening = old }()
	host, _, _ := lunaVolumeHost(t, 30)
	defer host.Close()
	v, err := controlsVolume(context.Background(), lunaVolumeAPI(t, host), yamDevice{Model: "yandexstation_2"}, controlsConfig{NativeVolume: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(*lunaControlVolume); !ok {
		t.Fatalf("Station 2 took the Mini's mixer path: %T", v)
	}
}

func TestLunaOwnerMigratesLegacyPackageSettings(t *testing.T) {
	for _, cfg := range []controlsConfig{
		{NativeVolume: true, VolumeBackend: volumeBackendPackage},
		{NativeVolume: true, VolumeDependency: "station-volume"},
		{NativeVolume: true, VolumeBackend: volumeBackendPackage, VolumeDependency: "station-volume"},
	} {
		host, current, sets := lunaVolumeHost(t, 35)
		api := lunaVolumeAPI(t, host)
		v, err := controlsVolume(context.Background(), api, yamDevice{}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := v.(*lunaControlVolume); !ok {
			t.Fatalf("legacy config selected %T on Luna", v)
		}
		if got, err := v.SetVolume(context.Background(), 55); err != nil || got != 55 {
			t.Fatalf("migrated write: %v %v", got, err)
		}
		if current.Load() != 55 || sets.Load() != 1 {
			t.Fatal("migrated change did not reach Luna exactly once")
		}
		host.Close()
	}
}
