package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cnzhwei/wopan-cli/internal/client"
	"github.com/cnzhwei/wopan-cli/internal/config"
	"github.com/cnzhwei/wopan-cli/internal/downloader"
	"github.com/cnzhwei/wopan-cli/internal/quark"
	"github.com/cnzhwei/wopan-cli/internal/uploader"
)

var Version = "1.4.0"

func printUsage() {
	fmt.Printf(`WoPan & Quark Relay CLI (沃家云盘与夸克轻量多线程中转工具) v%s

Usage:
  wopan-cli [options] <command> [arguments]

=== 沃家云盘 (WoPan) 核心命令 ===
  whoami                    显示当前登录账号信息与空间容量
  ls [path|dir_id]          浏览沃家网盘目录 (默认根目录 /)
  mkdir <parent> <name>     创建新目录
  rm <id>                   删除指定文件或目录 ID
  upload <local> [remote]   多线程并发上传本地文件至沃家网盘 (默认 4 线程)
  download <fid|path> [dst] 多线程并发下载沃家网盘文件 (支持 Range 分片加速)
  refresh                   强制使用 refresh_token 刷新当前登录凭据

=== 夸克网盘 (Quark) 组件命令 ===
  quark ls [path|fid]       浏览夸克网盘目录 (如 / 或 /来自：分享)
  quark download <fid|path> [dst]
                            从夸克网盘多线程并发高速下载 (绕过 Web 文件大小限制)

=== 跨盘极速中转 (Relay) ===
  relay quark:<remote_path> wopan:<remote_dir>
                            从夸克下载并自动多线程推流转存至沃家云盘，完成后自动销毁临时文件

=== 通用选项 ===
  -c, --config <path>       指定 WoPan 配置文件路径 (默认自动查找 ./wopan_config.json)
  --quark-config <path>     指定 Quark 配置文件路径 (默认自动查找 ./quark_config.json)
  -t, --threads <num>       上传/下载并发线程数 (默认: 4)
  version                   显示当前程序版本
`, Version)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	var configPath string
	var quarkConfigPath string
	var threads int

	flags := flag.NewFlagSet("wopan-cli", flag.ExitOnError)
	flags.StringVar(&configPath, "c", "", "WoPan config path")
	flags.StringVar(&configPath, "config", "", "WoPan config path")
	flags.StringVar(&quarkConfigPath, "quark-config", "", "Quark config path")
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
			if (arg == "-c" || arg == "--config" || arg == "--quark-config" || arg == "-t" || arg == "--threads") && i+1 < len(os.Args) {
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
		fmt.Printf("wopan-cli v%s\n", Version)
		return
	}

	ctx := context.Background()

	// 1. Dispatch Quark commands
	if cmd == "quark" {
		handleQuarkCommand(ctx, quarkConfigPath, threads, args)
		return
	}

	// 2. Dispatch Relay command (Quark -> WoPan)
	if cmd == "relay" {
		handleRelayCommand(ctx, configPath, quarkConfigPath, threads, args)
		return
	}

	// 3. Dispatch WoPan commands
	handleWoPanCommand(ctx, configPath, threads, cmd, args)
}

func handleQuarkCommand(ctx context.Context, configPath string, threads int, args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: wopan-cli quark <ls|download> [arguments]")
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
			fmt.Println("Usage: wopan-cli quark download <remote_path_or_fid> [local_output_path] [-t 4]")
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

		opt := quark.DownloadOptions{
			Concurrency:  threads,
			Retries:      3,
			ShowProgress: true,
		}
		if err := quark.Download(ctx, dlURL, headers, outputPath, opt); err != nil {
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown quark command %q. Supported: ls, download\n", qCmd)
		os.Exit(1)
	}
}

