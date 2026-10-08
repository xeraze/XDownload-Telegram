package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type TrackInfo struct {
	Title     string `json:"title"`
	Duration  int    `json:"duration"`
	Uploader  string `json:"uploader"`
	Thumbnail string `json:"thumbnail"`
	URL       string `json:"url"`
	IsSpotify bool   `json:"is_spotify,omitempty"`
}

var spotifyRe = regexp.MustCompile(`(?i)open\.spotify\.com|^spotify:`)
var spotifyTrackRe = regexp.MustCompile(`(?i)(?:open\.spotify\.com/track/|spotify:track:)([a-z0-9]+)`)

func spotifyTrackID(link string) string {
	if m := spotifyTrackRe.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return ""
}

const spotdlBlockWindow = 10 * time.Minute

func spotdlCooldownFile() string {
	return filepath.Join(os.TempDir(), "xdl-spotdl-cooldown")
}

func spotdlBlocked() bool {
	b, err := os.ReadFile(spotdlCooldownFile())
	if err != nil {
		return false
	}
	until, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return false
	}
	return time.Now().Before(time.Unix(until, 0))
}

func markSpotdlBlocked() {
	until := time.Now().Add(spotdlBlockWindow)
	if err := os.WriteFile(spotdlCooldownFile(), []byte(fmt.Sprint(until.Unix())), 0o644); err == nil {
		fmt.Println("spotdl is rate-limited by Spotify, cooling down for", spotdlBlockWindow)
	}
}

var youtubeClientArgs = [][]string{
	{},
	{"--extractor-args", "youtube:player_client=android"},
}

func ytdlpArgs(extra ...string) []string {
	args := []string{
		"--retries", "3",
		"--fragment-retries", "3",
	}
	return append(args, extra...)
}

func isSpotifyURL(url string) bool {
	return spotifyRe.MatchString(url)
}

