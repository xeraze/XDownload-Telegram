package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	stateQueued  = "queued"
	stateRunning = "running"
	stateDone    = "done"
	stateError   = "error"
)

type apiJob struct {
	id       string
	created  time.Time
	url      string
	format   string
	height   int
	state    string
	progress float64
	hasProg  bool
	dir      string
	path     string
	name     string
	size     int64
	message  string
}

type apiServer struct {
	mu      sync.Mutex
	jobs    map[string]*apiJob
	sem     chan struct{}
	dir     string
	key     string
	ttl     time.Duration
	timeout time.Duration
	maxJobs int
}

type fileRef struct {
	Name string `json:"name"`
	Size int64  `json:"size,omitempty"`
}

type jobResponse struct {
	State    string   `json:"state"`
	Progress *float64 `json:"progress,omitempty"`
	File     *fileRef `json:"file,omitempty"`
	Message  string   `json:"message,omitempty"`
}

var allowedHosts = []string{
	"youtube.com",
	"youtu.be",
	"music.youtube.com",
	"open.spotify.com",
	"spotify.com",
	"soundcloud.com",
	"tiktok.com",
	"instagram.com",
	"facebook.com",
	"fb.watch",
	"reddit.com",
	"redd.it",
	"pin.it",
	"vk.com",
	"x.com",
	"twitter.com",
	"rumble.com",
	"snapchat.com",
	"snap.com",
	"t.co",
}

var (
	pinterestHostRe = regexp.MustCompile(`^pinterest\.[a-z]{2,3}$`)
	extTypes        = map[string]string{
		".mp4":  "video/mp4",
		".m4v":  "video/x-m4v",
		".webm": "video/webm",
		".mp3":  "audio/mpeg",
		".m4a":  "audio/mp4",
		".opus": "audio/opus",
		".ogg":  "audio/ogg",
	}
)

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func cmdAPI(args []string) int {
	fs := flag.NewFlagSet("api", flag.ExitOnError)
	addr := fs.String("addr", envOr("XDL_API_ADDR", "127.0.0.1:8080"), "listen address")
	dir := fs.String("dir", envOr("XDL_API_DIR", filepath.Join(os.TempDir(), "xdownload-api")), "download directory")
	fs.Parse(args)

	s := newAPIServer(*dir)
	s.prepareDir()
	go s.janitor()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("POST /api/download", s.handleDownload)
	mux.HandleFunc("GET /api/job/{id}", s.handleJob)
	mux.HandleFunc("GET /api/file/{id}", s.handleFile)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           s.guard(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("xcore api listening on http://%s (dir=%s, key=%v)", *addr, *dir, s.key != "")
	if err := srv.ListenAndServe(); err != nil {
		log.Println("api server:", err)
		return 1
	}
	return 0
}

func newAPIServer(dir string) *apiServer {
	return &apiServer{
		jobs:    make(map[string]*apiJob),
		sem:     make(chan struct{}, envInt("XDL_API_CONCURRENCY", 2)),
		dir:     dir,
		key:     os.Getenv("XDL_API_KEY"),
		ttl:     time.Duration(envInt("XDL_API_TTL_MIN", 30)) * time.Minute,
		timeout: time.Duration(envInt("XDL_API_TIMEOUT_MIN", 15)) * time.Minute,
		maxJobs: envInt("XDL_API_MAX", 8),
	}
}

func (s *apiServer) prepareDir() {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		log.Println("dir:", err)
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(s.dir, e.Name()))
	}
}

func (s *apiServer) guard(next http.Handler) http.Handler {
	if s.key == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hmac.Equal([]byte(r.Header.Get("X-Api-Key")), []byte(s.key)) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *apiServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func validateDownloadURL(raw string) error {
	if raw == "" || len(raw) > 2048 {
		return errors.New("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("invalid url scheme")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errors.New("invalid url host")
	}
	host = strings.TrimPrefix(host, "www.")
	for _, h := range allowedHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return nil
		}
	}
	if pinterestHostRe.MatchString(host) {
		return nil
	}
	return errors.New("unsupported host")
}

type downloadRequest struct {
	URL    string `json:"url"`
	Format string `json:"format"`
	Height int    `json:"height"`
}

