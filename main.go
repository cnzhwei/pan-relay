package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cnzhwei/pan-relay/internal/baidu"
	"github.com/cnzhwei/pan-relay/internal/client"
	"github.com/cnzhwei/pan-relay/internal/config"
	"github.com/cnzhwei/pan-relay/internal/downloader"
	"github.com/cnzhwei/pan-relay/internal/emby"
	"github.com/cnzhwei/pan-relay/internal/quark"
	"github.com/cnzhwei/pan-relay/internal/stream"
	"github.com/cnzhwei/pan-relay/internal/uploader"
)

var Version = "2.2.0"

const maxConcurrency = 32

func printUsage() {
	fmt.Printf(`pan-relay (全能多网盘轻量多线程中转工具) v%s

Usage:
  pan-relay [options] <command> [arguments]

=== 1. 跨网盘全自动中转流水线 (Relay) ===
  relay <source> <destination> [--stream]
      从指定源网盘拉取文件并多线程推流至目标网盘。
      支持 --stream 开启内存管道流式穿透（零磁盘占用、边下边传、突破 VPS 硬盘限制）。
      示例:
        pan-relay relay quark:/来自：分享/电影.mkv wopan:/emby/movies/ --stream -t 6
        pan-relay relay baidu:/影视资源/电影.mkv wopan:/emby/movies/ --stream -t 6

=== 2. 沃家云盘 (WoPan) 命令 ===
  wopan whoami                  显示当前登录账号信息与空间容量
  wopan ls [path|dir_id]        浏览沃家网盘目录 (默认根目录 /)
  wopan mkdir <parent> <name>   创建新目录
  wopan rm <id>                 删除指定文件或目录 ID
  wopan upload <local> [remote] 多线程并发上传本地文件至沃家网盘 (默认 4 线程)
  wopan download <fid|path> [dst]
                                多线程并发下载沃家网盘文件 (支持 Range 分片加速)
  wopan refresh                 强制使用 refresh_token 刷新当前登录凭据
  (兼容简写: whoami, ls, mkdir, rm, upload, download, refresh 默认指向 wopan)

=== 3. 夸克网盘 (Quark) 命令 ===
  quark ls [path|fid]           浏览夸克网盘目录 (如 / 或 /来自：分享)
  quark download <fid|path> [dst]
                                从夸克网盘多线程并发高速下载 (绕过 Web 文件大小限制)

=== 4. 百度网盘 (Baidu Netdisk) 命令 ===
  baidu ls [path]               浏览百度网盘目录 (如 / 或 /我的资源)
  baidu download <path> [dst]   从百度网盘多线程并发高速下载 (支持 Range 分片)
  baidu mkdir <parent> <name>   在百度网盘创建新目录
  baidu rm <path>               删除百度网盘指定文件或目录

=== 全局通用选项 ===
  -c, --config <path>           指定 WoPan 配置文件路径 (默认自动查找 ./wopan_config.json)
  --quark-config <path>         指定 Quark 配置文件路径 (默认自动查找 ./quark_config.json)
  --baidu-config <path>         指定 Baidu 配置文件路径 (默认自动查找 ./baidu_config.json)
  --emby-config <path>          指定 Emby 配置文件路径 (默认 ~/.config/pan-relay/emby.json)
  --emby-ua <ua>                覆盖 Emby HTTP User-Agent
  --emby-client <name>          覆盖 X-Emby-Authorization Client
  --emby-device <name>          覆盖 X-Emby-Authorization Device
  --emby-version <version>      覆盖 X-Emby-Authorization Version
  -t, --threads <num>           并发线程数 (默认: 4)
  version                       显示当前程序版本
`, Version)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	var configPath string
	var quarkConfigPath string
	var baiduConfigPath string
	var embyConfigPath string
	var embyUA string
	var embyClient string
	var embyDevice string
	var embyVersion string
	var threads int
	var streamRelay bool

	flags := flag.NewFlagSet("pan-relay", flag.ExitOnError)
	flags.StringVar(&configPath, "c", "", "WoPan config path")
	flags.StringVar(&configPath, "config", "", "WoPan config path")
	flags.StringVar(&quarkConfigPath, "quark-config", "", "Quark config path")
	flags.StringVar(&baiduConfigPath, "baidu-config", "", "Baidu config path")
	flags.StringVar(&embyConfigPath, "emby-config", "", "Emby config path")
	flags.StringVar(&embyUA, "emby-ua", "", "Emby HTTP User-Agent")
	flags.StringVar(&embyClient, "emby-client", "", "Emby authorization client")
	flags.StringVar(&embyDevice, "emby-device", "", "Emby authorization device")
	flags.StringVar(&embyVersion, "emby-version", "", "Emby authorization version")
	flags.IntVar(&threads, "t", 4, "Concurrency threads")
	flags.IntVar(&threads, "threads", 4, "Concurrency threads")
	flags.BoolVar(&streamRelay, "stream", false, "Zero-disk memory streaming relay")

	// Parse arguments preserving subcommands
	var args []string
	var cmd string
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if strings.HasPrefix(arg, "-") {
			if arg == "-h" || arg == "--help" {
				printUsage()
				return
			}
			if (arg == "-c" || arg == "--config" || arg == "--quark-config" || arg == "--baidu-config" || arg == "--emby-config" || arg == "--emby-ua" || arg == "--emby-client" || arg == "--emby-device" || arg == "--emby-version" || arg == "-t" || arg == "--threads") && i+1 < len(os.Args) {
				_ = flags.Parse([]string{arg, os.Args[i+1]})
				i++
				continue
			}
			_ = flags.Parse([]string{arg})
		} else if cmd == "" {
			cmd = arg
		} else {
			args = append(args, arg)
		}
	}

	if cmd == "" || cmd == "help" {
		printUsage()
		return
	}

	if cmd == "version" {
		fmt.Printf("pan-relay v%s\n", Version)
		return
	}
	if threads < 1 || threads > maxConcurrency {
		fmt.Fprintf(os.Stderr, "invalid concurrency %d: must be between 1 and %d\n", threads, maxConcurrency)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "relay":
		handleRelayCommand(ctx, configPath, quarkConfigPath, baiduConfigPath, embyConfigPath, emby.ConfigOverrides{UserAgent: embyUA, Client: embyClient, Device: embyDevice, Version: embyVersion}, threads, streamRelay, args)
	case "quark":
		handleQuarkCommand(ctx, quarkConfigPath, threads, args)
	case "baidu":
		handleBaiduCommand(ctx, baiduConfigPath, threads, args)
	case "wopan":
		if len(args) == 0 {
			fmt.Println("Usage: pan-relay wopan <whoami|ls|upload|download|mkdir|rm|refresh> [args]")
			os.Exit(1)
		}
		handleWoPanCommand(ctx, configPath, threads, args[0], args[1:])
	default:
		// Default backward-compatible fallback to wopan commands (whoami, ls, upload, etc.)
		handleWoPanCommand(ctx, configPath, threads, cmd, args)
	}
}

