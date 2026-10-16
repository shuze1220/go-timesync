package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

const ntpEpochOffset = 2208988800 // seconds between 1900-01-01 and 1970-01-01
const maxPlausibleOffset = 86400 * 365

type NTPResult struct {
	Server string
	Offset float64
	Delay  float64
	DT     time.Time
}

// queryNTP sends a 48-byte NTP request to server:port and returns offset/delay/DT.
// Implements the 4-timestamp algorithm: T1=client send, T4=client recv,
// T2=server recv, T3=server send.
//
//	offset = ((T2 - T1) + (T3 - T4)) / 2
//	delay  = (T4 - T1) - (T3 - T2)
func queryNTP(server string, timeoutSec float64, retries int) (NTPResult, error) {
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		res, err := queryNTPOnce(server, timeoutSec)
		if err == nil {
			return res, nil
		}
		lastErr = err
	}
	return NTPResult{}, lastErr
}

func queryNTPOnce(server string, timeoutSec float64) (NTPResult, error) {
	// Resolve (IPv4/IPv6)
	resolved, err := net.ResolveUDPAddr("udp", server+":123")
	if err != nil {
		return NTPResult{}, fmt.Errorf("DNS 解析失败: %w", err)
	}
	conn, err := net.DialUDP("udp", nil, resolved)
	if err != nil {
		return NTPResult{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Duration(timeoutSec * float64(time.Second))))

	// Build 48-byte client packet: LI=0 VN=4 Mode=3(client)
	pkt := make([]byte, 48)
	pkt[0] = 0x23 // LI 0, VN 4, Mode 3

	T1 := time.Now()
	if _, err := conn.Write(pkt); err != nil {
		return NTPResult{}, err
	}
	resp := make([]byte, 48)
	n, err := conn.Read(resp)
	if err != nil {
		return NTPResult{}, err
	}
	T4 := time.Now()
	if n < 48 {
		return NTPResult{}, errors.New("响应数据过短")
	}
	if err := validateNTPPacket(resp); err != nil {
		return NTPResult{}, err
	}
	T2 := ntpToTime(resp[32:40])
	T3 := ntpToTime(resp[40:48])
	t1 := float64(T1.UnixNano()) / 1e9
	t2 := T2
	t3 := T3
	t4 := float64(T4.UnixNano()) / 1e9
	delay := (t4 - t1) - (t3 - t2)
	// offset corrected: (T2-T1)+(T3-T4) all in seconds
	off := (t2 - t1 + t3 - t4) / 2
	dt := time.Unix(int64(t3), int64((t3-float64(int64(t3)))*1e9)).UTC()
	if dt.IsZero() {
		dt = T3_time(t3)
	}
	// plausibility check: reject |offset| > 1 year
	if absFloat(off) > maxPlausibleOffset {
		return NTPResult{}, fmt.Errorf("偏差过大 %.3f 秒（疑似纪元错乱）", off)
	}
	return NTPResult{
		Server: server,
		Offset: off,
		Delay:  delay,
		DT:     dt,
	}, nil
}

func ntpToTime(b []byte) float64 {
	sec := binary.BigEndian.Uint32(b[0:4])
	frac := binary.BigEndian.Uint32(b[4:8])
	return float64(sec) - ntpEpochOffset + float64(frac)/4294967296.0
}

func T3_time(t float64) time.Time {
	sec := int64(t)
	nsec := int64((t - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC()
}

func validateNTPPacket(p []byte) error {
	vn := (p[0] >> 3) & 0x07
	mode := p[0] & 0x07
	if vn < 3 || vn > 4 {
		return fmt.Errorf("不支持的版本 VN=%d", vn)
	}
	if mode != 4 {
		return fmt.Errorf("非服务器模式(mode=%d)", mode)
	}
	stratum := int(p[1])
	if stratum == 0 {
		code := string(p[3:4])
		return fmt.Errorf("服务器未同步(kiss=%s)", code)
	}
	if stratum >= 16 {
		return fmt.Errorf("无效 stratum=%d", stratum)
	}
	return nil
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