func (s *apiServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var req downloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if err := validateDownloadURL(req.URL); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	switch req.Format {
	case "mp3":
		req.Height = 0
	case "mp4":
		switch req.Height {
		case 0:
			req.Height = 1080
		case 480, 720, 1080:
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid height"})
			return
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid format"})
		return
	}

	s.mu.Lock()
	active := 0
	for _, j := range s.jobs {
		if j.state == stateQueued || j.state == stateRunning {
			active++
		}
	}
	if active >= s.maxJobs {
		s.mu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "busy"})
		return
	}
	id := rand.Text()
	job := &apiJob{
		id:      id,
		created: time.Now(),
		url:     req.URL,
		format:  req.Format,
		height:  req.Height,
		state:   stateQueued,
		dir:     filepath.Join(s.dir, id),
	}
	s.jobs[id] = job
	s.mu.Unlock()

	go s.run(id)
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *apiServer) handleJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	job := s.jobs[id]
	if job == nil {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	resp := jobResponse{State: job.state, Message: job.message}
	if job.state == stateDone {
		resp.File = &fileRef{Name: job.name, Size: job.size}
	} else if job.hasProg {
		p := job.progress
		resp.Progress = &p
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

func (s *apiServer) handleFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	job := s.jobs[id]
	var path, name string
	if job != nil && job.state == stateDone && job.path != "" {
		path, name = job.path, job.name
	}
	s.mu.Unlock()
	if path == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "gone"})
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "gone"})
		return
	}
	ctype := extTypes[strings.ToLower(filepath.Ext(name))]
	if ctype == "" {
		ctype = mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", attachmentDisposition(name))
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func attachmentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	if ascii == "" {
		ascii = "download"
	}
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}

func (s *apiServer) run(id string) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	s.mu.Lock()
	job := s.jobs[id]
	if job == nil || job.state != stateQueued {
		s.mu.Unlock()
		return
	}
	job.state = stateRunning
	rawURL, format, height, jobDir := job.url, job.format, job.height, job.dir
	s.mu.Unlock()

	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		s.fail(id, err, nil)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	onProgress := func(p float64) {
		s.mu.Lock()
		if j := s.jobs[id]; j != nil {
			j.progress = p
			j.hasProg = true
		}
		s.mu.Unlock()
	}

	path, err := s.download(ctx, rawURL, format, height, jobDir, onProgress)
	if err != nil {
		log.Printf("job %s failed: %v", id, err)
		s.fail(id, err, ctx)
		return
	}

	fi, statErr := os.Stat(path)
	s.mu.Lock()
	if j := s.jobs[id]; j != nil {
		j.state = stateDone
		j.path = path
		j.name = filepath.Base(path)
		j.progress = 100
		j.hasProg = true
		if statErr == nil {
			j.size = fi.Size()
		}
	}
	s.mu.Unlock()
	log.Printf("job %s done: %s", id, filepath.Base(path))
}

func (s *apiServer) download(ctx context.Context, rawURL, format string, height int, jobDir string, onProgress func(float64)) (string, error) {
	if isSpotifyURL(rawURL) {
		return downloadSpotifyFile(ctx, rawURL, jobDir, onProgress)
	}
	if format == "mp4" {
		return downloadVideo(ctx, rawURL, jobDir, height, onProgress)
	}
	return downloadAudio(ctx, rawURL, format, jobDir, onProgress)
}

func (s *apiServer) fail(id string, err error, ctx context.Context) {
	msg := err.Error()
	if ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		msg = "timeout"
	}
	s.mu.Lock()
	if j := s.jobs[id]; j != nil {
		j.state = stateError
		j.message = msg
		jobDir := j.dir
		s.mu.Unlock()
		go os.RemoveAll(jobDir)
		return
	}
	s.mu.Unlock()
}

func (s *apiServer) janitor() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-s.ttl)
		s.mu.Lock()
		var removeDirs []string
		for id, j := range s.jobs {
			if (j.state == stateDone || j.state == stateError) && j.created.Before(cutoff) {
				if j.dir != "" {
					removeDirs = append(removeDirs, j.dir)
				}
				delete(s.jobs, id)
			}
		}
		s.mu.Unlock()
		for _, dir := range removeDirs {
			_ = os.RemoveAll(dir)
		}
	}
}