// -----------------------------------------------------------------------------
// Relay Implementation
// -----------------------------------------------------------------------------

func handleRelayCommand(ctx context.Context, wopanConfig, quarkConfig, baiduConfig, embyConfig string, embyOverrides emby.ConfigOverrides, threads int, streamRelay bool, args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: pan-relay relay <source:path> <destination:path> [--stream] [-t 4]")
		fmt.Println("Example: pan-relay relay quark:/来自：分享/电影.mkv wopan:/emby/movies/ --stream -t 6")
		fmt.Println("Example: pan-relay relay baidu:/我的资源/电影.mkv wopan:/emby/movies/ --stream -t 6")
		os.Exit(1)
	}

	srcStr := args[0]
	dstStr := args[1]

	srcParts := strings.SplitN(srcStr, ":", 2)
	dstParts := strings.SplitN(dstStr, ":", 2)

	if len(srcParts) != 2 || len(dstParts) != 2 {
		fmt.Println("Usage error: specify cloud prefixes like 'quark:<path>' or 'baidu:<path>' or 'wopan:<path>'")
		os.Exit(1)
	}

	srcCloud := strings.ToLower(srcParts[0])
	srcPath := srcParts[1]
	dstCloud := strings.ToLower(dstParts[0])
	dstPath := dstParts[1]

	fileName := filepath.Base(srcPath)

	if streamRelay {
		executeStreamRelay(ctx, wopanConfig, quarkConfig, baiduConfig, embyConfig, embyOverrides, srcCloud, srcPath, dstCloud, dstPath, threads)
		return
	}

	fmt.Printf("[Relay Pipeline] Starting relay: %s [%s] -> %s [%s] (Threads: %d)\n",
		strings.ToUpper(srcCloud), fileName, strings.ToUpper(dstCloud), dstPath, threads)

	// Stage file locally in tmp
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("relay_%d_%s", os.Getpid(), fileName))
	defer os.Remove(tmpFile)

	// 1. Download from Source
	fmt.Printf("\n[Stage 1/2] Downloading from %s...\n", strings.ToUpper(srcCloud))
	switch srcCloud {
	case "quark":
		qCfg, err := quark.LoadConfig(quarkConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Quark Config Error: %v\n", err)
			os.Exit(1)
		}
		qClient := quark.NewClient(qCfg)

		fid := srcPath
		if strings.Contains(srcPath, "/") {
			dirPath := filepath.Dir(srcPath)
			dirFID, err := qClient.ResolvePath(dirPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving Quark path %q: %v\n", dirPath, err)
				os.Exit(1)
			}
			files, err := qClient.ListDir(dirFID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			found := false
			for _, f := range files {
				if !f.IsFolder && f.Name == fileName {
					fid = f.FID
					found = true
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "File %q not found in Quark path %q\n", fileName, dirPath)
				os.Exit(1)
			}
		}
		info, err := qClient.GetDownloadInfo(fid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get Quark download URL: %v\n", err)
			os.Exit(1)
		}
		if info.FileName != "" {
			fileName = info.FileName
		}
		dlURL, headers := info.URL, info.Headers
		dlOpt := quark.DownloadOptions{Concurrency: threads, Retries: 3, ShowProgress: true}
		if err := quark.Download(ctx, dlURL, headers, tmpFile, dlOpt); err != nil {
			os.Exit(1)
		}

	case "baidu":
		bCfg, err := baidu.LoadConfig(baiduConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Baidu Config Error: %v\n", err)
			os.Exit(1)
		}
		bClient := baidu.NewClient(bCfg)
		dlURL, headers, err := bClient.GetDownloadLink(srcPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get Baidu download URL: %v\n", err)
			os.Exit(1)
		}
		dlOpt := baidu.DownloadOptions{Concurrency: threads, Retries: 3, ShowProgress: true}
		if err := baidu.Download(ctx, dlURL, headers, tmpFile, dlOpt); err != nil {
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unsupported source cloud for relay: %s (supported: quark, baidu)\n", srcCloud)
		os.Exit(1)
	}

	// 2. Upload to Destination
	fmt.Printf("\n[Stage 2/2] Uploading to %s...\n", strings.ToUpper(dstCloud))
	switch dstCloud {
	case "wopan":
		wCfg, err := config.Load(wopanConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WoPan Config Error: %v\n", err)
			os.Exit(1)
		}
		wClient, err := client.New(wCfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WoPan Client Error: %v\n", err)
			os.Exit(1)
		}
		if err := wClient.EnsureValidToken(); err != nil {
			fmt.Fprintf(os.Stderr, "WoPan Token Error: %v\n", err)
			os.Exit(1)
		}
		targetDirID, err := wClient.ResolvePath(dstPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving WoPan directory %q: %v\n", dstPath, err)
			os.Exit(1)
		}
		upOpt := uploader.UploadOptions{
			Concurrency:  threads,
			Retries:      3,
			ShowProgress: true,
			RemoteName:   fileName,
		}
		if _, err := uploader.Upload(ctx, wClient, tmpFile, targetDirID, upOpt); err != nil {
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unsupported destination cloud for relay: %s (supported: wopan)\n", dstCloud)
		os.Exit(1)
	}

	fmt.Println("\n[✓] Relay completed! Local temporary staging file deleted automatically.")
}

func executeStreamRelay(ctx context.Context, wopanConfig, quarkConfig, baiduConfig, embyConfig string, embyOverrides emby.ConfigOverrides, srcCloud, srcPath, dstCloud, dstPath string, threads int) {
	if dstCloud != "wopan" {
		fmt.Fprintf(os.Stderr, "Unsupported destination cloud for stream relay: %s (supported: wopan)\n", dstCloud)
		os.Exit(1)
	}

	wCfg, err := config.Load(wopanConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WoPan Config Error: %v\n", err)
		os.Exit(1)
	}
	wClient, err := client.New(wCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WoPan Client Error: %v\n", err)
		os.Exit(1)
	}
	if err := wClient.EnsureValidToken(); err != nil {
		fmt.Fprintf(os.Stderr, "WoPan Token Error: %v\n", err)
		os.Exit(1)
	}
	targetDirID, err := wClient.ResolvePath(dstPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving WoPan directory %q: %v\n", dstPath, err)
		os.Exit(1)
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
	}

	var fileName string
	var fileSize int64
	var getPartReader uploader.PartReaderFunc

	switch srcCloud {
	case "emby":
		eCfg, err := emby.LoadConfig(embyConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Emby Config Error: %v\n", err)
			os.Exit(1)
		}
		eClient, err := emby.NewClient(eCfg, embyOverrides)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Emby Client Error: %v\n", err)
			os.Exit(1)
		}
		if err := eClient.Login(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Emby Login Error: %v\n", err)
			os.Exit(1)
		}
		items, err := eClient.Resolve(ctx, srcPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Emby Search Error: %v\n", err)
			os.Exit(1)
		}
		if len(items) == 0 {
			fmt.Fprintf(os.Stderr, "Emby query %q returned no items\n", srcPath)
			os.Exit(1)
		}
		if len(items) > 1 {
			fmt.Fprintf(os.Stderr, "Emby query %q returned %d items; use IMDb, TMDB, or item:<id> for an exact match\n", srcPath, len(items))
			os.Exit(1)
		}
		item := items[0]
		info, err := eClient.GetMediaInfo(ctx, item.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Emby PlaybackInfo Error: %v\n", err)
			os.Exit(1)
		}
		fileName = eClient.FileName(item, info)
		fileSize = info.Size
		getPartReader = func(ctx context.Context, offset int64, size int64) (io.Reader, error) {
			return eClient.PartReader(ctx, item.ID, info.SourceID, offset, size)
		}

	case "quark":
		qCfg, err := quark.LoadConfig(quarkConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Quark Config Error: %v\n", err)
			os.Exit(1)
		}
		qClient := quark.NewClient(qCfg)

		fid := srcPath
		fileName = filepath.Base(srcPath)

		if strings.Contains(srcPath, "/") {
			dirPath := filepath.Dir(srcPath)
			dirFID, err := qClient.ResolvePath(dirPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving Quark path %q: %v\n", dirPath, err)
				os.Exit(1)
			}
			files, err := qClient.ListDir(dirFID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			found := false
			for _, f := range files {
				if !f.IsFolder && f.Name == fileName {
					fid = f.FID
					fileSize = f.Size
					found = true
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "File %q not found in Quark path %q\n", fileName, dirPath)
				os.Exit(1)
			}
		}

		info, err := qClient.GetDownloadInfo(fid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get Quark download URL: %v\n", err)
			os.Exit(1)
		}
		if info.FileName != "" {
			fileName = info.FileName
		}
		dlURL, headers := info.URL, info.Headers
		fileSize = info.Size

		if fileSize <= 0 {
			headReq, _ := http.NewRequestWithContext(ctx, "HEAD", dlURL, nil)
			for k, v := range headers {
				for _, val := range v {
					headReq.Header.Add(k, val)
				}
			}
			hResp, hErr := httpClient.Do(headReq)
			if hErr == nil {
				fileSize = hResp.ContentLength
				_ = hResp.Body.Close()
			}
		}

		if fileSize <= 0 {
			fmt.Fprintf(os.Stderr, "Error: unable to determine remote file size for %q\n", fileName)
			os.Exit(1)
		}

		getPartReader = func(ctx context.Context, offset int64, size int64) (io.Reader, error) {
			req, err := http.NewRequestWithContext(ctx, "GET", dlURL, nil)
			if err != nil {
				return nil, err
			}
			for k, v := range headers {
				for _, val := range v {
					req.Header.Add(k, val)
				}
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))

			resp, err := httpClient.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()

			return stream.ReadRangeResponse(resp, offset, size)
		}

	case "baidu":
		bCfg, err := baidu.LoadConfig(baiduConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Baidu Config Error: %v\n", err)
			os.Exit(1)
		}
		bClient := baidu.NewClient(bCfg)

		fileName = filepath.Base(srcPath)
		dlURL, headers, err := bClient.GetDownloadLink(srcPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get Baidu download URL: %v\n", err)
			os.Exit(1)
		}

		headReq, _ := http.NewRequestWithContext(ctx, "HEAD", dlURL, nil)
		for k, v := range headers {
			for _, val := range v {
				headReq.Header.Add(k, val)
			}
		}
		hResp, hErr := httpClient.Do(headReq)
		if hErr == nil {
			fileSize = hResp.ContentLength
			_ = hResp.Body.Close()
		}

		if fileSize <= 0 {
			rangeReq, _ := http.NewRequestWithContext(ctx, "GET", dlURL, nil)
			for k, v := range headers {
				for _, val := range v {
					rangeReq.Header.Add(k, val)
				}
			}
			rangeReq.Header.Set("Range", "bytes=0-0")
			rResp, rErr := httpClient.Do(rangeReq)
			if rErr == nil {
				cr := rResp.Header.Get("Content-Range")
				var start, end, total int64
				if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &start, &end, &total); err == nil && total > 0 {
					fileSize = total
				}
				_ = rResp.Body.Close()
			}
		}

		if fileSize <= 0 {
			fmt.Fprintf(os.Stderr, "Error: unable to determine Baidu remote file size for %q\n", fileName)
			os.Exit(1)
		}

		getPartReader = func(ctx context.Context, offset int64, size int64) (io.Reader, error) {
			req, err := http.NewRequestWithContext(ctx, "GET", dlURL, nil)
			if err != nil {
				return nil, err
			}
			for k, v := range headers {
				for _, val := range v {
					req.Header.Add(k, val)
				}
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+size-1))

			resp, err := httpClient.Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()

			return stream.ReadRangeResponse(resp, offset, size)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unsupported source cloud for stream relay: %s (supported: quark, baidu)\n", srcCloud)
		os.Exit(1)
	}

	fmt.Printf("[Stream Relay] Direct memory pipe: %s [%s] (%.2f MB) -> WoPan [%s] (Zero Disk Footprint, Threads: %d)...\n",
		strings.ToUpper(srcCloud), fileName, float64(fileSize)/(1024*1024), dstPath, threads)

	upOpt := uploader.UploadOptions{
		Concurrency:  threads,
		Retries:      3,
		ShowProgress: true,
		RemoteName:   fileName,
	}

	fid, err := uploader.UploadStream(ctx, wClient, fileName, fileSize, targetDirID, getPartReader, upOpt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Stream relay upload failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[✓] Stream relay completed successfully: %s (FID: %s)\n", fileName, fid)
}

// -----------------------------------------------------------------------------
// Baidu Netdisk Implementation
// -----------------------------------------------------------------------------

func handleBaiduCommand(ctx context.Context, configPath string, threads int, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: pan-relay baidu <ls|download|mkdir|rm> [arguments]")
		os.Exit(1)
	}

	bCmd := args[0]
	bArgs := args[1:]

	bCfg, err := baidu.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Baidu Config Error: %v\n", err)
		os.Exit(1)
	}
	bClient := baidu.NewClient(bCfg)

	switch bCmd {
	case "ls", "list":
		target := "/"
		if len(bArgs) > 0 {
			target = bArgs[0]
		}
		files, err := bClient.ListDir(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing Baidu directory: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("=== Baidu Netdisk Directory [%s] (%d items) ===\n", target, len(files))
		fmt.Printf("  %-6s  %-12s  %-20s  %-16s  %s\n", "TYPE", "SIZE", "MODIFIED", "FS_ID", "NAME")
		fmt.Printf("  -------------------------------------------------------------------------------------------------\n")
		for _, f := range files {
			typeStr := "FILE"
			sizeStr := fmt.Sprintf("%.2f MB", float64(f.Size)/(1024*1024))
			if f.IsFolder {
				typeStr = "DIR "
				sizeStr = "-"
			}
			timeStr := f.ServerTime.Format("2006-01-02 15:04:05")
			fmt.Printf("  [%s]  %-12s  %-20s  %-16d  %s\n", typeStr, sizeStr, timeStr, f.FsID, f.Name)
		}

	case "download":
		if len(bArgs) < 1 {
			fmt.Println("Usage: pan-relay baidu download <remote_path> [local_output_path] [-t 4]")
			os.Exit(1)
		}
		remotePath := bArgs[0]
		outputPath := filepath.Base(remotePath)
		if len(bArgs) > 1 {
			outputPath = bArgs[1]
			if fi, err := os.Stat(outputPath); err == nil && fi.IsDir() {
				outputPath = filepath.Join(outputPath, filepath.Base(remotePath))
			}
		}

		dlURL, headers, err := bClient.GetDownloadLink(remotePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting Baidu download link: %v\n", err)
			os.Exit(1)
		}

		opt := baidu.DownloadOptions{Concurrency: threads, Retries: 3, ShowProgress: true}
		if err := baidu.Download(ctx, dlURL, headers, outputPath, opt); err != nil {
			os.Exit(1)
		}

	case "mkdir":
		if len(bArgs) < 2 {
			fmt.Println("Usage: pan-relay baidu mkdir <parent_path> <dir_name>")
			os.Exit(1)
		}
		if err := bClient.Mkdir(bArgs[0], bArgs[1]); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[✓] Baidu directory created: %s/%s\n", bArgs[0], bArgs[1])

	case "rm", "delete":
		if len(bArgs) < 1 {
			fmt.Println("Usage: pan-relay baidu rm <file_or_dir_path>")
			os.Exit(1)
		}
		if err := bClient.Delete(bArgs[0]); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[✓] Baidu item deleted: %s\n", bArgs[0])

	default:
		fmt.Fprintf(os.Stderr, "Unknown baidu command %q\n", bCmd)
		os.Exit(1)
	}
}

