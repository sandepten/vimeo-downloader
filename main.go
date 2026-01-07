package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Global HTTP client with connection pooling for better performance
var httpClient = &http.Client{
	Timeout: 120 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     100,
		IdleConnTimeout:     90 * time.Second,
	},
}

// Playlist represents the Vimeo playlist.json structure
type Playlist struct {
	ClipID  string   `json:"clip_id"`
	BaseURL string   `json:"base_url"`
	Video   []Stream `json:"video"`
	Audio   []Stream `json:"audio"`
}

// Stream represents a video or audio stream
type Stream struct {
	ID                 string    `json:"id"`
	BaseURL            string    `json:"base_url"`
	Format             string    `json:"format"`
	MimeType           string    `json:"mime_type"`
	Codecs             string    `json:"codecs"`
	Bitrate            int       `json:"bitrate"`
	AvgBitrate         int       `json:"avg_bitrate"`
	Duration           float64   `json:"duration"`
	Framerate          float64   `json:"framerate"`
	Width              int       `json:"width"`
	Height             int       `json:"height"`
	MaxSegmentDuration float64   `json:"max_segment_duration"`
	InitSegment        string    `json:"init_segment"`
	InitSegmentURL     string    `json:"init_segment_url"`
	IndexSegment       string    `json:"index_segment"`
	Segments           []Segment `json:"segments"`
}

// Segment represents a single segment
type Segment struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	URL   string  `json:"url"`
	Size  int     `json:"size"`
}

var defaultHeaders = map[string]string{
	"User-Agent":      "Mozilla/5.0 (X11; Linux x86_64; rv:146.0) Gecko/20100101 Firefox/146.0",
	"Accept":          "*/*",
	"Accept-Language": "en-US,en;q=0.5",
	"Origin":          "https://player.vimeo.com",
	"Referer":         "https://player.vimeo.com/",
	"Sec-Fetch-Dest":  "empty",
	"Sec-Fetch-Mode":  "cors",
	"Sec-Fetch-Site":  "cross-site",
	"DNT":             "1",
}

