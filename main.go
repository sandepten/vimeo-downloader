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
	"time"
)

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
	concurrent := flag.Int("c", 8, "Number of concurrent downloads")
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
		fmt.Println("  -c int           Number of concurrent downloads (default: 8)")
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

	// Download video stream
	fmt.Println("\nDownloading video...")
	videoFile := filepath.Join(tempDir, "video.mp4")
	err = downloadStreamSegments(selectedVideo, baseURLPrefix, videoFile, *concurrent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading video: %v\n", err)
		os.Exit(1)
	}

	// Download audio stream
	fmt.Println("\nDownloading audio...")
	audioFile := filepath.Join(tempDir, "audio.mp4")
	err = downloadStreamSegments(selectedAudio, baseURLPrefix, audioFile, *concurrent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error downloading audio: %v\n", err)
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
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return nil, err
	}

	for key, value := range defaultHeaders {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func downloadStreamSegments(stream *Stream, baseURLPrefix, outputFile string, concurrent int) error {
	tempDir := filepath.Dir(outputFile)

	// Create output file
	out, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write init segment first (it's base64 encoded)
	if stream.InitSegment != "" {
		initData, err := base64.StdEncoding.DecodeString(stream.InitSegment)
		if err != nil {
			return fmt.Errorf("failed to decode init segment: %w", err)
		}
		if _, err := out.Write(initData); err != nil {
			return fmt.Errorf("failed to write init segment: %w", err)
		}
		fmt.Printf("  Wrote init segment (%d bytes)\n", len(initData))
	}

	// Download all segments to temp files
	segmentFiles := make([]string, len(stream.Segments))
	sem := make(chan struct{}, concurrent)
	var wg sync.WaitGroup
	var downloadErr error
	var errMutex sync.Mutex

	// Progress tracking
	var completed int64
	var progressMutex sync.Mutex
	total := len(stream.Segments)

	for i, segment := range stream.Segments {
		wg.Add(1)
		go func(idx int, seg Segment) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			segFile := filepath.Join(tempDir, fmt.Sprintf("seg_%05d.tmp", idx))
			segmentFiles[idx] = segFile

			// Construct full URL
			fullURL := baseURLPrefix + seg.URL

			err := downloadToFile(fullURL, segFile)
			if err != nil {
				errMutex.Lock()
				if downloadErr == nil {
					downloadErr = fmt.Errorf("segment %d: %w", idx, err)
				}
				errMutex.Unlock()
				return
			}

			progressMutex.Lock()
			completed++
			fmt.Printf("\r  Progress: %d/%d segments (%.1f%%)", completed, total, float64(completed)/float64(total)*100)
			progressMutex.Unlock()
		}(i, segment)
	}

	wg.Wait()
	fmt.Println()

	if downloadErr != nil {
		return downloadErr
	}

	// Concatenate segments in order
	fmt.Printf("  Concatenating %d segments...\n", len(segmentFiles))
	for _, segFile := range segmentFiles {
		data, err := os.ReadFile(segFile)
		if err != nil {
			return fmt.Errorf("failed to read segment: %w", err)
		}
		if _, err := out.Write(data); err != nil {
			return fmt.Errorf("failed to write segment: %w", err)
		}
		os.Remove(segFile) // Clean up as we go
	}

	return nil
}

func downloadToFile(urlStr, outputFile string) error {
	client := &http.Client{Timeout: 60 * time.Second}

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return err
	}

	for key, value := range defaultHeaders {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
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
