package app

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"shockingvrc/internal/config"
	"shockingvrc/internal/logx"
)

// ReleaseAssetSuffix identifies the Go build's release zip.
const ReleaseAssetSuffix = "go_windows_x64.zip"

type updater struct {
	mu        sync.Mutex
	cache     map[string]any
	cacheTime time.Time
	applying  sync.Mutex
	progMu    sync.Mutex
	progress  map[string]any
}

func newUpdater() *updater {
	return &updater{progress: map[string]any{
		"stage": "idle", "percent": 0, "downloaded": 0, "total": 0, "message": "", "error": "", "updated_at": 0.0,
	}}
}

func (u *updater) clearCache() {
	u.mu.Lock()
	u.cache = nil
	u.mu.Unlock()
}

func (u *updater) setProgress(kv ...any) {
	u.progMu.Lock()
	defer u.progMu.Unlock()
	for i := 0; i+1 < len(kv); i += 2 {
		k := kv[i].(string)
		v := kv[i+1]
		if k == "percent" {
			v = config.ClampInt(config.Int(v, 0), 0, 100)
		}
		u.progress[k] = v
	}
	u.progress["updated_at"] = nowSec()
}

func (u *updater) progressSnapshot() map[string]any {
	u.progMu.Lock()
	defer u.progMu.Unlock()
	return config.CopyMap(u.progress)
}

func (a *App) githubMirror() string {
	return strings.TrimRight(strings.TrimSpace(config.Str(a.Cfg.Get("general", "github_mirror"), "")), "/")
}

// applyMirror proxies github.com download URLs (never api.github.com).
func (a *App) applyMirror(u string) string {
	m := a.githubMirror()
	if m == "" || strings.Contains(u, "api.github.com") {
		return u
	}
	return m + "/" + u
}

var httpClient = &http.Client{Timeout: 120 * time.Second}

func httpGet(url string, timeout time.Duration) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ShockingVRChat")
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	c := *httpClient
	c.Timeout = timeout
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP Error %d: %s", resp.StatusCode, resp.Status)
	}
	return resp, nil
}

