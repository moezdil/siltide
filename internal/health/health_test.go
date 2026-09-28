package health

import (
	"testing"

	"github.com/moezdil/siltide/internal/device"
)

func TestScore(t *testing.T) {
	d := device.New(device.NVIDIA, 0, "x", "", "")
	o := Options{TempWarn: 85}
	if s, n := Score(d, o); s != 50 || len(n) != 1 {
		t.Fatalf("empty device = %d %v", s, n)
	}
	d.Metrics[device.Util] = 10
	d.Metrics[device.Throttle] = device.ThrottleThermal | device.ThrottlePowerCap
	d.Metrics[device.Temp] = 90
	d.Metrics[device.PCIeWidth], d.Metrics[device.PCIeMaxWidth] = 8, 16
	if s, n := Score(d, o); s != 55 || len(n) != 4 {
		t.Fatalf("got %d %v", s, n)
	}
	d.Metrics[device.LinksActive], d.Metrics[device.LinksTotal] = 17, 18
	d.Links = []device.Link{{Errors: 3}}
	if s, _ := Score(d, o); s != 40 {
		t.Fatalf("links: %d", s)
	}
	o.Xids, o.XidWorst = []int{79}, "critical"
	if s, n := Score(d, o); s != 20 || n[0] != "critical Xid in the last 10 minutes: 79" {
		t.Fatalf("xid: %d %v", s, n)
	}
	d.Metrics[device.EccUncorrected] = 3
	d.Metrics[device.Throttle] = device.ThrottleHW | device.ThrottleThermal | device.ThrottlePowerCap
	if s, _ := Score(d, o); s != 0 {
		t.Fatalf("floor at 0, got %d", s)
	}
	if Band(92) != "healthy" || Band(60) != "degraded" || Band(10) != "critical" {
		t.Fatal("bands")
	}
}

// TestPartialTelemetry covers a device (real AMD RX 9060 XT) whose driver
// serves VRAM but returns EBUSY for utilization, temperature and power: the
// score stays 100 but a note explains why the row is otherwise blank.
func TestPartialTelemetry(t *testing.T) {
	d := device.New(device.AMD, 0, "Radeon RX 9060 XT", "", "")
	d.Metrics[device.MemUsed] = 59891712
	d.Metrics[device.MemTotal] = 17095983104
	s, n := Score(d, Options{TempWarn: 85})
	if s != 100 || len(n) != 1 || n[0] != "driver reported memory but not utilization, temperature, or power" {
		t.Fatalf("partial telemetry = %d %v", s, n)
	}
	// A reading in any of the three clears the note.
	d.Metrics[device.Util] = 0
	if _, n := Score(d, Options{TempWarn: 85}); len(n) != 0 {
		t.Fatalf("util present should clear note: %v", n)
	}
}