func main() {
	// Parse command line flags
	playlistURL := flag.String("url", "", "Playlist JSON URL")
	playlistFile := flag.String("file", "", "Local playlist JSON file")
	outputFile := flag.String("o", "output.mp4", "Output filename")
	concurrent := flag.Int("c", 16, "Number of concurrent downloads per stream")
	listOnly := flag.Bool("list", false, "List available streams without downloading")
	videoQuality := flag.String("quality", "best", "Video quality: best, worst, or resolution like 1080, 720, 360")
	flag.Parse()

	if *playlistURL == "" && *playlistFile == "" {
		fmt.Println("Vimeo Downloader")
		fmt.Println("================")
		fmt.Println()
		fmt.Println("Usage:")
		fmt.Println("  vimeo-downloader -url <playlist_url> -o output.mp4")
		fmt.Println("  vimeo-downloader -file playlist.json -url <playlist_url> -o output.mp4")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -url string      Playlist JSON URL from Vimeo")
		fmt.Println("  -file string     Local playlist JSON file (requires -url for base URL)")
		fmt.Println("  -o string        Output filename (default: output.mp4)")
		fmt.Println("  -c int           Number of concurrent downloads per stream (default: 16)")
		fmt.Println("  -quality string  Video quality: best, worst, or resolution (default: best)")
		fmt.Println("  -list            List available streams without downloading")
		fmt.Println()
		fmt.Println("Example:")
		fmt.Println("  vimeo-downloader -url 'https://vod-adaptive-ak.vimeocdn.com/.../playlist.json?...' -o video.mp4")
		os.Exit(0)
	}

	// Load playlist
	var playlist Playlist
	var baseURLPrefix string

	if *playlistFile != "" {
		// Load from local file
		data, err := os.ReadFile(*playlistFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading playlist file: %v\n", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(data, &playlist); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing playlist JSON: %v\n", err)
			os.Exit(1)
		}
		// Need a base URL for local files
		if *playlistURL != "" {
			baseURLPrefix = getBaseURLPrefix(*playlistURL, playlist.BaseURL)
		} else {
			fmt.Fprintln(os.Stderr, "Error: Using local file requires -url to set the base URL prefix")
			os.Exit(1)
		}
	} else {
		// Fetch from URL
		fmt.Println("Fetching playlist...")
		data, err := fetchURL(*playlistURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error fetching playlist: %v\n", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(data, &playlist); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing playlist JSON: %v\n", err)
			os.Exit(1)
		}
		baseURLPrefix = getBaseURLPrefix(*playlistURL, playlist.BaseURL)
	}

	fmt.Printf("Clip ID: %s\n", playlist.ClipID)
	fmt.Printf("Found %d video streams, %d audio streams\n", len(playlist.Video), len(playlist.Audio))

	// Sort video streams by resolution (highest first)
	sort.Slice(playlist.Video, func(i, j int) bool {
		return playlist.Video[i].Width*playlist.Video[i].Height > playlist.Video[j].Width*playlist.Video[j].Height
	})

	// Sort audio streams by bitrate (highest first)
	sort.Slice(playlist.Audio, func(i, j int) bool {
		return playlist.Audio[i].Bitrate > playlist.Audio[j].Bitrate
	})

	// List streams
	fmt.Println("\nVideo streams:")
	for i, v := range playlist.Video {
		fmt.Printf("  [%d] %dx%d, %d kbps, %.1fs, %d segments\n",
			i, v.Width, v.Height, v.Bitrate/1000, v.Duration, len(v.Segments))
	}
	fmt.Println("\nAudio streams:")
	for i, a := range playlist.Audio {
		fmt.Printf("  [%d] %d kbps, %.1fs, %d segments\n",
			i, a.Bitrate/1000, a.Duration, len(a.Segments))
	}

	if *listOnly {
		return
	}

	// Select video stream
	var selectedVideo *Stream
	switch *videoQuality {
	case "best":
		selectedVideo = &playlist.Video[0]
	case "worst":
		selectedVideo = &playlist.Video[len(playlist.Video)-1]
	default:
		// Try to match resolution
		for i := range playlist.Video {
			v := &playlist.Video[i]
			if fmt.Sprintf("%d", v.Height) == *videoQuality ||
				fmt.Sprintf("%dp", v.Height) == *videoQuality {
				selectedVideo = v
				break
			}
		}
		if selectedVideo == nil {
			fmt.Fprintf(os.Stderr, "Quality '%s' not found, using best\n", *videoQuality)
			selectedVideo = &playlist.Video[0]
		}
	}

	// Select best audio
	selectedAudio := &playlist.Audio[0]

	fmt.Printf("\nSelected video: %dx%d @ %d kbps\n", selectedVideo.Width, selectedVideo.Height, selectedVideo.Bitrate/1000)
	fmt.Printf("Selected audio: %d kbps\n", selectedAudio.Bitrate/1000)

	// Create temp directory
	tempDir, err := os.MkdirTemp("", "vimeo-download-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating temp directory: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tempDir)

	videoFile := filepath.Join(tempDir, "video.mp4")
	audioFile := filepath.Join(tempDir, "audio.mp4")

	// Download video and audio streams IN PARALLEL
	fmt.Println("\nDownloading video and audio in parallel...")

	var wg sync.WaitGroup
	var videoErr, audioErr error

	// Progress tracking for both streams
	var videoCompleted, audioCompleted int64
	videoTotal := len(selectedVideo.Segments)
	audioTotal := len(selectedAudio.Segments)

	// Start video download goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		videoErr = downloadStreamSegments(selectedVideo, baseURLPrefix, videoFile, *concurrent, &videoCompleted)
	}()

	// Start audio download goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		audioErr = downloadStreamSegments(selectedAudio, baseURLPrefix, audioFile, *concurrent, &audioCompleted)
	}()

	// Progress reporter goroutine
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				vc := atomic.LoadInt64(&videoCompleted)
				ac := atomic.LoadInt64(&audioCompleted)
				fmt.Printf("\r  Video: %d/%d (%.1f%%) | Audio: %d/%d (%.1f%%)     ",
					vc, videoTotal, float64(vc)/float64(videoTotal)*100,
					ac, audioTotal, float64(ac)/float64(audioTotal)*100)
			}
		}
	}()

	wg.Wait()
	close(done)
	fmt.Println() // New line after progress

	if videoErr != nil {
		fmt.Fprintf(os.Stderr, "Error downloading video: %v\n", videoErr)
		os.Exit(1)
	}
	if audioErr != nil {
		fmt.Fprintf(os.Stderr, "Error downloading audio: %v\n", audioErr)
		os.Exit(1)
	}

	// Mux video and audio with ffmpeg
	fmt.Printf("\nMuxing with ffmpeg to %s...\n", *outputFile)
	err = muxStreams(videoFile, audioFile, *outputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error muxing: %v\n", err)
		os.Exit(1)
	}

	// Get file size
	info, _ := os.Stat(*outputFile)
	fmt.Printf("\nDone! Output saved to: %s (%.2f MB)\n", *outputFile, float64(info.Size())/(1024*1024))
}

