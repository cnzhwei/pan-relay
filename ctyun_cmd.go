package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cnzhwei/pan-relay/internal/ctyun"
)

// handleCTYunCommand dispatches `pan-relay ctyun <subcommand>`.
func handleCTYunCommand(ctx context.Context, configPath string, threads int, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: pan-relay ctyun <whoami|ls|download|upload|mkdir|rm|refresh> [args]")
		os.Exit(1)
	}

	sub := args[0]
	rest := args[1:]

	cfg, err := ctyun.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Tianyi Config Error: %v\n", err)
		os.Exit(1)
	}
	cl := ctyun.NewClient(cfg)

	space := "个人云"
	if cfg.IsFamily() {
		space = "家庭云"
	}

	switch sub {
	case "whoami", "user":
		if err := cl.EnsureSession(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Login failed: %v\n", err)
			os.Exit(1)
		}
		total, used, err := cl.Capacity(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Capacity query failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("=== 天翼云盘账号信息 ===")
		fmt.Printf("登录名   : %s\n", cl.LoginName())
		fmt.Printf("空间类型 : %s\n", space)
		if cfg.FamilyID != "" {
			fmt.Printf("家庭云ID : %s\n", cfg.FamilyID)
		}
		fmt.Printf("根目录ID : %s\n", cl.RootID())
		if total > 0 {
			fmt.Printf("总容量   : %s\n", humanSize(total))
			fmt.Printf("已使用   : %s (%.1f%%)\n", humanSize(used), float64(used)/float64(total)*100)
			fmt.Printf("剩余     : %s\n", humanSize(total-used))
		}
		if cfg.FilePath() != "" {
			fmt.Printf("配置文件 : %s\n", cfg.FilePath())
		}

	case "ls", "list":
		target := "/"
		if len(rest) > 0 {
			target = rest[0]
		}
		dirID, err := cl.ResolveDir(ctx, target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving path %q: %v\n", target, err)
			os.Exit(1)
		}
		items, err := cl.ListDir(ctx, dirID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing directory: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("=== 天翼云盘 [%s] %s (%d 项) ===\n", space, target, len(items))
		fmt.Printf("  %-6s  %-12s  %-20s  %-20s  %s\n", "TYPE", "SIZE", "MODIFIED", "ID", "NAME")
		fmt.Printf("  %s\n", strings.Repeat("-", 95))
		for _, it := range items {
			typ := "FILE"
			sizeStr := humanSize(it.Size)
			if it.IsFolder {
				typ = "DIR "
				sizeStr = "-"
			}
			mod := "-"
			if !it.Modified.IsZero() {
				mod = it.Modified.Format("2006-01-02 15:04:05")
			}
			fmt.Printf("  [%s]  %-12s  %-20s  %-20s  %s\n", typ, sizeStr, mod, it.ID, it.Name)
		}

	case "download", "dl":
		if len(rest) < 1 {
			fmt.Println("Usage: pan-relay ctyun download <remote_path|file_id> [local_output] [-t N]")
			os.Exit(1)
		}
		target := rest[0]

		var item *ctyun.FileItem
		if strings.Contains(target, "/") || !isAllDigits(target) {
			item, err = cl.FindFile(ctx, target)
		} else {
			item = &ctyun.FileItem{ID: target, Name: target}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving %q: %v\n", target, err)
			os.Exit(1)
		}

		// Never trust a remote-supplied name for a local path: reduce it to a
		// bare base name so a crafted "..\..\etc\cron.d/x" cannot escape the
		// working directory.
		safeName := filepath.Base(filepath.Clean("/" + item.Name))
		if safeName == "" || safeName == "." || safeName == "/" {
			safeName = "download.bin"
		}

		output := safeName
		if len(rest) > 1 {
			output = rest[1]
			if fi, err := os.Stat(output); err == nil && fi.IsDir() {
				output = filepath.Join(output, safeName)
			}
		}

		dlURL, err := cl.GetDownloadURL(ctx, item.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting download url: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("开始下载 %s -> %s (线程 %d)\n", item.Name, output, threads)
		start := time.Now()
		opt := ctyun.DownloadOptions{
			Concurrency:  threads,
			Retries:      3,
			ShowProgress: true,
			Progress:     progressBytes(start),
		}
		if err := ctyun.Download(ctx, dlURL, nil, output, opt); err != nil {
			fmt.Fprintf(os.Stderr, "\nDownload failed: %v\n", err)
			os.Exit(1)
		}
		if fi, err := os.Stat(output); err == nil {
			el := time.Since(start).Seconds()
			fmt.Printf("\n[✓] 下载完成 %s  %.2fs  %s/s\n",
				humanSize(fi.Size()), el, humanSize(int64(float64(fi.Size())/el)))
		}

	case "upload", "up":
		if len(rest) < 1 {
			fmt.Println("Usage: pan-relay ctyun upload <local_file> [remote_dir]")
			os.Exit(1)
		}
		local := rest[0]
		remoteDir := "/"
		if len(rest) > 1 {
			remoteDir = rest[1]
		}
		dirID, err := cl.ResolveDir(ctx, remoteDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving remote dir %q: %v\n", remoteDir, err)
			os.Exit(1)
		}

		start := time.Now()
		opt := ctyun.UploadOptions{
			Concurrency:  threads,
			Retries:      3,
			ShowProgress: true,
			Progress:     progressPrinter(start),
		}
		fid, err := cl.UploadLocal(ctx, dirID, local, opt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nUpload failed: %v\n", err)
			os.Exit(1)
		}
		if fi, err := os.Stat(local); err == nil {
			el := time.Since(start).Seconds()
			fmt.Printf("\n[✓] 上传完成 %s  %.2fs  %s/s  fileId=%s\n",
				humanSize(fi.Size()), el, humanSize(int64(float64(fi.Size())/el)), fid)
		} else {
			fmt.Printf("\n[✓] 上传完成 fileId=%s\n", fid)
		}

	case "mkdir":
		if len(rest) < 2 {
			fmt.Println("Usage: pan-relay ctyun mkdir <parent_path> <dir_name>")
			os.Exit(1)
		}
		parentID, err := cl.ResolveDir(ctx, rest[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving parent %q: %v\n", rest[0], err)
			os.Exit(1)
		}
		id, err := cl.Mkdir(ctx, parentID, rest[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[✓] 目录已创建: %s/%s (id=%s)\n", strings.TrimRight(rest[0], "/"), rest[1], id)

	case "rm", "delete":
		if len(rest) < 1 {
			fmt.Println("Usage: pan-relay ctyun rm <path>")
			os.Exit(1)
		}
		target := rest[0]
		dirID, err := cl.ResolveDir(ctx, filepath.Dir("/"+strings.TrimPrefix(target, "/")))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving parent: %v\n", err)
			os.Exit(1)
		}
		name := filepath.Base(target)
		items, err := cl.ListDir(ctx, dirID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing directory: %v\n", err)
			os.Exit(1)
		}
		var found *ctyun.FileItem
		for i := range items {
			if items[i].Name == name {
				found = &items[i]
				break
			}
		}
		if found == nil {
			fmt.Fprintf(os.Stderr, "Not found: %s\n", target)
			os.Exit(1)
		}
		if err := cl.Remove(ctx, *found); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[✓] 已删除: %s\n", target)

	case "refresh":
		secs, err := cl.RefreshToken(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Refresh failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("[✓] access_token 已刷新")
		if secs > 0 {
			fmt.Printf("有效期: %d 秒 (%.2f 天)\n", secs, float64(secs)/86400)
		}
		if cfg.FilePath() != "" {
			fmt.Printf("已写回: %s\n", cfg.FilePath())
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown ctyun command %q\n", sub)
		os.Exit(1)
	}
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// progressPrinter returns a callback that is safe to call from multiple
// goroutines: the rate-limit timestamp and the terminal writes are serialised.
func progressPrinter(start time.Time) func(int) {
	var mu sync.Mutex
	last := time.Duration(0)
	return func(pct int) {
		mu.Lock()
		defer mu.Unlock()
		el := time.Since(start)
		if el-last < 300*time.Millisecond && pct < 100 {
			return
		}
		last = el
		fmt.Printf("\r  %3d%%  用时 %5.1fs", pct, el.Seconds())
		if pct >= 100 {
			fmt.Println()
		}
	}
}

// progressBytes returns a byte-oriented progress callback that is safe to call
// from multiple goroutines.
func progressBytes(start time.Time) func(int64, int64) {
	var mu sync.Mutex
	last := time.Duration(0)
	return func(written, total int64) {
		mu.Lock()
		defer mu.Unlock()
		el := time.Since(start)
		if el-last < 300*time.Millisecond && (total <= 0 || written < total) {
			return
		}
		last = el
		if total > 0 {
			fmt.Printf("\r  %5.1f%%  %s / %s  用时 %.1fs",
				float64(written)/float64(total)*100, humanSize(written), humanSize(total), el.Seconds())
		} else {
			fmt.Printf("\r  %s  用时 %.1fs", humanSize(written), el.Seconds())
		}
		if total > 0 && written >= total {
			fmt.Println()
		}
	}
}
