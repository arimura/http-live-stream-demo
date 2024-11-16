package main

import (
	"image"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"gocv.io/x/gocv"
)

func main() {
	// Open the default camera using device ID 0
	webcam, err := gocv.OpenVideoCapture(0)
	if err != nil {
		log.Fatalf("Error opening webcam: %v", err)
	}
	defer webcam.Close()

	// Read initial frame to retrieve camera properties
	frame := gocv.NewMat()
	if ok := webcam.Read(&frame); !ok || frame.Empty() {
		log.Fatalf("Cannot read frame from webcam. Is the camera accessible?")
	}
	defer frame.Close()

	// Retrieve the camera frame dimensions
	frameWidth := frame.Cols()
	frameHeight := frame.Rows()
	if frameWidth == 0 || frameHeight == 0 {
		log.Fatalf("Invalid frame dimensions: width=%d, height=%d", frameWidth, frameHeight)
	}

	log.Printf("Camera frame dimensions: %dx%d\n", frameWidth, frameHeight)

	// Create a directory for HLS output if it doesn't exist
	hlsDirectory := "hls"
	if err := os.MkdirAll(hlsDirectory, fs.ModePerm); err != nil {
		log.Fatalf("Error creating HLS directory: %v", err)
	}

	// Prepare FFmpeg command to produce an HLS stream
	ffmpegCmd := exec.Command(
		"ffmpeg",
		"-hide_banner",
		"-loglevel", "error", // hide FFmpeg logs, set "info" or remove for debugging
		"-f", "rawvideo",
		"-pix_fmt", "bgr24",
		"-s", formatResolution(frameWidth, frameHeight),
		"-r", "30", // frame rate
		"-i", "pipe:0", // input from stdin
		// Video codec parameters
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "zerolatency",
		"-g", "30", // group of pictures (GOP) size
		// HLS parameters
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "3",
		"-hls_flags", "delete_segments", // optional: deletes old segments
		"-hls_segment_filename", filepath.Join(hlsDirectory, "segment_%03d.ts"),
		filepath.Join(hlsDirectory, "index.m3u8"),
	)

	// Setup FFmpeg stdout and stderr to monitor for errors
	ffmpegStdout, err := ffmpegCmd.StdoutPipe()
	if err != nil {
		log.Fatalf("Error creating StdoutPipe for FFmpeg: %v", err)
	}

	ffmpegStderr, err := ffmpegCmd.StderrPipe()
	if err != nil {
		log.Fatalf("Error creating StderrPipe for FFmpeg: %v", err)
	}

	// Setup FFmpeg stdin
	ffmpegIn, err := ffmpegCmd.StdinPipe()
	if err != nil {
		log.Fatalf("Error getting FFmpeg stdin pipe: %v", err)
	}

	// Start FFmpeg process
	if err := ffmpegCmd.Start(); err != nil {
		log.Fatalf("Error starting FFmpeg command: %v", err)
	}

	// Monitor FFmpeg outputs in goroutines (optional, but useful for debugging)
	go func() {
		for {
			buf := make([]byte, 1024)
			n, err := ffmpegStdout.Read(buf)
			if err != nil {
				break
			}
			log.Printf("FFmpeg stdout: %s", string(buf[:n]))
		}
	}()

	go func() {
		for {
			buf := make([]byte, 1024)
			n, err := ffmpegStderr.Read(buf)
			if err != nil {
				break
			}
			log.Printf("FFmpeg stderr: %s", string(buf[:n]))
		}
	}()

	// Capture frames and send to FFmpeg
	go func() {
		defer ffmpegIn.Close()

		// Re-use the frame Mat for capturing subsequent frames
		for {
			if ok := webcam.Read(&frame); !ok {
				log.Println("Cannot read frame from webcam")
				break
			}
			if frame.Empty() {
				continue
			}

			// Ensure frame dimensions match what we told FFmpeg
			// If not, we can resize or handle dynamically
			if frame.Cols() != frameWidth || frame.Rows() != frameHeight {
				gocv.Resize(frame, &frame, image.Point{X: frameWidth, Y: frameHeight}, 0, 0, gocv.InterpolationLinear)
			}

			// Write frame data to FFmpeg's stdin
			_, err := ffmpegIn.Write(frame.ToBytes())
			if err != nil {
				log.Printf("Error writing frame to FFmpeg: %v", err)
				break
			}

			// Sleep for the required frame interval based on the frame rate
			time.Sleep(time.Second / 30) // 30fps
		}
	}()

	// Serve the HTML page at root "/"
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		htmlContent := `<!DOCTYPE html>
<html>
<head>
    <title>Webcam Stream</title>
    <meta charset="UTF-8">
</head>
<body>
    <h1>Webcam Stream</h1>
    <video id="video" width="640" height="480" controls autoplay src="/hls/index.m3u8" type="application/vnd.apple.mpegurl"></video>
    <p>If the video does not play, your browser might not support HLS natively.</p>
</body>
</html>`
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(htmlContent))
	})

	// Serve the HLS files at "/hls/"
	http.Handle("/hls/", http.StripPrefix("/hls/", http.FileServer(http.Dir(hlsDirectory))))

	log.Println("Starting server on http://localhost:8080 (Press CTRL+C to exit)")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatalf("HTTP server error: %v", err)
	}
}

// formatResolution returns a string representation of the resolution for FFmpeg (e.g., "640x480")
func formatResolution(width, height int) string {
	return strconv.Itoa(width) + "x" + strconv.Itoa(height)
}
