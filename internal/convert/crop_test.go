package convert

import (
	"strings"
	"testing"
)

// Only what's black in every sampled frame is removed: dark frames report a smaller
// picture and must not shrink the crop; one full-frame (IMAX) shot means no crop at all.
func TestUnionCrop(t *testing.T) {
	scope := Crop{W: 3840, H: 1608, X: 0, Y: 276}
	many := func(cs ...Crop) []Crop {
		var out []Crop
		for i := 0; i < cropMinFrames; i++ {
			out = append(out, cs...)
		}
		return out
	}
	if got := unionCrop(many(scope), 3840, 2160); got == nil || *got != scope {
		t.Errorf("a scope film: got %+v, want %+v", got, scope)
	}
	// A dark scene (smaller picture) and a black frame (nonsense) change nothing.
	dark := Crop{W: 2304, H: 1200, X: 1536, Y: 480}
	black := Crop{W: -3838, H: -2158, X: 3840, Y: 2160}
	if got := unionCrop(many(scope, dark, black), 3840, 2160); got == nil || *got != scope {
		t.Errorf("with dark and black frames: got %+v, want %+v", got, scope)
	}
	// One IMAX shot among the samples: the film keeps its full height.
	imax := append(many(scope), Crop{W: 3840, H: 2160})
	if got := unionCrop(imax, 3840, 2160); got != nil {
		t.Errorf("a film that opens to the full frame must not be cropped, got %+v", got)
	}
	// Under 1% of an axis isn't worth an odd size.
	if got := unionCrop(many(Crop{W: 3840, H: 2148, X: 0, Y: 6}), 3840, 2160); got != nil {
		t.Errorf("12 rows of black: got %+v, want no crop", got)
	}
	// Edges land on even pixels (4:2:0 chroma).
	if got := unionCrop(many(Crop{W: 1919, H: 803, X: 1, Y: 139}), 1920, 1080); got == nil || got.X%2 != 0 || got.Y%2 != 0 || got.W%2 != 0 || got.H%2 != 0 {
		t.Errorf("odd detection: got %+v, want even edges", got)
	}
	// Too few usable frames to judge: leave it.
	if got := unionCrop([]Crop{scope, scope}, 3840, 2160); got != nil {
		t.Errorf("two frames is no evidence, got %+v", got)
	}
}

// A film whose picture changes shape is never cropped, whichever shape most samples show;
// jitter and dark scenes inside the usual frame are not a change of shape.
func TestCropNeverCutsAShapeChange(t *testing.T) {
	scope := Crop{W: 3840, H: 1608, X: 0, Y: 276}  // 2.39:1 in a 16:9 frame
	flat := Crop{W: 3840, H: 2160, X: 0, Y: 0}     // 1.78:1 — the full frame
	open := Crop{W: 3840, H: 2020, X: 0, Y: 70}    // a 1.90:1 IMAX shot
	dark := Crop{W: 3000, H: 1300, X: 420, Y: 430} // a night scene inside the scope frame
	n := func(c Crop, k int) []Crop {
		out := make([]Crop, k)
		for i := range out {
			out[i] = c
		}
		return out
	}
	join := func(parts ...[]Crop) []Crop {
		var out []Crop
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	for name, frames := range map[string][]Crop{
		"mostly scope, some flat":        join(n(scope, 200), n(flat, 20)),
		"mostly flat, some scope":        join(n(flat, 200), n(scope, 20)),
		"one IMAX shot among 288":        join(n(scope, 287), n(open, 1)),
		"no shape most samples agree on": join(n(scope, 20), n(dark, 30)),
	} {
		if got := unionCrop(frames, 3840, 2160); got != nil {
			t.Errorf("%s: cropped to %+v, want the full frame", name, got)
		}
	}
	// Uniform scope with the detector's jitter of a few rows, plus dark scenes: cropped to
	// the union of the samples.
	jitter := Crop{W: 3840, H: 1612, X: 0, Y: 274}
	got := unionCrop(join(n(scope, 200), n(jitter, 40), n(dark, 40)), 3840, 2160)
	if want := (Crop{W: 3840, H: 1612, X: 0, Y: 274}); got == nil || *got != want {
		t.Errorf("uniform scope: got %+v, want %+v", got, want)
	}
}

// About one sample every 25 seconds, never fewer than 40 or more than 400.
func TestCropSampleCount(t *testing.T) {
	for dur, want := range map[float64]int{7200: 288, 1800: 72, 60: 40, 6 * 3600: 400} {
		if got := cropSampleCount(dur); got != want {
			t.Errorf("%.0fs: %d samples, want %d", dur, got, want)
		}
	}
}

// The crop reaches every encode path that takes system-memory frames, after deinterlacing.
func TestCropInFilters(t *testing.T) {
	mi := film("h264", 1920, 1080, 30000, aud("ac3", "eng", 6))
	mi.Interlaced = true
	plan := Plan{VideoCodec: "hevc", Quality: 18, Crop: &Crop{W: 1920, H: 800, X: 0, Y: 140}}
	if got, want := swFilterChain(mi, plan), deintFilter+",crop=1920:800:0:140"; got != want {
		t.Errorf("filter chain = %q, want %q", got, want)
	}
	args := strings.Join(compileOutputArgs(cpuEncoder("hevc"), mi, plan, false, 4, false), " ")
	if !strings.Contains(args, "crop=1920:800:0:140") {
		t.Errorf("CPU encode lacks the crop: %s", args)
	}
	vaapi := strings.Join(videoArgs(Encoder{Name: "hevc_vaapi", Kind: "vaapi"}, mi, plan, false, 4, false), " ")
	if !strings.Contains(vaapi, "crop=1920:800:0:140,format=") {
		t.Errorf("VAAPI upload chain lacks the crop before the upload: %s", vaapi)
	}
}

// Image subtitles are drawn on the full frame's canvas, so a file that keeps them keeps its
// bars.
func TestKeepsImageSubs(t *testing.T) {
	mi := withSubs(film("hevc", 3840, 2160, 70000), srt("eng"))
	if keepsImageSubs(mi, Plan{}) {
		t.Error("text-only subtitles shouldn't block a crop")
	}
	mi.Subs = append(mi.Subs, SubStream{SubIndex: 1, Codec: "hdmv_pgs_subtitle", Lang: "eng"})
	mi.SubTracks = 2
	if !keepsImageSubs(mi, Plan{Subs: SubPlan{ImageSubs: ImageSubsKeep}}) {
		t.Error("a kept PGS track must block the crop")
	}
	if keepsImageSubs(mi, Plan{Subs: SubPlan{ImageSubs: ImageSubsRemove}}) {
		t.Error("PGS being removed anyway shouldn't block the crop")
	}
}
