# Vimeo Downloader

A fast, concurrent video downloader for Vimeo that uses the playlist.json API to download and combine video/audio streams.

## Features

- Downloads video and audio streams in parallel
- Concurrent segment downloads (16 per stream by default)
- Connection pooling for maximum throughput
- Automatic retry on failed segments
- Quality selection (1080p, 720p, etc.)
- Live progress display

## Requirements

- Go 1.21+ (for building)
- ffmpeg (for muxing video and audio)

## Installation

```bash
git clone <repo-url>
cd vimeo-downloader
go build -o vimeo-downloader .
```

## Usage

### 1. Get the playlist.json URL

1. Open browser DevTools (F12)
2. Go to Network tab
3. Play the Vimeo video
4. Filter for `playlist.json`
5. Copy the full URL

### 2. Download the video

```bash
# Download best quality
./vimeo-downloader -url 'https://vod-adaptive-ak.vimeocdn.com/.../playlist.json?...' -o video.mp4

# List available qualities without downloading
./vimeo-downloader -url '...' -list

# Download specific quality (720p)
./vimeo-downloader -url '...' -quality 720 -o video.mp4

# Download lowest quality
./vimeo-downloader -url '...' -quality worst -o video.mp4

# Increase concurrency for faster downloads
./vimeo-downloader -url '...' -c 32 -o video.mp4
```

### Using a local playlist file

If you saved the playlist.json locally:

```bash
./vimeo-downloader -file playlist.json -url 'https://original-playlist-url...' -o video.mp4
```

Note: The `-url` is still required to construct segment URLs.

## Options

| Flag | Description | Default |
|------|-------------|---------|
| `-url` | Playlist JSON URL from Vimeo | required |
| `-file` | Local playlist JSON file | - |
| `-o` | Output filename | output.mp4 |
| `-c` | Concurrent downloads per stream | 16 |
| `-quality` | Video quality: best, worst, or resolution (1080, 720, etc.) | best |
| `-list` | List available streams without downloading | false |

## Example Output

```
Fetching playlist...
Clip ID: cb5b838d-f902-4b45-a87b-9e6fe23d806d
Found 5 video streams, 3 audio streams

Video streams:
  [0] 1852x1080, 1287 kbps, 9031.6s, 1505 segments
  [1] 1234x720, 771 kbps, 9031.6s, 1505 segments
  [2] 926x540, 452 kbps, 9031.6s, 1505 segments
  [3] 618x360, 275 kbps, 9031.6s, 1505 segments
  [4] 412x240, 138 kbps, 9031.6s, 1505 segments

Audio streams:
  [0] 195 kbps, 9031.6s, 1507 segments
  [1] 102 kbps, 9031.6s, 1505 segments
  [2] 69 kbps, 9031.6s, 1505 segments

Selected video: 1852x1080 @ 1287 kbps
Selected audio: 195 kbps

Downloading video and audio in parallel...
  Video: 1505/1505 (100.0%) | Audio: 1507/1507 (100.0%)

Muxing with ffmpeg to video.mp4...

Done! Output saved to: video.mp4 (1420.35 MB)
```

## Notes

- Playlist URLs contain time-limited tokens (`exp=...`), so they expire after some time
- The downloader uses ~16-32 concurrent connections, which maximizes throughput on most networks
- Segments are buffered in memory before writing to disk for speed
