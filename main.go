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
	"github.com/cnzhwei/wopan-cli/internal/uploader"
)

var Version = "1.0.0"

func printUsage() {
	fmt.Printf(`WoPan CLI (沃家云盘轻量多线程中转工具) v%s

Usage:
  wopan-cli [options] <command> [arguments]

Commands:
  whoami                    显示当前登录账号信息与空间容量
  ls [path|dir_id]          浏览目录 (默认根目录 /)
  mkdir <parent> <name>     创建新目录
  rm <id>                   删除指定文件或目录 ID
  upload <local> [remote]   多线程并发上传本地文件至网盘目录
  download <fid> [local]    多线程并发下载网盘文件 (支持 Range 分片加速)
  refresh                   强制使用 refresh_token 刷新当前登录凭据
  version                   显示当前程序版本

Options:
  -c, --config <path>       指定配置文件路径 (默认自动查找 ./wopan_config.json 或 ~/.config/wopan-cli/config.json)
  -t, --threads <num>       上传/下载并发线程数 (默认: 4)

Environment Variables:
  WOPAN_REFRESH_TOKEN       沃家云盘 Refresh Token (长效凭据)
  WOPAN_ACCESS_TOKEN        沃家云盘 Access Token (可选，会自动刷新)
  WOPAN_FAMILY_ID           家庭空间 ID (可选，不设默认个人空间)
`, Version)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	var configPath string
	var threads int

	flags := flag.NewFlagSet("wopan-cli", flag.ExitOnError)
	flags.StringVar(&configPath, "c", "", "Config file path")
	flags.StringVar(&configPath, "config", "", "Config file path")
	flags.IntVar(&threads, "t", 4, "Concurrency threads")
	flags.IntVar(&threads, "threads", 4, "Concurrency threads")

	// Filter out options before the subcommand
	var args []string
	var cmd string
	for i := 1; i < len(os.Args); i++ {
		arg := os.Args[i]
		if strings.HasPrefix(arg, "-") {
			// flags
			if arg == "-h" || arg == "--help" {
				printUsage()
				return
			}
			if (arg == "-c" || arg == "--config" || arg == "-t" || arg == "--threads") && i+1 < len(os.Args) {
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

	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	cli, err := client.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing client: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()

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

		// First, test if target is a direct FID
		downloadURL, err := cli.GetDownloadURL(fid)
		if err != nil && strings.HasPrefix(target, "/") {
			// If direct FID failed and target looks like a path, search by path
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

		// Output path
		outputPath := filepath.Base(target)
		if strings.Contains(target, "/") && !strings.HasPrefix(target, "/") {
			outputPath = "downloaded_file.bin"
		}
		if len(args) > 1 {
			outputPath = args[1]
			// if outputPath is a directory, append filename
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