// -----------------------------------------------------------------------------
// Quark Implementation
// -----------------------------------------------------------------------------

func handleQuarkCommand(ctx context.Context, configPath string, threads int, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: pan-relay quark <ls|download> [arguments]")
		os.Exit(1)
	}

	qCmd := args[0]
	qArgs := args[1:]

	qCfg, err := quark.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Quark Config Error: %v\n", err)
		os.Exit(1)
	}
	qClient := quark.NewClient(qCfg)

	switch qCmd {
	case "ls", "list":
		target := "0"
		if len(qArgs) > 0 {
			target = qArgs[0]
		}
		targetFID, err := qClient.ResolvePath(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving Quark path %q: %v\n", target, err)
			os.Exit(1)
		}

		files, err := qClient.ListDir(targetFID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing Quark directory: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("=== Quark Directory [%s] (%d items) ===\n", target, len(files))
		fmt.Printf("  %-6s  %-12s  %-20s  %-34s  %s\n", "TYPE", "SIZE", "MODIFIED", "FID", "NAME")
		fmt.Printf("  -------------------------------------------------------------------------------------------------\n")
		for _, f := range files {
			typeStr := "FILE"
			sizeStr := fmt.Sprintf("%.2f MB", float64(f.Size)/(1024*1024))
			if f.IsFolder {
				typeStr = "DIR "
				sizeStr = "-"
			}
			timeStr := f.UpdateTime.Format("2006-01-02 15:04:05")
			fmt.Printf("  [%s]  %-12s  %-20s  %-34s  %s\n", typeStr, sizeStr, timeStr, f.FID, f.Name)
		}

	case "download":
		if len(qArgs) < 1 {
			fmt.Println("Usage: pan-relay quark download <remote_path_or_fid> [local_output_path] [-t 4]")
			os.Exit(1)
		}
		target := qArgs[0]
		fid := target

		if strings.Contains(target, "/") {
			dirPath := filepath.Dir(target)
			fileName := filepath.Base(target)
			dirFID, err := qClient.ResolvePath(dirPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error resolving path %q: %v\n", dirPath, err)
				os.Exit(1)
			}
			files, err := qClient.ListDir(dirFID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			found := false
			for _, f := range files {
				if !f.IsFolder && f.Name == fileName {
					fid = f.FID
					found = true
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "Error: file %q not found in Quark directory %s\n", fileName, dirPath)
				os.Exit(1)
			}
		}

		outputPath := filepath.Base(target)
		if len(qArgs) > 1 {
			outputPath = qArgs[1]
			if fi, err := os.Stat(outputPath); err == nil && fi.IsDir() {
				outputPath = filepath.Join(outputPath, filepath.Base(target))
			}
		}

		dlURL, headers, err := qClient.GetDownloadLink(fid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error obtaining Quark download link: %v\n", err)
			os.Exit(1)
		}

		opt := quark.DownloadOptions{Concurrency: threads, Retries: 3, ShowProgress: true}
		if err := quark.Download(ctx, dlURL, headers, outputPath, opt); err != nil {
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown quark command %q\n", qCmd)
		os.Exit(1)
	}
}

// -----------------------------------------------------------------------------
// WoPan Implementation
// -----------------------------------------------------------------------------

func handleWoPanCommand(ctx context.Context, configPath string, threads int, cmd string, args []string) {
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WoPan Config Error: %v\n", err)
		os.Exit(1)
	}

	cli, err := client.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing client: %v\n", err)
		os.Exit(1)
	}

	switch cmd {
	case "whoami", "user":
		if err := cli.EnsureValidToken(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		user, err := cli.Raw().AppQueryUser()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error querying user: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("=== WoPan Account Info ===")
		fmt.Printf("User Name : %s\n", user.UserName)
		fmt.Printf("User ID   : %s\n", user.UserId)
		fmt.Printf("Reg Time  : %s\n", user.RegisterTime)
		if cfg.FilePath() != "" {
			fmt.Printf("Config    : %s\n", cfg.FilePath())
		}

	case "refresh":
		fmt.Println("Refreshing WoPan access token...")
		ref, err := cli.ForceRefreshToken()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Refresh failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("[✓] Token refreshed successfully!")
		fmt.Printf("Expires In: %d seconds\n", ref.ExpiresIn)
		if cfg.FilePath() != "" {
			fmt.Printf("Updated   : %s\n", cfg.FilePath())
		}

	case "ls", "list":
		if err := cli.EnsureValidToken(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		target := "0"
		if len(args) > 0 {
			target = args[0]
		}
		targetID, err := cli.ResolvePath(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving path %q: %v\n", target, err)
			os.Exit(1)
		}

		files, err := cli.ListDir(targetID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing files: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("=== WoPan Directory [%s] (%d items) ===\n", target, len(files))
		fmt.Printf("  %-6s  %-12s  %-20s  %-34s  %s\n", "TYPE", "SIZE", "MODIFIED", "ID / FID", "NAME")
		fmt.Printf("  -------------------------------------------------------------------------------------------------\n")
		for _, f := range files {
			typeStr := "FILE"
			sizeStr := fmt.Sprintf("%.2f MB", float64(f.Size)/(1024*1024))
			fidStr := f.FID
			if f.IsFolder {
				typeStr = "DIR "
				sizeStr = "-"
				fidStr = f.ID
			}
			timeStr := f.CreateTime.Format("2006-01-02 15:04:05")
			fmt.Printf("  [%s]  %-12s  %-20s  %-34s  %s\n", typeStr, sizeStr, timeStr, fidStr, f.Name)
		}

	case "mkdir":
		if len(args) < 2 {
			fmt.Println("Usage: pan-relay wopan mkdir <parent_path_or_id> <dir_name>")
			os.Exit(1)
		}
		if err := cli.EnsureValidToken(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		parentID, err := cli.ResolvePath(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving parent path %q: %v\n", args[0], err)
			os.Exit(1)
		}
		newID, err := cli.Mkdir(parentID, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating directory: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("[✓] WoPan directory created: %s (ID: %s)\n", args[1], newID)

	case "rm", "delete":
		if len(args) < 1 {
			fmt.Println("Usage: pan-relay wopan rm <file_or_dir_id>")
			os.Exit(1)
		}
		if err := cli.EnsureValidToken(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		if err := cli.Delete(args[0]); err != nil {
			fmt.Fprintf(os.Stderr, "Error deleting %s: %v\n", args[0], err)
			os.Exit(1)
		}
		fmt.Printf("[✓] WoPan item deleted: %s\n", args[0])

	case "upload":
		if len(args) < 1 {
			fmt.Println("Usage: pan-relay wopan upload <local_file> [remote_dir_path_or_id] [-t 4]")
			os.Exit(1)
		}
		localFile := args[0]
		remoteDir := "0"
		if len(args) > 1 {
			remoteDir = args[1]
		}
		if err := cli.EnsureValidToken(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		targetDirID, err := cli.ResolvePath(remoteDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving remote directory %q: %v\n", remoteDir, err)
			os.Exit(1)
		}

		opt := uploader.UploadOptions{Concurrency: threads, Retries: 3, ShowProgress: true}
		if _, err := uploader.Upload(ctx, cli, localFile, targetDirID, opt); err != nil {
			os.Exit(1)
		}

	case "download":
		if len(args) < 1 {
			fmt.Println("Usage: pan-relay wopan download <file_fid_or_path> [local_output_path] [-t 4]")
			os.Exit(1)
		}
		target := args[0]
		fid := target

		if err := cli.EnsureValidToken(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		downloadURL, err := cli.GetDownloadURL(fid)
		if err != nil && strings.HasPrefix(target, "/") {
			dirPath := filepath.Dir(target)
			fileName := filepath.Base(target)
			dirID, rErr := cli.ResolvePath(dirPath)
			if rErr == nil {
				files, lErr := cli.ListDir(dirID)
				if lErr == nil {
					for _, f := range files {
						if !f.IsFolder && f.Name == fileName {
							fid = f.FID
							downloadURL, err = cli.GetDownloadURL(fid)
							break
						}
					}
				}
			}
		}

		if err != nil || downloadURL == "" {
			fmt.Fprintf(os.Stderr, "Error obtaining download URL for %q: %v\n", target, err)
			os.Exit(1)
		}

		outputPath := filepath.Base(target)
		if strings.Contains(target, "/") && !strings.HasPrefix(target, "/") {
			outputPath = "downloaded_file.bin"
		}
		if len(args) > 1 {
			outputPath = args[1]
			if fi, sErr := os.Stat(outputPath); sErr == nil && fi.IsDir() {
				outputPath = filepath.Join(outputPath, filepath.Base(target))
			}
		}

		opt := downloader.DownloadOptions{Concurrency: threads, Retries: 3, ShowProgress: true}
		if err := downloader.Download(ctx, downloadURL, outputPath, opt); err != nil {
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s. Run 'pan-relay help' for usage.\n", cmd)
		os.Exit(1)
	}
}
