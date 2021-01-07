package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	//"overrustlelogs/common"
	"github.com/b-ggs/overrustlelogs/common"
)

func init() {
	configPath := flag.String("config", "/logger/overrustlelogs.toml", "config path")
	flag.Parse()
	common.SetupConfig(*configPath)
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	logs := NewChatLogs()

	dc := common.NewDestiny()
	dl := NewLogger(logs)
	go dl.DestinyLog(dc.Messages())
	go dc.Run()

	twitchLogHandler := func(m <-chan *common.Message) {
		NewLogger(NewChatLogs()).TwitchLog(m)
	}

	tl := NewTwitchLogger(twitchLogHandler)
	go tl.Start()

	sigint := make(chan os.Signal, 1)
	signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
	<-sigint
	logs.Close()
	dc.Stop()
	tl.Stop()
	log.Println("i love you guys, be careful")
	os.Exit(0)
}
