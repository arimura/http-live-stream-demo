package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"sync"

	"gocv.io/x/gocv"
)

var (
	cameraIndex int = 0 // Default camera index
	frameWidth  int = 640
	frameHeight int = 480
	fps         int = 30
)

// MemoryStore holds the HLS segments in memory.
type MemoryStore struct {
	mutex    sync.RWMutex
	segments map[string][]byte
	playlist []byte
}

var store = &MemoryStore{
	segments: make(map[string][]byte),
}

func main() {
	// Open default camera
	capture, err := gocv.OpenVideoCapture(cameraIndex)
	if err != nil {
		log.Fatalf("Error opening video capture device: %v", err)
	}
	defer capture.Close()

	// Set camera properties
	capture.Set(gocv.VideoCaptureFrameWidth, float64(frameWidth))
	capture.Set(gocv.VideoCaptureFrameHeight, float64(frameHeight))
	capture.Set(gocv.VideoCaptureFPS, float64(fps))

	// Start HLS Encoding
	log.Println("Starting FFmpeg for HLS encoding...")
	cmd := exec.Command("ffmpeg",
		"-f", "rawvideo", // input format is raw video frames
		"-pix_fmt", "bgr24", // pixel format for the frames
		"-s", fmt.Sprintf("%dx%d", frameWidth, frameHeight), // frame size
		"-r", fmt.Sprintf("%d", fps), // frame rate
		"-i", "-", // read input from stdin (pipe)
		"-c:v", "libx264", // video codec
		"-preset", "veryfast", // encoding preset
		"-g", fmt.Sprintf("%d", fps*2), // keyframe interval (for segmenting)
		"-sc_threshold", "0",
		"-f", "hls", // output format HLS
		"-hls_list_size", "3", // number of segments in playlist
		"-hls_time", "2", // segment length in seconds
		"-hls_flags", "delete_segments", // remove old segments
		"-method", "PUT", // method used for segments
		"-hls_segment_filename", "http://localhost:8080/segment_%03d.ts",
		"http://localhost:8080/index.m3u8", // M3U8 playlist
	)

	// Set up a pipe for video data to ffmpeg's stdin
	ffmpegIn, err := cmd.StdinPipe()
	if err != nil {
		log.Fatalf("Error creating FFmpeg stdin pipe: %v", err)
	}
	cmd.Stdout = &pipeWriter{onWrite: savePlaylist}
	cmd.Stderr = io.MultiWriter(log.Writer(), &pipeWriter{onWrite: savePlaylist}) // For debugging

	// Start ffmpeg command
	if err := cmd.Start(); err != nil {
		log.Fatalf("Error starting ffmpeg: %v", err)
	}

	// Capture frames and write to ffmpeg's stdin
	go func() {
		defer ffmpegIn.Close()

		img := gocv.NewMat()
		defer img.Close()

		for {
			if ok := capture.Read(&img); !ok {
				log.Println("Error: cannot read frame from camera")
				continue
			}
			if img.Empty() {
				continue
			}

			_, err := ffmpegIn.Write(img.ToBytes())
			if err != nil {
				log.Printf("Error writing frame to ffmpeg: %v", err)
				return
			}
		}
	}()

	// Define HTTP routes
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/index.m3u8", playlistHandler)
	http.HandleFunc("/segment_", segmentHandler)

	// Start the HTTP server
	port := "8080"
	log.Printf("HLS live stream server running at http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// Handler for the root page - returns an HTML page with the video feed
func indexHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	html := `
<html>
  <head>
    <title>Webcam Live Stream (HLS)</title>
  </head>
  <body>
    <h1>Webcam Live Stream (HLS)</h1>
    <video width="640" height="480" controls autoplay>
      <source src="/index.m3u8" type="application/x-mpegURL">
    </video>
  </body>
</html>
`
	w.Write([]byte(html))
}

// Handler for the HLS playlist (index.m3u8)
func playlistHandler(w http.ResponseWriter, r *http.Request) {
	store.mutex.RLock()
	defer store.mutex.RUnlock()

	if len(store.playlist) == 0 {
		http.Error(w, "Playlist not yet available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Write(store.playlist)
}

// Handler for HLS segments (segment_XXX.ts)
func segmentHandler(w http.ResponseWriter, r *http.Request) {
	segmentName := r.URL.Path[1:] // e.g. "segment_001.ts"

	switch r.Method {
	case http.MethodPut:
		// FFmpeg will PUT segment data here
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r.Body); err != nil {
			log.Printf("Error reading segment data: %v", err)
			http.Error(w, "Error reading segment data", http.StatusInternalServerError)
			return
		}
		store.mutex.Lock()
		store.segments[segmentName] = buf.Bytes()
		store.mutex.Unlock()
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("OK"))

	case http.MethodGet:
		// Client requests segment data
		store.mutex.RLock()
		segmentData, ok := store.segments[segmentName]
		store.mutex.RUnlock()
		if !ok {
			http.Error(w, "Segment not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(segmentData)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// Save playlist or logs from ffmpeg
func savePlaylist(p []byte) {
	data := string(p)
	if len(data) > 0 && data[0] == '#' {
		// M3U8 lines start with '#'
		store.mutex.Lock()
		store.playlist = append(store.playlist, p...)
		store.playlist = append(store.playlist, '\n')
		store.mutex.Unlock()
	} else {
		// This might be ffmpeg logs
		log.Println("FFmpeg output: ", data)
	}
}

// pipeWriter is used to capture ffmpeg output
type pipeWriter struct {
	onWrite func([]byte)
}

func (p *pipeWriter) Write(data []byte) (int, error) {
	if p.onWrite != nil {
		p.onWrite(data)
	}
	return len(data), nil
}