// VersionNewer compares dotted numeric versions (first 3 parts).
func VersionNewer(latest, current string) bool {
	parse := func(v string) ([]int, bool) {
		v = strings.SplitN(v, "-", 2)[0]
		parts := strings.Split(v, ".")
		if len(parts) > 3 {
			parts = parts[:3]
		}
		out := []int{}
		for _, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil, false
			}
			out = append(out, n)
		}
		return out, true
	}
	l, ok1 := parse(latest)
	c, ok2 := parse(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < len(l) || i < len(c); i++ {
		var x, y int
		if i < len(l) {
			x = l[i]
		}
		if i < len(c) {
			y = c[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func (a *App) checkUpdate() map[string]any {
	u := a.upd
	u.mu.Lock()
	if u.cache != nil && time.Since(u.cacheTime) < 5*time.Minute {
		c := u.cache
		u.mu.Unlock()
		return c
	}
	u.mu.Unlock()

	res, apiErr := a.checkViaAPI()
	if apiErr != nil {
		// api.github.com allows only 60 unauthenticated requests per hour per
		// IP and is often unreachable from mainland China. The public release
		// page has neither problem (and works through mirrors), so fall back.
		logx.Info("[update] GitHub API unavailable (%v), using release page", apiErr)
		var pageErr error
		res, pageErr = a.checkViaReleasePage()
		if pageErr != nil {
			logx.Warn("[update] Update check failed: api: %v; page: %v", apiErr, pageErr)
			return map[string]any{"current": Version, "latest": nil, "update_available": false,
				"error": apiErr.Error() + "; " + pageErr.Error()}
		}
	}
	res["github_mirror"] = a.githubMirror()
	u.mu.Lock()
	u.cache, u.cacheTime = res, time.Now()
	u.mu.Unlock()
	return res
}

func (a *App) checkViaAPI() (map[string]any, error) {
	resp, err := httpGet("https://api.github.com/repos/"+GitHubRepo+"/releases/latest", 15*time.Second)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	tag := config.Str(data["tag_name"], "")
	if tag == "" {
		return nil, fmt.Errorf("no tag_name in API response")
	}
	latest := strings.TrimLeft(tag, "v")
	var dlURL any
	dlSize, dlName := 0, ""
	assets := config.AsList(data["assets"])
	if assets == nil {
		assets = []any{}
	}
	for _, as := range assets {
		m := config.AsMap(as)
		if n := config.Str(m["name"], ""); strings.HasSuffix(n, ReleaseAssetSuffix) {
			dlURL, dlSize, dlName = m["browser_download_url"], config.Int(m["size"], 0), n
			break
		}
	}
	notes := config.Str(data["body"], "")
	if r := []rune(notes); len(r) > 2000 {
		notes = string(r[:2000])
	}
	return map[string]any{
		"current": Version, "latest": latest, "latest_tag": tag,
		"update_available": VersionNewer(latest, Version),
		"download_url":     dlURL, "download_size": dlSize, "download_name": dlName,
		"assets": assets, "release_name": config.Str(data["name"], ""),
		"release_notes": notes, "published_at": config.Str(data["published_at"], ""),
		"source": "api",
	}, nil
}

var tagInPage = regexp.MustCompile(`/releases/tag/(v?[0-9][0-9A-Za-z.\-]*)`)

// latestTagFromPage resolves the latest release tag from
// https://github.com/<repo>/releases/latest (a redirect to .../tag/<tag>).
func (a *App) latestTagFromPage() (string, error) {
	url := a.applyMirror("https://github.com/" + GitHubRepo + "/releases/latest")
	c := &http.Client{
		Timeout: 15 * time.Second,
		// Read the redirect target instead of downloading the HTML page.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if tagInPage.MatchString(req.URL.Path) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ShockingVRChat")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "" {
		if m := tagInPage.FindStringSubmatch(loc); m != nil {
			return m[1], nil
		}
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP Error %d: %s", resp.StatusCode, resp.Status)
	}
	// Some mirrors follow the redirect themselves and return the HTML page.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if m := tagInPage.FindStringSubmatch(string(body)); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("could not find the latest release tag")
}

func (a *App) checkViaReleasePage() (map[string]any, error) {
	tag, err := a.latestTagFromPage()
	if err != nil {
		return nil, err
	}
	latest := strings.TrimLeft(tag, "v")
	name := "shocking_vrchat_" + ReleaseAssetSuffix
	base := "https://github.com/" + GitHubRepo + "/releases/download/" + tag + "/"
	return map[string]any{
		"current": Version, "latest": latest, "latest_tag": tag,
		"update_available": VersionNewer(latest, Version),
		// Size is unknown here; integrity then relies on sha256sums.txt.
		"download_url": base + name, "download_size": 0, "download_name": name,
		"assets":       []any{map[string]any{"name": "sha256sums.txt", "browser_download_url": base + "sha256sums.txt"}},
		"release_name": tag, "release_notes": "", "published_at": "",
		"source": "page",
	}, nil
}

func (a *App) apiUpdateCheck(*http.Request) (any, error) { return a.checkUpdate(), nil }

func (a *App) download(url, dest string, expected int) (int64, error) {
	resp, err := httpGet(url, 120*time.Second)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	total := resp.ContentLength
	if total <= 0 {
		total = int64(expected)
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var done int64
	lastLog, lastPct := -10, -1
	buf := make([]byte, 65536)
	a.upd.setProgress("downloaded", 0, "total", total)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return done, err
			}
			done += int64(n)
			pct := 0
			if total > 0 {
				pct = int(done * 100 / total)
				if pct-lastLog >= 10 {
					logx.Info("[update] Download progress: %d%% (%d/%d)", pct, done, total)
					lastLog = pct
				}
			}
			if pct != lastPct || total <= 0 {
				lastPct = pct
				a.upd.setProgress("stage", "downloading", "percent", pct, "downloaded", done, "total", total, "message", "正在下载更新包...")
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return done, rerr
		}
	}
	return done, nil
}

func (a *App) fetchSHA256(assets []any, zipName string) string {
	for _, as := range assets {
		m := config.AsMap(as)
		n := strings.ToLower(config.Str(m["name"], ""))
		if !strings.Contains(n, "sha256") || !strings.HasSuffix(n, ".txt") {
			continue
		}
		resp, err := httpGet(a.applyMirror(config.Str(m["browser_download_url"], "")), 15*time.Second)
		if err != nil {
			logx.Warn("[update] Could not fetch sha256sums.txt: %v", err)
			return ""
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				fn := strings.TrimLeft(parts[len(parts)-1], "*")
				if fn == zipName || strings.HasSuffix(fn, "/"+zipName) {
					return parts[0]
				}
			}
		}
		return ""
	}
	return ""
}

func verifyZip(path string, size int, sum string) string {
	st, err := os.Stat(path)
	if err != nil {
		return err.Error()
	}
	if size > 0 && st.Size() != int64(size) {
		return fmt.Sprintf("Size mismatch: expected %d, got %d", size, st.Size())
	}
	if sum != "" {
		f, err := os.Open(path)
		if err != nil {
			return err.Error()
		}
		defer f.Close()
		h := sha256.New()
		_, _ = io.Copy(h, f)
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, sum) {
			return fmt.Sprintf("SHA256 mismatch: expected %.16s..., got %.16s...", sum, got)
		}
	}
	return ""
}

// extractZip extracts with zip-slip protection.
func extractZip(path, dir string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	absDir, _ := filepath.Abs(dir)
	for _, f := range zr.File {
		target := filepath.Join(absDir, filepath.FromSlash(f.Name))
		if target != absDir && !strings.HasPrefix(target, absDir+string(os.PathSeparator)) {
			return &HTTPError{Code: 500, Detail: "压缩包包含恶意路径，更新已中止: " + f.Name}
		}
	}
	for _, f := range zr.File {
		target := filepath.Join(absDir, filepath.FromSlash(f.Name))
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		r, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.Create(target)
		if err != nil {
			r.Close()
			return err
		}
		_, err = io.Copy(w, r)
		r.Close()
		w.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func updateScript(pid int, exeDir, exePath, srcDir, tmpDir string) string {
	// User data that must survive an update untouched.
	userDirs := `"_backup" "wave_presets" "dg-lab" "profiles" "recordings"`
	userFiles := fmt.Sprintf(`"%s" "%s" "curve_config.yaml" "overlimit_rules.yaml" "window_state.json" "shocking_vrchat.log*" "error.log" "update.log"`,
		config.FileAdvanced, config.FileBasic)
	// robocopy defaults to 1,000,000 retries x 30 s on a locked file, which
	// would hang the (hidden) updater forever.
	const retry = "/R:10 /W:1 /NP /NFL /NDL"
	logFile := exeDir + `\update.log`
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\r\n", args...) }
	w("@echo off")
	w("setlocal enabledelayedexpansion")
	w("chcp 65001 >nul 2>&1")
	w(`set "LOG=%s"`, logFile)
	w(`echo [%%date%% %%time%%] Updating, waiting for PID %d to exit > "%%LOG%%"`, pid)
	w("set WAIT_COUNT=0")
	w(":WAIT_LOOP")
	// Match the image name rather than the PID digits (no partial matches,
	// no quoting issues, independent of the localized "no tasks" text).
	w(`tasklist /FI "PID eq %d" /NH 2>nul | find /I "%s" >nul`, pid, exePath[strings.LastIndexAny(exePath, `\\/`)+1:])
	w("if errorlevel 1 goto COPY_FILES")
	w("set /a WAIT_COUNT+=1")
	w("if %%WAIT_COUNT%% GEQ 20 (")
	w(`    echo Process did not exit in 20s, force killing >> "%%LOG%%"`)
	w("    taskkill /F /PID %d >nul 2>&1", pid)
	w("    ping -n 3 127.0.0.1 >nul")
	w("    goto COPY_FILES")
	w(")")
	w("ping -n 2 127.0.0.1 >nul")
	w("goto WAIT_LOOP")
	w(":COPY_FILES")
	// The exe can stay locked for a moment after the process is gone.
	w("ping -n 2 127.0.0.1 >nul")
	w(`echo Backing up >> "%%LOG%%"`)
	w(`if exist "%s\_backup" rmdir /s /q "%s\_backup"`, exeDir, exeDir)
	w(`robocopy "%s" "%s\_backup" /E /XD %s /XF %s %s >> "%%LOG%%" 2>&1`, exeDir, exeDir, userDirs, userFiles, retry)
	w(`echo Applying update >> "%%LOG%%"`)
	w(`robocopy "%s" "%s" /E /XD %s /XF %s %s >> "%%LOG%%" 2>&1`, srcDir, exeDir, userDirs, userFiles, retry)
	w("set RC=!ERRORLEVEL!")
	// Bundled presets: add new ones, never overwrite the user's files.
	w(`robocopy "%s\wave_presets" "%s\wave_presets" /E /XC /XN /XO %s >> "%%LOG%%" 2>&1`, srcDir, exeDir, retry)
	w(`robocopy "%s\dg-lab" "%s\dg-lab" /E /XC /XN /XO %s >> "%%LOG%%" 2>&1`, srcDir, exeDir, retry)
	w("if !RC! GEQ 8 (")
	w(`    echo [ERROR] Copy failed ^(robocopy !RC!^), rolling back >> "%%LOG%%"`)
	w(`    robocopy "%s\_backup" "%s" /E %s >> "%%LOG%%" 2>&1`, exeDir, exeDir, retry)
	w(`    start "" "%s"`, exePath)
	w("    goto CLEANUP")
	w(")")
	w(`echo Update complete, starting new version >> "%%LOG%%"`)
	w(`start "" "%s"`, exePath)
	w(":CLEANUP")
	// Last line: deleting the folder also deletes this script.
	w(`cd /d "%%TEMP%%" & rmdir /s /q "%s" & exit /b 0`, tmpDir)
	return b.String()
}

func (a *App) apiUpdateApply(*http.Request) (any, error) {
	u := a.upd
	if !u.applying.TryLock() {
		return nil, httpErr(409, "更新正在进行中，请勿重复操作")
	}
	defer u.applying.Unlock()
	if !a.Packaged || runtime.GOOS != "windows" {
		return nil, httpErr(400, "仅 Windows 打包版本支持自动更新，开发环境请使用 git pull")
	}
	chk := a.checkUpdate()
	if !config.Bool(chk["update_available"], false) {
		return map[string]any{"success": false, "message": "当前已是最新版本"}, nil
	}
	dlURL := config.Str(chk["download_url"], "")
	if dlURL == "" {
		return nil, httpErr(400, "未找到可下载的更新文件")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	exeDir := filepath.Dir(exe)
	tmpDir, err := os.MkdirTemp("", "svrc_update_")
	if err != nil {
		return nil, err
	}
	fail := func(e error) (any, error) {
		_ = os.RemoveAll(tmpDir)
		u.setProgress("stage", "error", "error", e.Error())
		logx.Error("[update] Failed: %v", e)
		if _, ok := e.(*HTTPError); ok {
			return nil, e
		}
		return nil, httpErr(500, "更新失败: %v", e)
	}
	zipPath := filepath.Join(tmpDir, "update.zip")
	size := config.Int(chk["download_size"], 0)
	u.setProgress("stage", "downloading", "percent", 0, "downloaded", 0, "total", size, "message", "正在下载更新包...", "error", "")
	actual := a.applyMirror(dlURL)
	logx.Info("[update] Downloading: %s", actual)
	n, err := a.download(actual, zipPath, size)
	if err != nil {
		return fail(err)
	}
	logx.Info("[update] Downloaded %d bytes to %s", n, zipPath)
	u.setProgress("stage", "verifying", "percent", 100, "message", "正在校验文件完整性...")
	sum := a.fetchSHA256(config.AsList(chk["assets"]), config.Str(chk["download_name"], ""))
	if sum == "" && size == 0 {
		return fail(&HTTPError{Code: 500, Detail: "文件完整性校验失败: 无法获取 sha256sums.txt，且文件大小未知"})
	}
	if msg := verifyZip(zipPath, size, sum); msg != "" {
		return fail(&HTTPError{Code: 500, Detail: "文件完整性校验失败: " + msg})
	}
	logx.Info("[update] Integrity check passed")
	u.setProgress("stage", "extracting", "message", "正在解压更新包...")
	extract := filepath.Join(tmpDir, "extracted")
	if err := extractZip(zipPath, extract); err != nil {
		return fail(err)
	}
	src := extract
	if entries, _ := os.ReadDir(extract); len(entries) == 1 && entries[0].IsDir() {
		src = filepath.Join(extract, entries[0].Name())
	}
	u.setProgress("stage", "applying", "message", "正在准备应用更新...")
	bat := filepath.Join(tmpDir, "apply_update.bat")
	if err := os.WriteFile(bat, []byte(updateScript(os.Getpid(), exeDir, exe, src, tmpDir)), 0o644); err != nil {
		return fail(err)
	}
	logx.Info("[update] Launching update script and exiting...")
	u.setProgress("stage", "restarting", "percent", 100, "message", "更新已下载，正在应用并重启...")
	if err := spawnHiddenConsole("cmd", []string{"/c", bat}); err != nil {
		return fail(err)
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		logx.Info("[update] Stopping services before restart...")
		a.Engine.Stop(false)
		logx.Info("[update] Exiting for update apply...")
		logx.Close()
		os.Exit(0)
	}()
	return map[string]any{"success": true, "message": "更新下载完成，正在应用更新并重启..."}, nil
}
