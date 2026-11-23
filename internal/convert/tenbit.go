package convert

import "context"

// 10-bit output for 8-bit SDR sources, as a stated choice. CPU encodes used to write every
// file as 10-bit (Main10): the extra precision all but removes the banding on skies, smoke
// and dark gradients that is the first thing anyone notices in a re-encode. But an 8-bit
// H.264 film written as HEVC Main10 can turn a direct play into a transcode on older Rokus,
// browsers and 1080p sticks — the devices panel shows which players that is. So it's the
// owner's call:
//
//   - "always": every re-encode is 10-bit (the old behaviour; kept for an install that was
//     already converting, unless changed).
//   - "match_source": an 8-bit SDR source stays 8-bit (x265 Main / 8-bit SVT-AV1); 10-bit
//     and HDR sources are always 10-bit. The default for new installs.
//
// Hardware encodes follow the same choice when the GPU proved at startup that it can write
// 10-bit (Encoder.TenBit, hwOutputTenBit); one that can't keeps an 8-bit source 8-bit, and
// Settings says so.

// The ten-bit choices (Settings.TenBit).
const (
	TenBitAlways      = "always"
	TenBitMatchSource = "match_source"
)

const keyTenBit = "convert_ten_bit"

// Output pixel formats of a CPU encode.
const (
	pixFmt10 = "yuv420p10le"
	pixFmt8  = "yuv420p"
)

// convertUsedKeys are the settings only an install that has used Convert has saved. A new
// default that would change what an existing install does is keyed to their absence.
var convertUsedKeys = []string{keyAuto, keySweepStart, keyReclaimed, keyAllowAV1, keyUseGPU}

// tenBitChoice reads the setting. The first read on an install without it decides the
// default and saves it, so it never changes under the owner: an install that has already
// used Convert keeps 10-bit, a new one matches the source.
func (s *Service) tenBitChoice(ctx context.Context) string {
	switch v := s.settings.Get(ctx, keyTenBit, ""); v {
	case TenBitAlways, TenBitMatchSource:
		return v
	}
	choice := TenBitMatchSource
	for _, k := range convertUsedKeys {
		if _, used := s.settings.Lookup(k); used {
			choice = TenBitAlways
			break
		}
	}
	if err := s.settings.Set(ctx, keyTenBit, choice); err != nil && s.log != nil {
		s.log.Warn("convert: couldn't save the 10-bit default", "err", err)
	}
	return choice
}

// outputPixFmt is the pixel format a CPU re-encode writes for this file under this plan.
func outputPixFmt(mi *MediaInfo, plan Plan) string {
	if !plan.MatchSourceDepth || mi == nil || mi.TenBit || isHDR(mi.EncodeHDR()) {
		return pixFmt10
	}
	return pixFmt8
}

// withPixFmt sets the -pix_fmt value in an argument list (cpuVideoArgs writes 10-bit).
func withPixFmt(args []string, pixFmt string) []string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-pix_fmt" {
			args[i+1] = pixFmt
		}
	}
	return args
}

// recordPixFmt stores what the conversion wrote in the ledger row: the output's own depth
// when it was probed, else what the plan asked for.
func (s *Service) recordPixFmt(ctx context.Context, rec *jobRecord) {
	if rec == nil || rec.historyID <= 0 || !rec.hasPlan || rec.plan.VideoCodec == "" || s.history == nil || s.history.db == nil {
		return
	}
	pix := outputPixFmt(rec.srcInfo, rec.plan)
	if rec.outInfo != nil && rec.outInfo.VideoCodec != "" {
		pix = pixFmt8
		if rec.outInfo.TenBit {
			pix = pixFmt10
		}
	}
	_, _ = s.history.db.ExecContext(ctx, `UPDATE convert_history SET pix_fmt = ? WHERE id = ?`, pix, rec.historyID)
}
