//go:build darwin && cgo

package camera

import (
	"context"
	"testing"
)

func TestParseDevices(t *testing.T) {
	tests := []struct {
		name string
		list string
		want []Device
	}{
		{"nothing", "", nil},
		{"one camera", "0x1\tFaceTime HD Camera\n", []Device{{ID: "0x1", Description: "FaceTime HD Camera"}}},
		{"two cameras", "a\tBuilt-in\nb\tUSB Camera\n", []Device{{ID: "a", Description: "Built-in"}, {ID: "b", Description: "USB Camera"}}},
		{"a camera without a name is called by its id", "abc\t\n", []Device{{ID: "abc", Description: "abc"}}},
		{"lines that are not a camera are left out", "junk\n\tname\nid\tname\n", []Device{{ID: "id", Description: "name"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDevices(tt.list)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("device %d = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestDevicesListsWithoutTurningACameraOn lists the cameras of this Mac, which needs no permission; a Mac without a camera lists
// none.
func TestDevicesListsWithoutTurningACameraOn(t *testing.T) {
	devices, err := Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	for _, d := range devices {
		if d.ID == "" || d.Description == "" {
			t.Errorf("a camera has no id or no name: %+v", d)
		}
	}
	t.Logf("%d cameras", len(devices))
}