func handleRelayCommand(ctx context.Context, wopanConfig, quarkConfig string, threads int, args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: wopan-cli relay quark:<remote_path_or_fid> wopan:<remote_dir_path_or_id> [-t 4]")
		os.Exit(1)
	}

	srcStr := args[0]
	dstStr := args[1]

	if !strings.HasPrefix(srcStr, "quark:") || !strings.HasPrefix(dstStr, "wopan:") {
		fmt.Println("Usage error: source must be 'quark:<path>' and destination must be 'wopan:<path>'")
		os.Exit(1)
	}

	quarkPath := strings.TrimPrefix(srcStr, "quark:")
	wopanPath := strings.TrimPrefix(dstStr, "wopan:")

	// 1. Init Quark
	qCfg, err := quark.LoadConfig(quarkConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Quark Config Error: %v\n", err)
		os.Exit(1)
	}
	qClient := quark.NewClient(qCfg)

	// 2. Init WoPan
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

	// 3. Resolve Quark FID and FileName
	quarkFID := quarkPath
	fileName := filepath.Base(quarkPath)
	if strings.Contains(quarkPath, "/") {
		dirPath := filepath.Dir(quarkPath)
		dirFID, err := qClient.ResolvePath(dirPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving Quark path %q: %v\n", dirPath, err)
			os.Exit(1)
		}
		files, err := qClient.ListDir(dirFID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error listing Quark dir %s: %v\n", dirFID, err)
			os.Exit(1)
		}
		found := false
		for _, f := range files {
			if !f.IsFolder && f.Name == fileName {
				quarkFID = f.FID
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "File %q not found in Quark path %q\n", fileName, dirPath)
			os.Exit(1)
		}
	}

	// 4. Resolve WoPan target directory
	targetDirID, err := wClient.ResolvePath(wopanPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving WoPan directory %q: %v\n", wopanPath, err)
		os.Exit(1)
	}

	fmt.Printf("[Relay Pipeline] Starting relay: Quark [%s] -> WoPan [%s] (Threads: %d)\n", fileName, wopanPath, threads)

	// 5. Stage download in temporary location
	tmpFile := filepath.Join(os.TempDir(), fmt.Sprintf("wopan_relay_%d_%s", os.Getpid(), fileName))
	defer os.Remove(tmpFile)

	dlURL, headers, err := qClient.GetDownloadLink(quarkFID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get Quark download URL: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("[Stage 1/2] Downloading from Quark...")
	dlOpt := quark.DownloadOptions{
		Concurrency:  threads,
		Retries:      3,
		ShowProgress: true,
	}
	if err := quark.Download(ctx, dlURL, headers, tmpFile, dlOpt); err != nil {
		fmt.Fprintf(os.Stderr, "Download failed: %v\n", err)
		os.Exit(1)
	}

	// 6. Stage upload to WoPan
	fmt.Println("\n[Stage 2/2] Uploading to WoPan...")
	upOpt := uploader.UploadOptions{
		Concurrency:  threads,
		Retries:      3,
		ShowProgress: true,
	}
	_, err = uploader.Upload(ctx, wClient, tmpFile, targetDirID, upOpt)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Upload failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("\n[✓] Relay completed! Local temporary staging file deleted automatically.")
}

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
			fmt.Println("Usage: wopan-cli mkdir <parent_path_or_id> <dir_name>")
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
		fmt.Printf("[✓] Directory created: %s (ID: %s)\n", args[1], newID)

	case "rm", "delete":
		if len(args) < 1 {
			fmt.Println("Usage: wopan-cli rm <file_or_dir_id>")
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
		fmt.Printf("[✓] Deleted successfully: %s\n", args[0])

	case "upload":
		if len(args) < 1 {
			fmt.Println("Usage: wopan-cli upload <local_file> [remote_dir_path_or_id] [-t 4]")
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

		opt := uploader.UploadOptions{
			Concurrency:  threads,
			Retries:      3,
			ShowProgress: true,
		}
		_, err = uploader.Upload(ctx, cli, localFile, targetDirID, opt)
		if err != nil {
			os.Exit(1)
		}

	case "download":
		if len(args) < 1 {
			fmt.Println("Usage: wopan-cli download <file_fid_or_path> [local_output_path] [-t 4]")
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

		opt := downloader.DownloadOptions{
			Concurrency:  threads,
			Retries:      3,
			ShowProgress: true,
		}
		if err := downloader.Download(ctx, downloadURL, outputPath, opt); err != nil {
			os.Exit(1)
		}

	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s. Run 'wopan-cli help' for usage.\n", cmd)
		os.Exit(1)
	}
}
