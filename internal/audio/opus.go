package audio

import (
	"errors"
	"fmt"

	"layeh.com/gopus"
)

const (
	defaultOpusBitrate = 24000
	speechMinPeak      = 110
	speechMinAvgAbs    = 25
	speechStrongAvg    = 60
)

func DecodeOpusFrames(frames [][]byte, sampleRate, frameDurationMS int) ([]int16, error) {
	if sampleRate <= 0 {
		return nil, errors.New("sample rate must be > 0")
	}
	if frameDurationMS <= 0 {
		return nil, errors.New("frame duration must be > 0")
	}
	decoder, err := gopus.NewDecoder(sampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("create opus decoder failed: %w", err)
	}
	frameSize := sampleRate * frameDurationMS / 1000
	if frameSize <= 0 {
		return nil, errors.New("invalid opus frame size")
	}

	pcm := make([]int16, 0, len(frames)*frameSize)
	for _, frame := range frames {
		if len(frame) == 0 {
			continue
		}
		decoded, err := decoder.Decode(frame, frameSize, false)
		if err != nil {
			return nil, fmt.Errorf("decode opus frame failed: %w", err)
		}
		pcm = append(pcm, decoded...)
	}
	return pcm, nil
}

func EncodeOpusFrames(sampleRate, frameDurationMS int, pcm []int16) ([][]byte, error) {
	return EncodeOpusFramesWithBitrate(sampleRate, frameDurationMS, defaultOpusBitrate, pcm)
}

func EncodeOpusFramesWithBitrate(sampleRate, frameDurationMS, bitrate int, pcm []int16) ([][]byte, error) {
	if sampleRate <= 0 {
		return nil, errors.New("sample rate must be > 0")
	}
	if frameDurationMS <= 0 {
		return nil, errors.New("frame duration must be > 0")
	}
	if bitrate <= 0 {
		return nil, errors.New("opus bitrate must be > 0")
	}
	frameSize := sampleRate * frameDurationMS / 1000
	if frameSize <= 0 {
		return nil, errors.New("invalid opus frame size")
	}

	encoder, err := gopus.NewEncoder(sampleRate, 1, gopus.Voip)
	if err != nil {
		return nil, fmt.Errorf("create opus encoder failed: %w", err)
	}
	encoder.SetBitrate(bitrate)
	encoder.SetVbr(true)
	totalFrames := (len(pcm) + frameSize - 1) / frameSize
	if totalFrames == 0 {
		return nil, nil
	}

	out := make([][]byte, 0, totalFrames)
	for offset := 0; offset < len(pcm); offset += frameSize {
		frame := make([]int16, frameSize)
		copy(frame, pcm[offset:min(offset+frameSize, len(pcm))])
		packet, err := encoder.Encode(frame, frameSize, 4096)
		if err != nil {
			return nil, fmt.Errorf("encode opus frame failed: %w", err)
		}
		out = append(out, packet)
	}
	return out, nil
}

func PCMHasSpeech(samples []int16) bool {
	if len(samples) == 0 {
		return false
	}

	var sumAbs int64
	peak := 0
	for _, sample := range samples {
		value := int(sample)
		if value < 0 {
			value = -value
		}
		sumAbs += int64(value)
		if value > peak {
			peak = value
		}
	}

	avgAbs := int(sumAbs / int64(len(samples)))
	if avgAbs >= speechStrongAvg {
		return true
	}
	return peak >= speechMinPeak && avgAbs >= speechMinAvgAbs
}