func getBaseURLPrefix(playlistURL, relativeBase string) string {
	// Parse the playlist URL
	u, err := url.Parse(playlistURL)
	if err != nil {
		return ""
	}

	// Get the directory of the playlist
	dir := filepath.Dir(u.Path)

	// Apply the relative base URL (e.g., "../../../../../range/prot/")
	parts := strings.Split(relativeBase, "/")
	for _, part := range parts {
		if part == ".." {
			dir = filepath.Dir(dir)
		} else if part != "" && part != "." {
			dir = filepath.Join(dir, part)
		}
	}

	// Reconstruct the full URL
	u.Path = dir + "/"
	u.RawQuery = "" // Remove query params, they'll be in segment URLs
	return u.String()
}

func fetchURL(urlStr string) ([]byte, error) {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}

	for key, value := range defaultHeaders {
		req.Header.Set(key, value)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func downloadStreamSegments(stream *Stream, baseURLPrefix, outputFile string, concurrent int, completedCounter *int64) error {
	// Write init segment first (it's base64 encoded)
	var initData []byte
	if stream.InitSegment != "" {
		var err error
		initData, err = base64.StdEncoding.DecodeString(stream.InitSegment)
		if err != nil {
			return fmt.Errorf("failed to decode init segment: %w", err)
		}
	}

	// Download all segments concurrently and store in memory
	segmentData := make([][]byte, len(stream.Segments))
	sem := make(chan struct{}, concurrent)
	var wg sync.WaitGroup
	var downloadErr error
	var errMutex sync.Mutex

	for i, segment := range stream.Segments {
		wg.Add(1)
		go func(idx int, seg Segment) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			// Construct full URL
			fullURL := baseURLPrefix + seg.URL

			// Download with retry
			var data []byte
			var err error
			for retries := 0; retries < 3; retries++ {
				data, err = downloadToMemory(fullURL)
				if err == nil {
					break
				}
				time.Sleep(time.Duration(retries+1) * 500 * time.Millisecond)
			}

			if err != nil {
				errMutex.Lock()
				if downloadErr == nil {
					downloadErr = fmt.Errorf("segment %d: %w", idx, err)
				}
				errMutex.Unlock()
				return
			}

			segmentData[idx] = data
			atomic.AddInt64(completedCounter, 1)
		}(i, segment)
	}

	wg.Wait()

	if downloadErr != nil {
		return downloadErr
	}

	// Write everything to output file
	out, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write init segment
	if len(initData) > 0 {
		if _, err := out.Write(initData); err != nil {
			return fmt.Errorf("failed to write init segment: %w", err)
		}
	}

	// Write all segments in order
	for _, data := range segmentData {
		if _, err := out.Write(data); err != nil {
			return fmt.Errorf("failed to write segment: %w", err)
		}
	}

	return nil
}

func downloadToMemory(urlStr string) ([]byte, error) {
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}

	for key, value := range defaultHeaders {
		req.Header.Set(key, value)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func muxStreams(videoFile, audioFile, outputFile string) error {
	cmd := exec.Command("ffmpeg",
		"-i", videoFile,
		"-i", audioFile,
		"-c", "copy",
		"-y",
		outputFile,
	)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
