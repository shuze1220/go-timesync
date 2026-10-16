package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HTTPResult struct {
	Server string
	Offset float64
	RTT    float64
	DT     time.Time
}

// queryHTTP fetches the URL's Date header and returns the time offset
// (server - local + RTT/2). RTT is half-corrected for one-way transmission.
func queryHTTP(server string, timeoutSec float64, retries int) (HTTPResult, error) {
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		res, err := queryHTTPOnce(server, timeoutSec)
		if err == nil {
			return res, nil
		}
		lastErr = err
	}
	return HTTPResult{}, lastErr
}

func queryHTTPOnce(server string, timeoutSec float64) (HTTPResult, error) {
	// Normalize: if no scheme, add https://
	u := server
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return HTTPResult{}, fmt.Errorf("URL 解析失败: %w", err)
	}
	host := parsed.Host
	if host == "" {
		return HTTPResult{}, errors.New("URL 无 host")
	}
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	// Force IP to avoid DNS caching affecting measurements? Keep DNS for normal use.

	dialer := &net.Dialer{Timeout: time.Duration(timeoutSec * float64(time.Second))}
	tr := &http.Transport{
		DialContext:     dialer.DialContext,
		TLSClientConfig: &tls.Config{ServerName: parsed.Hostname()},
		// Limit response body: we only need headers
		ResponseHeaderTimeout: time.Duration(timeoutSec * float64(time.Second)),
		DisableKeepAlives:     true,
	}
	client := &http.Client{
		Timeout:   time.Duration(timeoutSec * float64(time.Second)),
		Transport: tr,
	}
	// We don't want to follow redirects (avoid extra DNS/handshake skewing RTT)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	t0 := time.Now()
	req, err := http.NewRequest("HEAD", u, nil)
	if err != nil {
		return HTTPResult{}, err
	}
	req.Header.Set("User-Agent", "time_sync/1.0")
	resp, err := client.Do(req)
	t1 := time.Now()
	if err != nil {
		// Some servers reject HEAD; fall back to GET with body discard.
		req, _ = http.NewRequest("GET", u, nil)
		req.Header.Set("User-Agent", "time_sync/1.0")
		t0 = time.Now()
		resp, err = client.Do(req)
		t1 = time.Now()
		if err != nil {
			return HTTPResult{}, err
		}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	dateStr := resp.Header.Get("Date")
	if dateStr == "" {
		return HTTPResult{}, errors.New("响应无 Date 头")
	}
	dt, err := http.ParseTime(dateStr)
	if err != nil {
		return HTTPResult{}, fmt.Errorf("Date 头解析失败: %w", err)
	}
	rtt := float64(t1.Sub(t0)) / float64(time.Second)
	half := rtt / 2
	// server - local + RTT/2
	localUTC := time.Now().UTC()
	offset := dt.Sub(localUTC).Seconds() + half
	return HTTPResult{
		Server: server,
		Offset: offset,
		RTT:    rtt,
		DT:     dt.UTC(),
	}, nil
}
