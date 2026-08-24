package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCrowsnestStream(t *testing.T) {
	cases := []struct {
		name string
		conf string
		want string
	}{
		{
			name: "ustreamer",
			conf: `[cam 1]
mode: ustreamer
device: /dev/video0
port: 8080
`,
			want: "http://127.0.0.1:8080/?action=stream",
		},
		{
			name: "comentario inline",
			conf: `[cam 1]
mode: ustreamer # backend de captura
device: /dev/video0
port: 8080 # HTTP/MJPG stream/snapshot port
`,
			want: "http://127.0.0.1:8080/?action=stream",
		},
		{
			name: "v4l2rtsp con name",
			conf: `[cam 1]
mode: v4l2rtsp
device: /dev/video0
port: 8554
name: webcam
`,
			want: "rtsp://127.0.0.1:8554/webcam",
		},
		{
			name: "v4l2rtsp sin name",
			conf: `[cam cam1]
mode: v4l2rtsp
device: /dev/video0
port: 8554
`,
			want: "rtsp://127.0.0.1:8554/cam1",
		},
		{
			name: "camera-streamer",
			conf: `[cam 1]
mode: camera-streamer
device: /dev/video0
port: 8554
`,
			want: "rtsp://127.0.0.1:8554/stream",
		},
		{
			name: "primera camara en multiples",
			conf: `[cam 1]
mode: ustreamer
port: 8080

[cam 2]
mode: v4l2rtsp
port: 8554
`,
			want: "http://127.0.0.1:8080/?action=stream",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "crowsnest.conf")
			if err := os.WriteFile(p, []byte(c.conf), 0o600); err != nil {
				t.Fatal(err)
			}
			got, _, err := crowsnestStream(p)
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}

	t.Run("sin camaras", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "crowsnest.conf")
		_ = os.WriteFile(p, []byte("# vacío\n"), 0o600)
		if _, _, err := crowsnestStream(p); err == nil {
			t.Error("esperaba error sin sección [cam]")
		}
	})
}

func TestGo2rtcStreamUrl(t *testing.T) {
	cases := []struct {
		url       string
		transcode bool
		want      string
	}{
		{"http://127.0.0.1:8080/?action=stream", true, "ffmpeg:http://127.0.0.1:8080/?action=stream#video=h264"},
		{"http://127.0.0.1:8080/?action=stream", false, "http://127.0.0.1:8080/?action=stream"},
		{"rtsp://127.0.0.1:8554/webcam", false, "rtsp://127.0.0.1:8554/webcam"},
	}
	for _, c := range cases {
		if got := go2rtcStreamUrl(c.url, c.transcode); got != c.want {
			t.Errorf("go2rtcStreamUrl(%q, %v) = %q, want %q", c.url, c.transcode, got, c.want)
		}
	}
}
