package sonos

import (
	"context"
	"errors"
	"strconv"
	"strings"
)

func (c *Client) GetHouseholdID(ctx context.Context) (string, error) {
	resp, err := c.soapCall(ctx, controlDeviceProperties, urnDeviceProperties, "GetHouseholdID", nil)
	if err != nil {
		return "", err
	}
	hh := strings.TrimSpace(resp["CurrentHouseholdID"])
	if hh == "" {
		return "", errors.New("missing CurrentHouseholdID in response")
	}
	return hh, nil
}

// ZoneInfo holds the response from GetZoneInfo.
type ZoneInfo struct {
	SerialNumber    string `json:"serialNumber"`
	SoftwareVersion string `json:"softwareVersion"`
	IPAddress       string `json:"ipAddress"`
	MACAddress      string `json:"macAddress"`
	HTAudioIn       int    `json:"htAudioIn"`
	// AudioFormat is the human-readable audio format derived from HTAudioIn.
	AudioFormat string `json:"audioFormat,omitempty"`
}

func (c *Client) GetZoneInfo(ctx context.Context) (ZoneInfo, error) {
	resp, err := c.soapCall(ctx, controlDeviceProperties, urnDeviceProperties, "GetZoneInfo", nil)
	if err != nil {
		return ZoneInfo{}, err
	}
	code, _ := strconv.Atoi(resp["HTAudioIn"])
	return ZoneInfo{
		SerialNumber:    resp["SerialNumber"],
		SoftwareVersion: resp["SoftwareVersion"],
		IPAddress:       resp["IPAddress"],
		MACAddress:      resp["MACAddress"],
		HTAudioIn:       code,
		AudioFormat:     AudioInputFormat(code),
	}, nil
}

// audioInputFormats maps HTAudioIn codes to human-readable format names.
// Codes sourced from the SoCo Python library.
var audioInputFormats = map[int]string{
	0:         "",
	2:         "Stereo PCM",
	7:         "Dolby 2.0",
	18:        "Dolby Digital 5.1",
	21:        "No Input",
	22:        "No Audio",
	32:        "DTS",
	59:        "Dolby Atmos (DD+)",
	61:        "Dolby Atmos (TrueHD)",
	63:        "Dolby Atmos (MAT 2.0)",
	33554434:  "PCM 2.0",
	33554454:  "PCM 2.0 (No Audio)",
	33554488:  "Dolby 2.0",
	33554490:  "Dolby Digital Plus 2.0",
	33554492:  "Dolby TrueHD 2.0",
	33554494:  "Dolby Multichannel PCM 2.0",
	84934658:  "Multichannel PCM 5.1",
	84934713:  "Dolby Digital 5.1",
	84934714:  "Dolby Digital Plus 5.1",
	84934716:  "Dolby TrueHD 5.1",
	84934718:  "Dolby Multichannel PCM 5.1",
	84934721:  "DTS 5.1",
	118489090: "Multichannel PCM 7.1",
	118489146: "Dolby Digital Plus 7.1",
}

// AudioInputFormat returns the human-readable audio format for an HTAudioIn code.
func AudioInputFormat(code int) string {
	if f, ok := audioInputFormats[code]; ok {
		return f
	}
	if code == 0 {
		return ""
	}
	return "Unknown (" + strconv.Itoa(code) + ")"
}