func fetchInfo(url string) (*TrackInfo, error) {
	if isSpotifyURL(url) {
		return &TrackInfo{Title: "Spotify track", IsSpotify: true}, nil
	}
	cmd := exec.Command("yt-dlp", ytdlpArgs("--dump-single-json", "--no-playlist", "--no-warnings", "--skip-download", url)...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp failed (is it installed?): %w", err)
	}
	var raw struct {
		Title      string `json:"title"`
		Duration   int    `json:"duration"`
		Uploader   string `json:"uploader"`
		Thumbnail  string `json:"thumbnail"`
		WebpageURL string `json:"webpage_url"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	return &TrackInfo{
		Title:     raw.Title,
		Duration:  raw.Duration,
		Uploader:  raw.Uploader,
		Thumbnail: raw.Thumbnail,
		URL:       raw.WebpageURL,
	}, nil
}

type ytMeta struct {
	extractor string
	id        string
	height    string
}

func prettyExtractor(key string) string {
	switch key {
	case "Youtube":
		return "YouTube"
	case "Soundcloud":
		return "SoundCloud"
	}
	return key
}

func serviceFileName(service, kind, id, height, ext string) string {
	if service == "" {
		service = "Media"
	}
	if id == "" || id == "NA" {
		id = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if kind == "video" {
		if height != "" && height != "NA" {
			return fmt.Sprintf("%s_video_%s_%sp.%s", service, id, height, ext)
		}
		return fmt.Sprintf("%s_video_%s.%s", service, id, ext)
	}
	return fmt.Sprintf("%s_audio_%s.%s", service, id, ext)
}

func renameService(path, service, kind, id, height string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("no output file")
	}
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	dst := filepath.Join(filepath.Dir(path), serviceFileName(service, kind, id, height, ext))
	if dst == path {
		return path, nil
	}
	os.Remove(dst)
	if err := os.Rename(path, dst); err != nil {
		return "", fmt.Errorf("rename result: %w", err)
	}
	return dst, nil
}

func downloadAudio(ctx context.Context, url, ext, dir string, onProgress func(float64)) (string, error) {
	args := []string{
		"--newline",
		"--progress",
		"--no-playlist",
		"--extract-audio",
		"--audio-format", ext,
		"--audio-quality", "0",
		"--progress-template", "download:[%(progress._percent_str)s] %(progress._speed_str)s",
		"--print", "after_move:FILE:%(filepath)s",
		"--print", "after_move:META:%(extractor_key)s|%(id)s|%(height)s",
		"-o", filepath.Join(dir, "%(id)s.%(ext)s"),
		url,
	}
	path, meta, err := runYTDLPYouTube(ctx, args, dir, onProgress)
	if err != nil {
		return "", err
	}
	return renameService(path, prettyExtractor(meta.extractor), "audio", meta.id, meta.height)
}

func downloadVideo(ctx context.Context, url, dir string, height int, onProgress func(float64)) (string, error) {
	format := fmt.Sprintf("bestvideo[height<=%d][ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best", height)
	args := []string{
		"--newline",
		"--progress",
		"--no-playlist",
		"-f", format,
		"--merge-output-format", "mp4",
		"--progress-template", "download:[%(progress._percent_str)s] %(progress._speed_str)s",
		"--print", "after_move:FILE:%(filepath)s",
		"--print", "after_move:META:%(extractor_key)s|%(id)s|%(height)s",
		"-o", filepath.Join(dir, "%(id)s.%(ext)s"),
		url,
	}
	path, meta, err := runYTDLPYouTube(ctx, args, dir, onProgress)
	if err != nil {
		return "", err
	}
	return renameService(path, prettyExtractor(meta.extractor), "video", meta.id, meta.height)
}

func runYTDLPYouTube(ctx context.Context, extra []string, dir string, onProgress func(float64)) (string, ytMeta, error) {
	var lastErr error
	for _, client := range youtubeClientArgs {
		args := append(ytdlpArgs(), client...)
		args = append(args, extra...)
		path, meta, err := runYTDLP(ctx, args, dir, onProgress)
		if err == nil {
			return path, meta, nil
		}
		lastErr = err
	}
	return "", ytMeta{}, lastErr
}

type stdoutCapture struct {
	buf        bytes.Buffer
	pending    []byte
	onProgress func(float64)
}

var progressRe = regexp.MustCompile(`\[ *([0-9]+(?:\.[0-9]+)?)%`)

func (c *stdoutCapture) Write(p []byte) (int, error) {
	n, err := c.buf.Write(p)
	if c.onProgress == nil {
		return n, err
	}
	c.pending = append(c.pending, p...)
	for {
		i := bytes.IndexByte(c.pending, '\n')
		if i < 0 {
			break
		}
		line := c.pending[:i]
		c.pending = c.pending[i+1:]
		if m := progressRe.FindSubmatch(line); m != nil {
			if v, ferr := strconv.ParseFloat(string(m[1]), 64); ferr == nil {
				c.onProgress(v)
			}
		}
	}
	return n, err
}

func (c *stdoutCapture) String() string {
	return c.buf.String()
}

func runYTDLP(ctx context.Context, args []string, dir string, onProgress func(float64)) (string, ytMeta, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", ytMeta{}, err
	}
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	capture := &stdoutCapture{onProgress: onProgress}
	cmd.Stdout = io.MultiWriter(os.Stdout, capture)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", ytMeta{}, fmt.Errorf("yt-dlp failed: %w", err)
	}
	var path string
	var meta ytMeta
	for _, line := range strings.Split(capture.String(), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "FILE:"):
			path = strings.TrimPrefix(line, "FILE:")
		case strings.HasPrefix(line, "META:"):
			if parts := strings.Split(strings.TrimPrefix(line, "META:"), "|"); len(parts) == 3 {
				meta = ytMeta{extractor: parts[0], id: parts[1], height: parts[2]}
			}
		}
	}
	if path == "" {
		return "", meta, fmt.Errorf("yt-dlp finished but no output file was found")
	}
	return path, meta, nil
}

func downloadSpotify(ctx context.Context, url, dir string) int {
	path, err := downloadSpotifyFile(ctx, url, dir, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if path != "" {
		fmt.Println("RESULT:" + path)
	}
	return 0
}

func downloadSpotifyFile(ctx context.Context, url, dir string, onProgress func(float64)) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	path, fastErr := spotifyViaYouTube(ctx, url, dir, onProgress)
	if path != "" && fastErr == nil {
		if id := spotifyTrackID(url); id != "" {
			if renamed, err := renameService(path, "Spotify", "audio", id, ""); err == nil {
				path = renamed
			}
		}
		return path, nil
	}
	if fastErr == nil {
		fastErr = fmt.Errorf("no output file")
	}

	if spotdlBlocked() {
		return "", fmt.Errorf("%v (spotdl cooling down, not tried)", fastErr)
	}
	sub, err := os.MkdirTemp(dir, "spot-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(sub)

	lastErr := runSpotDLOnce(ctx, url, sub)
	if lastErr == nil {
		latest, err := newestFile(sub)
		switch {
		case err != nil:
			lastErr = err
		case latest == "":
			lastErr = fmt.Errorf("no output file")
		default:
			final := filepath.Join(dir, filepath.Base(latest))
			if final != latest {
				if err := os.Rename(latest, final); err != nil {
					return "", err
				}
				latest = final
			}
			if id := spotifyTrackID(url); id != "" {
				if renamed, err := renameService(latest, "Spotify", "audio", id, ""); err == nil {
					latest = renamed
				}
			}
			return latest, nil
		}
	}

	return "", fmt.Errorf("fast path: %v; spotdl: %v", fastErr, lastErr)
}

func runSpotDLOnce(ctx context.Context, url, sub string) error {
	cmd := exec.CommandContext(ctx, "spotdl", "download", url, "--output", sub, "--format", "mp3",
		"--bitrate", "320k",
		"--yt-dlp-args", "--extractor-args youtube:player_client=android --retries 3 --fragment-retries 3")
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	var out bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &out)
	cmd.Stderr = io.MultiWriter(os.Stdout, &out)
	err := cmd.Run()
	log := out.String()
	if err != nil {
		if strings.Contains(log, "Could not get session") || strings.Contains(log, "Status Code: 403") {
			markSpotdlBlocked()
		}
		return err
	}
	os.Remove(spotdlCooldownFile())
	return nil
}

func spotifyViaYouTube(ctx context.Context, link, dir string, onProgress func(float64)) (string, error) {
	title, err := oembedTitle(link)
	if err != nil {
		return "", fmt.Errorf("could not fetch track info from Spotify: %w", err)
	}
	fmt.Println("searching YouTube for:", title)
	args := []string{
		"--newline",
		"--progress",
		"--no-playlist",
		"--extract-audio",
		"--audio-format", "mp3",
		"--audio-quality", "0",
		"--progress-template", "download:[%(progress._percent_str)s] %(progress._speed_str)s",
		"--print", "after_move:FILE:%(filepath)s",
		"--print", "after_move:META:%(extractor_key)s|%(id)s|%(height)s",
		"-o", filepath.Join(dir, "%(id)s.%(ext)s"),
		"ytsearch1:" + title,
	}
	path, meta, err := runYTDLPYouTube(ctx, args, dir, onProgress)
	if err != nil {
		return "", err
	}
	return renameService(path, prettyExtractor(meta.extractor), "audio", meta.id, meta.height)
}

func oembedTitle(link string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get("https://open.spotify.com/oembed?url=" + url.QueryEscape(link))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("spotify oembed HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		return "", fmt.Errorf("no track title in spotify response")
	}
	return title, nil
}

func newestFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var files []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".mp3") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) == 0 {
		return "", nil
	}
	sort.Slice(files, func(i, j int) bool {
		si, errI := os.Stat(files[i])
		sj, errJ := os.Stat(files[j])
		if errI != nil || errJ != nil {
			return false
		}
		return si.ModTime().After(sj.ModTime())
	})
	return files[0], nil
}
