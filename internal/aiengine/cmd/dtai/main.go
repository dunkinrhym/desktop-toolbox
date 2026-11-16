// dtai：AI 组件下载与引擎调用的命令行工具（开发调试与 CI 冒烟测试用，不随安装包发布）。
//
//	dtai list                                   列出组件与当前平台的下载大小
//	dtai install -root DIR id...                下载并安装组件（断点续传 + SHA256 校验）
//	dtai call -root DIR -python PY -script S [-ffmpeg F] cmd 'json'   通过常驻引擎执行一条命令
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"desktoptoolbox/internal/aiengine"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法：dtai list | install | call")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	m, err := aiengine.LoadManifest()
	must(err)
	switch os.Args[1] {
	case "list":
		plat := aiengine.Platform()
		var sum int64
		for _, c := range m.Components {
			n := c.TotalSize(plat)
			sum += n
			fmt.Printf("%-10s %9.1f MB  %s\n", c.ID, float64(n)/1e6, c.Name)
		}
		fmt.Printf("合计 %.1f MB（%s），本机内存 %.1f GB，推荐 %s\n", float64(sum)/1e6, plat,
			float64(aiengine.TotalRAM())/(1<<30), aiengine.RecommendLLM(aiengine.TotalRAM()))
	case "install":
		fs := flag.NewFlagSet("install", flag.ExitOnError)
		root := fs.String("root", "", "AI 数据目录")
		must(fs.Parse(os.Args[2:]))
		mgr := aiengine.NewManager(*root)
		for _, id := range fs.Args() {
			c, err := m.Component(id)
			must(err)
			t := time.Now()
			last := time.Time{}
			err = mgr.Install(ctx, c, func(p aiengine.Progress) {
				if time.Since(last) > 3*time.Second || p.Stage == "done" {
					fmt.Printf("  %s %s %5.1f%% %6.1f MB/s %s\n", p.Component, p.Stage, 100*float64(p.Done)/float64(max(1, p.Total)),
						float64(p.Speed)/1e6, p.Host)
					last = time.Now()
				}
			})
			must(err)
			fmt.Printf("已安装 %s（%.1f MB，%s）\n", id, float64(mgr.DiskSize(c))/1e6, time.Since(t).Round(time.Second))
		}
	case "call":
		fs := flag.NewFlagSet("call", flag.ExitOnError)
		root := fs.String("root", "", "AI 数据目录")
		py := fs.String("python", "python3", "Python")
		script := fs.String("script", "ai-engine/dtai_main.py", "引擎脚本")
		ff := fs.String("ffmpeg", "", "ffmpeg")
		timeout := fs.Duration("timeout", 30*time.Minute, "超时")
		must(fs.Parse(os.Args[2:]))
		if fs.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "缺少命令")
			os.Exit(2)
		}
		var args any = map[string]any{}
		if fs.NArg() > 1 {
			must(json.Unmarshal([]byte(fs.Arg(1)), &args))
		}
		e := &aiengine.Engine{Python: *py, Script: *script, Root: *root, FFmpeg: *ff}
		defer e.Stop()
		cctx, cc := context.WithTimeout(ctx, *timeout)
		defer cc()
		t := time.Now()
		res, err := e.Call(cctx, fs.Arg(0), args, func(ev aiengine.Event) {
			if ev.Event == "progress" {
				fmt.Fprintf(os.Stderr, "  [%3d%%] %s\n", ev.Pct, ev.Msg)
			}
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误（%s）：%v\n", time.Since(t).Round(time.Millisecond), err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "完成，用时 %s\n", time.Since(t).Round(time.Millisecond))
		os.Stdout.Write(res)
		fmt.Println()
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}
