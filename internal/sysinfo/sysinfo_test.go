package sysinfo

import "testing"

const twoCards = `0, GPU-11111111-aaaa-bbbb-cccc-000000000000, NVIDIA A10, 23028, 550.54
1, GPU-22222222-dddd-eeee-ffff-000000000000, NVIDIA L40S, 46068, 550.54
`

func TestParseGPUList(t *testing.T) {
	gpus := parseGPUList(twoCards)
	if len(gpus) != 2 {
		t.Fatalf("gpus = %+v", gpus)
	}
	if g := gpus[1]; g.Index != 1 || g.Name != "NVIDIA L40S" || g.MemoryMB != 46068 ||
		g.UUID != "GPU-22222222-dddd-eeee-ffff-000000000000" || g.DriverVers != "550.54" {
		t.Fatalf("second card = %+v", g)
	}
}

// Review finding: the registered model and total VRAM came from gpus[0] while free VRAM
// was measured on CUDA_VISIBLE_DEVICES[0]; on a multi-GPU host they described two cards.
func TestSelectGPUMatchesTheMeasuredDevice(t *testing.T) {
	gpus := parseGPUList(twoCards)
	cases := []struct {
		visible string
		want    string
		ok      bool
	}{
		{"0", "NVIDIA A10", true},
		{"1", "NVIDIA L40S", true},
		{"GPU-22222222-dddd-eeee-ffff-000000000000", "NVIDIA L40S", true},
		{"gpu-22222222-DDDD-eeee-ffff-000000000000", "NVIDIA L40S", true},
		{"GPU-2222", "NVIDIA L40S", true}, // CUDA accepts a unique UUID prefix
		{"GPU-", "", false},               // ambiguous prefix
		{"7", "", false},
		{"MIG-unknown", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		g, ok := SelectGPU(gpus, c.visible)
		if ok != c.ok || g.Name != c.want {
			t.Errorf("SelectGPU(%q) = %q, %v; want %q, %v", c.visible, g.Name, ok, c.want, c.ok)
		}
	}
}

func TestActiveGPUFollowsCUDAVisibleDevices(t *testing.T) {
	gpus := parseGPUList(twoCards)
	t.Setenv("CUDA_VISIBLE_DEVICES", "1,0")
	if g, ok := ActiveGPU(gpus); !ok || g.Name != "NVIDIA L40S" {
		t.Fatalf("ActiveGPU = %+v, %v; want the L40S (device 1)", g, ok)
	}
	t.Setenv("CUDA_VISIBLE_DEVICES", "")
	if g, ok := ActiveGPU(gpus); !ok || g.Name != "NVIDIA A10" {
		t.Fatalf("ActiveGPU = %+v, %v; want device 0 when unset", g, ok)
	}
}
