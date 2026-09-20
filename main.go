package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cnzhwei/pan-relay/internal/baidu"
	"github.com/cnzhwei/pan-relay/internal/client"
	"github.com/cnzhwei/pan-relay/internal/config"
	"github.com/cnzhwei/pan-relay/internal/downloader"
	"github.com/cnzhwei/pan-relay/internal/quark"
	"github.com/cnzhwei/pan-relay/internal/uploader"
)

var Version = "2.0.0"

func printUsage() {
	fmt.Printf(`pan-relay (全能多网盘轻量多线程中转工具) v%s

Usage:
  pan-relay [options] <command> [arguments]

=== 1. 跨网盘全自动中转流水线 (Relay) ===
  relay <source> <destination>
      从指定源网盘拉取文件并多线程推流至目标网盘，完成后自动销毁本地临时切片。
      示例:
        pan-relay relay quark:/来自：分享/电影.mkv wopan:/emby/movies/ -t 6
        pan-relay relay baidu:/影视资源/电影.mkv wopan:/emby/movies/ -t 6

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
	var threads int

	flags := flag.NewFlagSet("pan-relay", flag.ExitOnError)
	flags.StringVar(&configPath, "c", "", "WoPan config path")
	flags.StringVar(&configPath, "config", "", "WoPan config path")
	flags.StringVar(&quarkConfigPath, "quark-config", "", "Quark config path")
	flags.StringVar(&baiduConfigPath, "baidu-config", "", "Baidu config path")
	flags.IntVar(&threads, "t", 4, "Concurrency threads")
	flags.IntVar(&threads, "threads", 4, "Concurrency threads")

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
			if (arg == "-c" || arg == "--config" || arg == "--quark-config" || arg == "--baidu-config" || arg == "-t" || arg == "--threads") && i+1 < len(os.Args) {
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

	ctx := context.Background()

	switch cmd {
	case "relay":
		handleRelayCommand(ctx, configPath, quarkConfigPath, baiduConfigPath, threads, args)
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

func handleRelayCommand(ctx context.Context, wopanConfig, quarkConfig, baiduConfig string, threads int, args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: pan-relay relay <source:path> <destination:path> [-t 4]")
		fmt.Println("Example: pan-relay relay quark:/来自：分享/电影.mkv wopan:/emby/movies/ -t 6")
		fmt.Println("Example: pan-relay relay baidu:/我的资源/电影.mkv wopan:/emby/movies/ -t 6")
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
		dlURL, headers, err := qClient.GetDownloadLink(fid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get Quark download URL: %v\n", err)
			os.Exit(1)
		}
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
		upOpt := uploader.UploadOptions{Concurrency: threads, Retries: 3, ShowProgress: true}
		if _, err := uploader.Upload(ctx, wClient, tmpFile, targetDirID, upOpt); err != nil {
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unsupported destination cloud for relay: %s (supported: wopan)\n", dstCloud)
		os.Exit(1)
	}

	fmt.Println("\n[✓] Relay completed! Local temporary staging file deleted automatically.")
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
