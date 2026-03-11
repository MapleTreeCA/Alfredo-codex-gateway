package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

type PCM struct {
	SampleRate int
	Channels   int
	Samples    []int16
}

func ParseWAV(data []byte) (PCM, error) {
	if len(data) < 44 {
		return PCM{}, errors.New("wav payload is too small")
	}
	if string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return PCM{}, errors.New("wav header is invalid")
	}

	var (
		audioFormat   uint16
		channelCount  uint16
		sampleRate    uint32
		bitsPerSample uint16
		pcmData       []byte
	)

	for offset := 12; offset+8 <= len(data); {
		chunkID := string(data[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		chunkStart := offset + 8
		chunkEnd := chunkStart + chunkSize
		if chunkEnd > len(data) {
			return PCM{}, errors.New("wav chunk exceeds payload length")
		}

		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return PCM{}, errors.New("wav fmt chunk is too small")
			}
			audioFormat = binary.LittleEndian.Uint16(data[chunkStart : chunkStart+2])
			channelCount = binary.LittleEndian.Uint16(data[chunkStart+2 : chunkStart+4])
			sampleRate = binary.LittleEndian.Uint32(data[chunkStart+4 : chunkStart+8])
			bitsPerSample = binary.LittleEndian.Uint16(data[chunkStart+14 : chunkStart+16])
		case "data":
			pcmData = append([]byte(nil), data[chunkStart:chunkEnd]...)
		}

		offset = chunkEnd
		if chunkSize%2 == 1 {
			offset++
		}
	}

	if audioFormat != 1 {
		return PCM{}, fmt.Errorf("unsupported wav format %d", audioFormat)
	}
	if channelCount == 0 {
		return PCM{}, errors.New("wav channel count is missing")
	}
	if sampleRate == 0 {
		return PCM{}, errors.New("wav sample rate is missing")
	}
	if bitsPerSample != 16 {
		return PCM{}, fmt.Errorf("unsupported wav bit depth %d", bitsPerSample)
	}
	if len(pcmData) == 0 {
		return PCM{}, errors.New("wav data chunk is missing")
	}
	if len(pcmData)%2 != 0 {
		return PCM{}, errors.New("wav pcm payload has odd byte length")
	}

	samples := make([]int16, len(pcmData)/2)
	if err := binary.Read(bytes.NewReader(pcmData), binary.LittleEndian, &samples); err != nil {
		return PCM{}, fmt.Errorf("read wav pcm failed: %w", err)
	}

	return PCM{
		SampleRate: int(sampleRate),
		Channels:   int(channelCount),
		Samples:    samples,
	}, nil
}

func EncodeWAV(sampleRate int, samples []int16) ([]byte, error) {
	if sampleRate <= 0 {
		return nil, errors.New("sample rate must be > 0")
	}

	dataSize := len(samples) * 2
	buf := &bytes.Buffer{}
	if _, err := buf.WriteString("RIFF"); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint32(36+dataSize)); err != nil {
		return nil, err
	}
	if _, err := buf.WriteString("WAVE"); err != nil {
		return nil, err
	}
	if _, err := buf.WriteString("fmt "); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint32(16)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint16(1)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint16(1)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint32(sampleRate)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint32(sampleRate*2)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint16(2)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint16(16)); err != nil {
		return nil, err
	}
	if _, err := buf.WriteString("data"); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, uint32(dataSize)); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.LittleEndian, samples); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func MixToMono(pcm PCM) PCM {
	if pcm.Channels <= 1 {
		return PCM{
			SampleRate: pcm.SampleRate,
			Channels:   1,
			Samples:    append([]int16(nil), pcm.Samples...),
		}
	}
	frameCount := len(pcm.Samples) / pcm.Channels
	mono := make([]int16, frameCount)
	for i := 0; i < frameCount; i++ {
		total := 0
		for ch := 0; ch < pcm.Channels; ch++ {
			total += int(pcm.Samples[i*pcm.Channels+ch])
		}
		mono[i] = int16(total / pcm.Channels)
	}
	return PCM{
		SampleRate: pcm.SampleRate,
		Channels:   1,
		Samples:    mono,
	}
}

func ResampleLinear(samples []int16, inputRate, outputRate int) []int16 {
	if len(samples) == 0 || inputRate <= 0 || outputRate <= 0 || inputRate == outputRate {
		return append([]int16(nil), samples...)
	}

	outputLength := int(math.Round(float64(len(samples)) * float64(outputRate) / float64(inputRate)))
	if outputLength < 1 {
		outputLength = 1
	}
	resampled := make([]int16, outputLength)
	last := len(samples) - 1

	for i := range resampled {
		sourcePos := float64(i) * float64(inputRate) / float64(outputRate)
		index := int(sourcePos)
		if index >= last {
			resampled[i] = samples[last]
			continue
		}
		frac := sourcePos - float64(index)
		a := float64(samples[index])
		b := float64(samples[index+1])
		resampled[i] = int16(math.Round(a + (b-a)*frac))
	}
	return resampled
}
